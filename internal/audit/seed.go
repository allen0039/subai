package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedDefaults imports the versioned default rule package on first boot (§4:
// 初始化导入数据库). Existing admin-modified rules are never overwritten —
// seeding only fills rule_ids that have no rows at all.
func SeedDefaults(ctx context.Context, pool *pgxpool.Pool, ruleYAML []byte) error {
	rules, err := LoadRuleFile(ruleYAML)
	if err != nil {
		return fmt.Errorf("parse default rules: %w", err)
	}
	for _, r := range rules {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_rules WHERE rule_id=$1)`, r.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		matcherJSON, err := json.Marshal(r.Matcher)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO audit_rules(rule_id, version, category, title, description, scope, matcher,
				action, severity, message, fixture_set, enabled, source, modified_by)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'builtin','seed')`,
			r.ID, r.Version, r.Category, r.Title, r.Description, r.Scope, matcherJSON,
			r.Action, r.Severity, r.Message, r.FixtureSet, r.Enabled); err != nil {
			return err
		}
	}
	return nil
}

// LoadRuleset compiles the latest version of every rule from the database
// into a snapshot. The snapshot content hash identifies the rule version for
// cache keys and audit events (§6.2).
func LoadRuleset(ctx context.Context, pool *pgxpool.Pool) (*Ruleset, error) {
	rows, err := pool.Query(ctx, `
			SELECT DISTINCT ON (rule_id)
				rule_id, version, category, title, description, array_to_json(scope)::text, matcher::text,
				action, severity, message, fixture_set, enabled
			FROM audit_rules ORDER BY rule_id, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var defs []Rule
	h := sha256.New()
	for rows.Next() {
		var r Rule
		var scopeText, matcherText string
		if err := rows.Scan(&r.ID, &r.Version, &r.Category, &r.Title, &r.Description, &scopeText, &matcherText,
			&r.Action, &r.Severity, &r.Message, &r.FixtureSet, &r.Enabled); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(matcherText), &r.Matcher); err != nil {
			return nil, fmt.Errorf("rule %s matcher: %w", r.ID, err)
		}
		if err := json.Unmarshal([]byte(scopeText), &r.Scope); err != nil {
			return nil, fmt.Errorf("rule %s scope: %w", r.ID, err)
		}
		defs = append(defs, r)
		canonical, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("rule %s canonicalize: %w", r.ID, err)
		}
		_, _ = h.Write(canonical)
		_, _ = h.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rs, err := Compile(defs, 1)
	if err != nil {
		return nil, err
	}
	rs.Version = hex.EncodeToString(h.Sum(nil))[:16]
	return rs, nil
}

// LoadPolicyVersion returns the active audit policy version (0 = none).
func LoadPolicyVersion(ctx context.Context, pool *pgxpool.Pool) int64 {
	var v int64
	_ = pool.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM audit_policies WHERE status='active'`).Scan(&v)
	return v
}

// SeedSyntheticPrice inserts a synthetic price version on first boot (D-008).
// It is activated so the mock pipeline works end to end; the admin UI labels
// it synthetic and production_ready requires a verified official version.
func SeedSyntheticPrice(ctx context.Context, pool *pgxpool.Pool, models map[string][3]string) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM price_versions WHERE origin='synthetic')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO price_versions(origin, status, notes, source_url)
		VALUES('synthetic','draft','Synthetic seed prices for testing only — NOT official pricing.','')
		RETURNING id::text`).Scan(&versionID); err != nil {
		return err
	}
	for model, rates := range models {
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_prices(price_version_id, model, input_per_mtok, cached_input_per_mtok, output_per_mtok)
			VALUES($1,$2,$3,$4,$5)`, versionID, model, rates[0], rates[1], rates[2]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE price_versions SET status='active', activated_at=now() WHERE id=$1`, versionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
