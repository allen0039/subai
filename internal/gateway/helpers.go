package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"subai/internal/audit"
	"subai/internal/billing"
	"subai/internal/config"
)

// readLimited enforces the per-request body cap during the read itself
// (FORMAT-001, §20). Used by callers without the global gate.
func readLimited(r *http.Request, limit int64) ([]byte, error) {
	limited := io.LimitReader(r.Body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge{}
	}
	return data, nil
}

// errMemoryFull marks rejection by the global in-flight body budget (§22).
type errMemoryFull struct{}

func (errMemoryFull) Error() string { return "in-flight body memory budget exhausted" }

const bodyChunkSize = 64 << 10

// readBodyWithBudget reads the request body in chunks, reserving global
// budget BEFORE each chunk is buffered (review R2-09: 准入发生在分配之前).
// Slow concurrent uploads occupy accounted budget while reading, and
// over-budget requests stop reading early with all accounted bytes released.
// The per-request cap still applies.
func readBodyWithBudget(r *http.Request, limit int64, gate *BodyMemoryGate) ([]byte, error) {
	if gate == nil {
		return readLimited(r, limit)
	}
	var out []byte
	var accounted int64
	buf := make([]byte, bodyChunkSize)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			if !gate.TryReserve(int64(n)) {
				gate.Release(accounted)
				return nil, errMemoryFull{}
			}
			accounted += int64(n)
			out = append(out, buf[:n]...)
			if int64(len(out)) > limit {
				gate.Release(accounted)
				return nil, errTooLarge{}
			}
		}
		if err != nil {
			if err == io.EOF {
				// read complete; the caller owns the accounted bytes until
				// audit finishes (released via the handler's defer)
				return out, nil
			}
			gate.Release(accounted)
			return nil, err
		}
	}
}

type errTooLarge struct{}

func (errTooLarge) Error() string { return "body too large" }

func extractModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	return req.Model
}

// estimateReservation returns the provable upper bound of the request cost
// (D-001/D-002): conservative input token estimate priced at the frozen
// version plus the injected output bound, rounded up (§18.1).
func estimateReservation(body []byte, price *billing.ModelPrice, cfg *config.Config) decimal.Decimal {
	if price == nil {
		return decimal.Zero
	}
	inputTokens := billing.EstimateTokens(body)
	outputTokens := int64(cfg.OutputBound)
	cost := price.Cost(billing.Usage{InputTokens: inputTokens, OutputTokens: outputTokens})
	return billing.RoundUpReservation(cost)
}

// nextPeriodResetHint gives the client a reset_at hint for budget errors
// (§17.1). It is an approximation in the default policy timezone (Monday
// 00:00 weeks) — never presented as an upstream quota window (§18.2).
func nextPeriodResetHint() string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.UTC
	}
	local := time.Now().In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	weekday := int(start.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return start.AddDate(0, 0, 8-weekday).Format(time.RFC3339)
}

// recordAuditEvent persists the audit outcome (§9). The error is surfaced so
// auditable requests stop before any upstream dispatch when logging fails.
func (s *Server) recordAuditEvent(ctx context.Context, rid, keyID string, result *audit.Result) error {
	hits, _ := json.Marshal(result.Hits)
	cats, _ := json.Marshal(result.Categories)
	_, err := s.DB.Pool.Exec(ctx, `
		INSERT INTO audit_events(request_id, api_key_id, decision, coverage, hits, categories,
			summary, policy_version, model_version, duration_ms, cache_state, error_type, content_hmac)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		rid, keyID, string(result.Decision), result.Coverage, hits, cats, result.Summary,
		result.PolicyVersion, result.ModelVersion, result.DurationMS, result.CacheState, result.ErrorType, result.ContentHMAC)
	return err
}

// AuditExceptionLookup gives the audit pipeline a narrowly scoped exemption
// lookup. The review record stores an HMAC rather than the original content.
func AuditExceptionLookup(pool *pgxpool.Pool) audit.ExceptionLookup {
	return func(ctx context.Context, keyID string, contentHMAC []byte, ruleID string) (bool, error) {
		var exempt bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM audit_reviews
				WHERE outcome='exception_created' AND expires_at > now()
				  AND exception->>'rule_id'=$1
				  AND exception->>'api_key_id'=$2
				  AND exception->>'content_hmac'=$3
			)`, ruleID, keyID, hex.EncodeToString(contentHMAC)).Scan(&exempt)
		return exempt, err
	}
}

var _ = pgxpool.New

// logStorageErr surfaces persisted-layer failures; silent 502s would hide
// regressions from operators and tests alike.
func logStorageErr(where string, err error) {
	if err != nil {
		log.Printf("gateway storage error at %s: %v", where, err)
	}
}

// (log import)
