package admin

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"subai/internal/audit"
	"subai/internal/billing"
	"subai/internal/storage"
)

// ── Budget policies & periods & ledger ───────────────────────────────────────

func (s *Server) listBudgetPolicies(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT id::text, owner_type, COALESCE(owner_member_id::text,''), COALESCE(owner_key_id::text,''),
		       COALESCE(owner_account_id::text,''), COALESCE(owner_group_id::text,''),
		       period, timezone, mode, COALESCE(amount::text,''), COALESCE(percent_bps, -1),
		       COALESCE(base_policy_id::text,''), status, version
		FROM budget_policies ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, ownerType, member, key, account, group, period, tz, mode, amount, base, status string
		var bps int
		var version int
		if err := rows.Scan(&id, &ownerType, &member, &key, &account, &group, &period, &tz, &mode, &amount, &bps, &base, &status, &version); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "owner_type": ownerType, "owner_member_id": member, "owner_key_id": key,
			"owner_account_id": account, "owner_group_id": group, "period": period, "timezone": tz,
			"mode": mode, "amount": amount, "percent_bps": bps, "base_policy_id": base, "status": status, "version": version,
		})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// createBudgetPolicy validates §18.2 constraints: fixed needs amount, percent
// needs percent_bps∈[0,10000] and a fixed base policy of the same period;
// percent-on-percent is rejected (cycle-free by construction).
func (s *Server) createBudgetPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OwnerType      string `json:"owner_type"`
		OwnerMemberID  string `json:"owner_member_id"`
		OwnerKeyID     string `json:"owner_key_id"`
		OwnerAccountID string `json:"owner_account_id"`
		OwnerGroupID   string `json:"owner_group_id"`
		Period         string `json:"period"`
		Timezone       string `json:"timezone"`
		Mode           string `json:"mode"`
		Amount         string `json:"amount"`
		PercentBps     *int   `json:"percent_bps"`
		BasePolicyID   string `json:"base_policy_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&req); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	ownerTypes := map[string]bool{"member": true, "key": true, "account": true, "group": true, "key_account": true, "key_group": true}
	if !ownerTypes[req.OwnerType] {
		s.writeErr(w, 400, "invalid owner_type")
		return
	}
	// Validate owner IDs
	if req.OwnerMemberID != "" && !resourceUUID.MatchString(req.OwnerMemberID) {
		s.writeErr(w, 400, "invalid owner_member_id")
		return
	}
	if req.OwnerKeyID != "" && !resourceUUID.MatchString(req.OwnerKeyID) {
		s.writeErr(w, 400, "invalid owner_key_id")
		return
	}
	if req.OwnerAccountID != "" && !resourceUUID.MatchString(req.OwnerAccountID) {
		s.writeErr(w, 400, "invalid owner_account_id")
		return
	}
	if req.OwnerGroupID != "" && !resourceUUID.MatchString(req.OwnerGroupID) {
		s.writeErr(w, 400, "invalid owner_group_id")
		return
	}
	if req.Period != "day" && req.Period != "week" {
		s.writeErr(w, 400, "period must be day|week")
		return
	}
	if req.Timezone == "" {
		req.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		s.writeErr(w, 400, "invalid timezone")
		return
	}
	var amount any
	switch req.Mode {
	case "fixed":
		if req.Amount == "" {
			s.writeErr(w, 400, "amount required for fixed mode")
			return
		}
		d, err := decimal.NewFromString(req.Amount)
		if err != nil || d.IsNegative() {
			s.writeErr(w, 400, "invalid amount")
			return
		}
		amount = d
	case "percent":
		if req.PercentBps == nil || *req.PercentBps < 0 || *req.PercentBps > 10000 {
			s.writeErr(w, 400, "percent_bps must be 0..10000")
			return
		}
		if !resourceUUID.MatchString(req.BasePolicyID) {
			s.writeErr(w, 400, "valid base_policy_id required for percent mode")
			return
		}
		var mode, period string
		var baseBase *string
		if err := s.DB.Pool.QueryRow(r.Context(),
			`SELECT mode, period, base_policy_id FROM budget_policies WHERE id=$1 AND status='active'`, req.BasePolicyID).
			Scan(&mode, &period, &baseBase); err != nil || mode != "fixed" || period != req.Period || baseBase != nil {
			s.writeErr(w, 400, "base policy must be active fixed-mode with the same period")
			return
		}
	default:
		s.writeErr(w, 400, "mode must be fixed|percent")
		return
	}
	var id string
	err := s.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO budget_policies(owner_type, owner_member_id, owner_key_id, owner_account_id, owner_group_id,
			period, timezone, mode, amount, percent_bps, base_policy_id, created_by)
		VALUES($1,NULLIF($2,'')::uuid,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,
			$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,$12) RETURNING id::text`,
		req.OwnerType, req.OwnerMemberID, req.OwnerKeyID, req.OwnerAccountID, req.OwnerGroupID,
		req.Period, req.Timezone, req.Mode, amount, req.PercentBps, req.BasePolicyID, actorFrom(r)).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "budget_policy.create", "budget_policy", id,
		storage.SanitizeForAdminEvent(map[string]any{"mode": req.Mode, "amount": req.Amount}), "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) listBudgetPeriods(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"period_start"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT p.id::text, p.policy_id::text, p.period_start::text, p.period_end::text, p.timezone,
		       p.limit_snapshot::text, p.spent::text, p.reserved::text
		FROM budget_periods p ORDER BY p.period_start DESC, p.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, policy, start, end, tz, limit, spent, reserved string
		if err := rows.Scan(&id, &policy, &start, &end, &tz, &limit, &spent, &reserved); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "policy_id": policy, "period_start": start, "period_end": end,
			"timezone": tz, "limit": limit, "spent": spent, "reserved": reserved})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) listLedger(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT l.id::text, l.request_id::text, l.api_key_id::text, COALESCE(l.account_id::text,''),
		       l.input_tokens, l.cached_input_tokens, l.output_tokens, l.cost::text,
		       COALESCE(l.price_version_id::text,''), l.entry_type, l.created_at::text
		FROM usage_ledger l ORDER BY l.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, reqID, keyID, account, input, cached, output, cost, priceVersion, entryType, created string
		if err := rows.Scan(&id, &reqID, &keyID, &account, &input, &cached, &output, &cost, &priceVersion, &entryType, &created); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "request_id": reqID, "api_key_id": keyID, "account_id": account,
			"input_tokens": input, "cached_input_tokens": cached, "output_tokens": output, "cost": cost,
			"price_version_id": priceVersion, "entry_type": entryType, "created_at": created})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// ── Prices ───────────────────────────────────────────────────────────────────

func (s *Server) listPriceVersions(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT v.id::text, v.origin, v.status, COALESCE(v.source_url,''), COALESCE(v.source_hash,''),
		       v.fetched_at::text, COALESCE(v.activated_at::text,''), v.notes,
		       (SELECT count(*) FROM model_prices m WHERE m.price_version_id=v.id)
		FROM price_versions v ORDER BY v.created_at DESC, v.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, origin, status, url, hash, fetched, activated, notes string
		var models int
		if err := rows.Scan(&id, &origin, &status, &url, &hash, &fetched, &activated, &notes, &models); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "origin": origin, "status": status, "source_url": url,
			"source_hash": hash, "fetched_at": fetched, "activated_at": activated, "notes": notes, "model_count": models})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// syncPrices attempts an official-source sync (P0-04). Until the parser is
// verified against the real page, it records the attempt and never activates
// anything unverified (§23: 不伪造同步功能).
func (s *Server) syncPrices(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"activated": false, "status": "not_verified"}
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "prices.sync", "price_version", "",
		map[string]any{"outcome": "parser_unverified"}, "")
	s.writeJSON(w, 200, result)
}

// overridePrice creates a manual price version and activates it (§17.2).
func (s *Server) overridePrice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models []struct {
			Model       string `json:"model"`
			Input       string `json:"input_per_mtok"`
			CachedInput string `json:"cached_input_per_mtok"`
			Output      string `json:"output_per_mtok"`
		} `json:"models"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Models) == 0 {
		s.writeErr(w, 400, "models required")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var versionID string
	if err := tx.QueryRow(r.Context(), `
		INSERT INTO price_versions(origin, status, notes, source_url)
		VALUES('manual','draft',$1,'manual-entry') RETURNING id::text`, req.Notes).Scan(&versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	for _, m := range req.Models {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO model_prices(price_version_id, model, input_per_mtok, cached_input_per_mtok, output_per_mtok)
			VALUES($1,$2,$3,$4,$5)`, versionID, m.Model, m.Input, m.CachedInput, m.Output); err != nil {
			s.writeErr(w, 400, "invalid price for "+m.Model+": "+err.Error())
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `UPDATE price_versions SET status='superseded' WHERE status='active'`); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE price_versions SET status='active', activated_at=now() WHERE id=$1`, versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "prices.override", "price_version", versionID, nil, "")
	s.writeJSON(w, 201, map[string]any{"id": versionID})
}

// ── Audit rules & policies & events ──────────────────────────────────────────

func (s *Server) listAuditRules(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"rule_id"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT DISTINCT ON (rule_id) rule_id, id::text, version, category, title, description,
		       scope::text, matcher::text, action, severity, message, enabled, source, modified_by
		FROM audit_rules ORDER BY rule_id, version DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var ruleID, id, category, title, description, scope, matcher, action, severity, message, source, modifiedBy string
		var version int
		var enabled bool
		if err := rows.Scan(&ruleID, &id, &version, &category, &title, &description, &scope, &matcher, &action, &severity, &message, &enabled, &source, &modifiedBy); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "rule_id": ruleID, "version": version, "category": category,
			"title": title, "description": description, "scope": scope, "matcher": matcher, "action": action,
			"severity": severity, "message": message, "enabled": enabled, "source": source, "modified_by": modifiedBy})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// patchAuditRule creates a NEW version (restore-default also versions, §20);
// the historical rows are preserved for diffs and recovery.
func (s *Server) patchAuditRule(w http.ResponseWriter, r *http.Request, rowID string) {
	var req struct {
		Enabled *bool           `json:"enabled"`
		Matcher json.RawMessage `json:"matcher"`
		Action  *string         `json:"action"`
		Message *string         `json:"message"`
		Version int             `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version <= 0 {
		s.writeErr(w, 400, "valid version required")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var ruleID string
	var curVersion int
	var matcherJSON, scope, category, action, message, source string
	var enabled bool
	err = tx.QueryRow(r.Context(), `
		SELECT rule_id, version, matcher::text, array_to_json(scope)::text, category, action, message, enabled, source
		FROM audit_rules WHERE id=$1 FOR UPDATE`, rowID).
		Scan(&ruleID, &curVersion, &matcherJSON, &scope, &category, &action, &message, &enabled, &source)
	if err != nil {
		s.writeErr(w, 404, "rule not found")
		return
	}
	if curVersion != req.Version {
		s.writeErr(w, 409, "version conflict")
		return
	}
	newMatcher := json.RawMessage(matcherJSON)
	if req.Matcher != nil {
		newMatcher = req.Matcher
		// Generic form controls submit text as a JSON string. Accept that
		// representation while still requiring the contained value to be a
		// matcher object below.
		if len(newMatcher) > 0 && newMatcher[0] == '"' {
			var text string
			if err := json.Unmarshal(newMatcher, &text); err != nil {
				s.writeErr(w, 400, "invalid matcher")
				return
			}
			newMatcher = json.RawMessage(text)
		}
	}
	newAction, newMessage := action, message
	if req.Action != nil {
		newAction = *req.Action
	}
	if req.Message != nil {
		newMessage = *req.Message
	}
	newEnabled := enabled
	if req.Enabled != nil {
		newEnabled = *req.Enabled
	}
	// Validate the resulting matcher compiles before persisting.
	if err := validateRulePatch(category, scope, newMatcher, newAction); err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO audit_rules(rule_id, version, category, title, description, scope, matcher, action, severity, message, enabled, source, modified_by)
		SELECT rule_id, version+1, category, title, description, scope, $2, $3, severity, $4, $5, source, $6
		FROM audit_rules WHERE rule_id=$1 ORDER BY version DESC LIMIT 1`,
		ruleID, newMatcher, newAction, newMessage, newEnabled, actorFrom(r)); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "audit_rule.update", "audit_rule", ruleID, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func validateRulePatch(category, scopeJSON string, matcher json.RawMessage, action string) error {
	if action != "flag" && action != "review" && action != "block" && action != "reject" && action != "unsupported" {
		return fmt.Errorf("invalid action %q", action)
	}
	var m audit.Matcher
	if err := json.Unmarshal(matcher, &m); err != nil {
		return fmt.Errorf("invalid matcher: %v", err)
	}
	var scope []string
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		return fmt.Errorf("invalid scope: %v", err)
	}
	_, err := audit.Compile([]audit.Rule{{
		ID: "preview", Version: 1, Category: category, Scope: scope, Matcher: m,
		Action: action, Enabled: true,
	}}, 1)
	return err
}

// validateAuditRule runs local matching only (§17.2); sending the sample to
// the external moderation API requires the UI's explicit disclosure flow.
func (s *Server) validateAuditRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text  string `json:"text"`
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	doc := &audit.AuditDocument{Coverage: audit.CoverageFull, Segments: []audit.Segment{{
		Role: "user", Source: req.Scope, Type: "text", Text: req.Text,
	}}}
	hits, secretBlocked := s.Rules.RunLocal(doc)
	out := map[string]any{"hits": hits, "secret_blocked": secretBlocked}
	s.writeJSON(w, 200, out)
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at"})
	q := `SELECT e.id::text, e.request_id::text, e.api_key_id::text, e.decision, e.coverage,
	      e.hits::text, e.summary, e.policy_version, e.model_version, e.duration_ms, e.cache_state,
	      COALESCE(e.error_type,''), e.created_at::text,
	      (SELECT COALESCE(json_agg(json_build_object('outcome', rv.outcome, 'note', rv.note))::text, '[]')
	       FROM audit_reviews rv WHERE rv.audit_event_id=e.id) AS reviews
	      FROM audit_events e WHERE 1=1`
	args := []any{}
	if d := r.URL.Query().Get("decision"); d != "" {
		args = append(args, d)
		q += fmt.Sprintf(" AND e.decision=$%d", len(args))
	}
	if req := r.URL.Query().Get("request_id"); req != "" {
		args = append(args, req)
		q += fmt.Sprintf(" AND e.request_id::text=$%d", len(args))
	}
	args = append(args, limit, offset)
	q += fmt.Sprintf(" ORDER BY e.created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.DB.Pool.Query(r.Context(), q, args...)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, reqID, keyID, decision, coverage, hits, summary, model, cache, errType, created, reviews string
		var policyVer, duration int
		if err := rows.Scan(&id, &reqID, &keyID, &decision, &coverage, &hits, &summary, &policyVer, &model,
			&duration, &cache, &errType, &created, &reviews); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		var reviewsArr []any
		if err := json.Unmarshal([]byte(reviews), &reviewsArr); err != nil {
			reviewsArr = []any{}
		}
		out = append(out, map[string]any{"id": id, "request_id": reqID, "api_key_id": keyID, "decision": decision,
			"coverage": coverage, "hits": json.RawMessage(hits), "summary": summary, "policy_version": policyVer,
			"model_version": model, "duration_ms": duration, "cache_state": cache, "error_type": errType,
			"created_at": created, "reviews": reviewsArr})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// reviewAuditEvent records the review verdict (§17.2): it never replays the
// request and exceptions are exact-scoped with expiry.
func (s *Server) reviewAuditEvent(w http.ResponseWriter, r *http.Request, eventID string) {
	var req struct {
		Outcome       string  `json:"outcome"`
		Note          string  `json:"note"`
		ExceptionRule *string `json:"exception_rule_id"`
		ExceptionTTL  *int    `json:"exception_ttl_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	switch req.Outcome {
	case "confirmed_violation", "false_positive", "exception_created":
	default:
		s.writeErr(w, 400, "invalid outcome")
		return
	}
	var exception any
	var expires any
	if req.Outcome == "exception_created" {
		if req.ExceptionRule == nil || *req.ExceptionRule == "" {
			s.writeErr(w, 400, "exception_rule_id required for exceptions")
			return
		}
		ttl := 24
		if req.ExceptionTTL != nil && *req.ExceptionTTL > 0 && *req.ExceptionTTL <= 24*30 {
			ttl = *req.ExceptionTTL
		}
		var keyID string
		var contentHMAC, hitsJSON []byte
		if err := s.DB.Pool.QueryRow(r.Context(), `
			SELECT api_key_id::text, content_hmac, hits
			FROM audit_events WHERE id=$1`, eventID).Scan(&keyID, &contentHMAC, &hitsJSON); err != nil {
			s.writeErr(w, 404, "audit event not found")
			return
		}
		if len(contentHMAC) == 0 {
			s.writeErr(w, 409, "audit event predates exact exception support")
			return
		}
		var hits []audit.RuleHit
		if err := json.Unmarshal(hitsJSON, &hits); err != nil {
			s.writeErr(w, 409, "audit event hits are invalid")
			return
		}
		matched := false
		for _, hit := range hits {
			if hit.RuleID == *req.ExceptionRule {
				if hit.Category == "secret" {
					s.writeErr(w, 400, "secret rules cannot be excepted")
					return
				}
				matched = true
				break
			}
		}
		if !matched {
			s.writeErr(w, 400, "exception rule was not hit by this audit event")
			return
		}
		exception = map[string]any{
			"rule_id": *req.ExceptionRule, "api_key_id": keyID,
			"content_hmac": hex.EncodeToString(contentHMAC),
		}
		expires = time.Now().Add(time.Duration(ttl) * time.Hour)
	}
	if _, err := s.DB.Pool.Exec(r.Context(), `
		INSERT INTO audit_reviews(audit_event_id, reviewer, outcome, note, exception, expires_at)
		VALUES($1,$2,$3,$4,$5,$6)`, eventID, actorFrom(r), req.Outcome, req.Note, exception, expires); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "audit_event.review", "audit_event", eventID,
		storage.SanitizeForAdminEvent(map[string]any{"outcome": req.Outcome}), "")
	s.writeJSON(w, 200, map[string]any{"ok": true, "note": "review recorded; the original request is never replayed"})
}

// resolveUnknown applies an evidence-driven manual settlement for a request in
// unknown state (review P2-9). Pricing uses the request's FROZEN price version
// (review R2-03) — never today's table. Over-reservation keeps the account
// isolated (review R2-02): recovery requires a clean adjustment AND no other
// open unknowns AND no other hold reason.
func (s *Server) resolveUnknown(w http.ResponseWriter, r *http.Request, requestID string) {
	var req struct {
		InputTokens  *int64 `json:"input_tokens"`
		CachedTokens int64  `json:"cached_input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		Note         string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	if req.InputTokens == nil || req.OutputTokens == nil {
		s.writeErr(w, 400, "input_tokens (total) and output_tokens required")
		return
	}
	usage, err := billing.UsageFromTotal(*req.InputTokens, req.CachedTokens, *req.OutputTokens)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	var model string
	var frozenVersion *string
	if err := s.DB.Pool.QueryRow(r.Context(),
		`SELECT model, price_version_id::text FROM requests WHERE id=$1`, requestID).Scan(&model, &frozenVersion); err != nil {
		s.writeErr(w, 404, "request not found")
		return
	}
	if frozenVersion == nil || *frozenVersion == "" {
		s.writeErr(w, 409, "request has no frozen price version; record evidence via a manual price override covering the original model before adjusting")
		return
	}
	price, err := billing.ModelPriceFor(r.Context(), s.DB.Pool, *frozenVersion, model)
	if err != nil {
		// Missing historical price must fail loudly, not silently reprice at
		// today's table (review R2-03).
		s.writeErr(w, 409, "frozen price version lacks the model's price; create a manual override for the original version scope and retry: "+err.Error())
		return
	}
	result, err := billing.AdjustUnknown(r.Context(), s.DB.Pool, requestID, actorFrom(r), usage, price, *frozenVersion)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "request.resolve_unknown", "request", requestID,
		storage.SanitizeForAdminEvent(map[string]any{
			"cost": result.Cost.String(), "over_reserve": result.OverReserve,
			"price_version": frozenVersion, "note": req.Note,
		}), "")
	s.writeJSON(w, 200, map[string]any{
		"ok": true, "cost": result.Cost.String(), "over_reserve": result.OverReserve,
		"price_version_id": frozenVersion, "account_recovered": result.AccountRecovered,
	})
}

// patchBudgetPolicy enables/disables a policy (乐观锁); amounts are never
// edited in place — snapshot immutability governs periods (§18.2).
func (s *Server) patchBudgetPolicy(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Status  *string `json:"status"`
		Version int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "disabled" {
		s.writeErr(w, 400, "status must be active|disabled")
		return
	}
	tag, err := s.DB.Pool.Exec(r.Context(),
		`UPDATE budget_policies SET status=COALESCE($2,status), version=version+1, updated_at=now() WHERE id=$1 AND version=$3`,
		id, req.Status, req.Version)
	if err != nil || tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or policy not found")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "budget_policy.update", "budget_policy", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteGroup removes an empty-route-safe group; membership rows cascade.
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, id string) {
	tag, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM account_groups WHERE id=$1`, id)
	if err != nil {
		s.writeErr(w, 409, "group in use or not found: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 404, "group not found")
		return
	}
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "group.delete", "account_group", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

// groupRemoveAccount detaches one account from a group.
func (s *Server) groupRemoveAccount(w http.ResponseWriter, r *http.Request, groupID, accountID string) {
	if _, err := s.DB.Pool.Exec(r.Context(),
		`DELETE FROM account_group_members WHERE group_id=$1 AND account_id=$2`, groupID, accountID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "group.remove_account", "account_group", groupID,
		map[string]any{"account_id": accountID}, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── System status & admin events ─────────────────────────────────────────────

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Same gate as the data plane, with the concrete blocking reasons
	// (review P1-2: 生产准入与展示使用同一判断).
	ready, reasons := s.Ready.Check(ctx)
	out := map[string]any{
		"production_ready":  ready,
		"not_ready_reasons": reasons,
		"time":              time.Now().UTC().Format(time.RFC3339),
	}
	var activeRequests, unknownRequests int
	_ = s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE state NOT IN ('completed','failed_after_dispatch','rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch','unknown')`).Scan(&activeRequests)
	_ = s.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE state='unknown'`).Scan(&unknownRequests)
	out["active_requests"] = activeRequests
	out["unknown_requests"] = unknownRequests
	var accountsByState []map[string]any
	rows, err := s.DB.Pool.Query(ctx, `SELECT state, count(*) FROM accounts GROUP BY state`)
	if err == nil {
		defer rows.Close()
		accountsByState = []map[string]any{}
		for rows.Next() {
			var state string
			var n int
			_ = rows.Scan(&state, &n)
			accountsByState = append(accountsByState, map[string]any{"state": state, "count": n})
		}
	}
	out["accounts"] = accountsByState
	s.writeJSON(w, 200, out)
}

func (s *Server) listAdminEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT id::text, actor, action, target_type, target_id, changes::text, created_at::text
		FROM admin_events ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, actor, action, targetType, targetID, changes, created string
		if err := rows.Scan(&id, &actor, &action, &targetType, &targetID, &changes, &created); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "actor": actor, "action": action, "target_type": targetType,
			"target_id": targetID, "changes": json.RawMessage(changes), "created_at": created})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// ── OAuth sessions ───────────────────────────────────────────────────────────

func (s *Server) startOAuthSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReuseAccountID string `json:"reuse_account_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var accountID *string
	if req.ReuseAccountID != "" {
		if !resourceUUID.MatchString(req.ReuseAccountID) {
			s.writeErr(w, 400, "invalid reuse_account_id")
			return
		}
		accountID = &req.ReuseAccountID
	}
	id, state, authorizeURL, _, err := s.OAuth.StartSession(r.Context(), accountID)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "oauth.session_start", "oauth_session", id, nil, "")
	s.writeJSON(w, 201, map[string]any{"id": id, "state": state, "authorize_url": authorizeURL})
}

func (s *Server) getOAuthSession(w http.ResponseWriter, r *http.Request, id string) {
	var status, accountID string
	err := s.DB.Pool.QueryRow(r.Context(), `
		SELECT status, COALESCE(account_id::text,'') FROM oauth_sessions WHERE id=$1`, id).
		Scan(&status, &accountID)
	if err != nil {
		s.writeErr(w, 404, "session not found")
		return
	}
	s.writeJSON(w, 200, map[string]any{"id": id, "status": status, "account_id": accountID})
}

// oauthCallback is the public redirect target: state must match a pending
// session; single use; then it completes and records the account.
func (s *Server) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		s.writeErr(w, 400, "state and code required")
		return
	}
	accountID, err := s.OAuth.CompleteCallback(r.Context(), state, code)
	if err != nil {
		s.writeErr(w, 400, "oauth callback rejected: "+err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]any{"ok": true, "account_id": accountID})
}

var _ = strings.TrimSpace
