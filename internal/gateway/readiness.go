package gateway

import (
	"context"
	"sync"
	"time"

	"subai/internal/config"
	"subai/internal/storage"
)

// Readiness is the single production gate used by BOTH the data plane and the
// admin status page (review P1-2: 生产准入与展示使用同一判断). A request is
// only dispatched when every condition holds; the reasons are surfaced
// verbatim so operators see the actual missing pieces (§22).
type Readiness struct {
	cfg *config.Config
	db  *storage.DB
	ttl time.Duration

	mu       sync.Mutex
	cachedAt time.Time
	cachedOK bool
	cached   []string
}

func NewReadiness(cfg *config.Config, db *storage.DB) *Readiness {
	return &Readiness{cfg: cfg, db: db, ttl: 3 * time.Second}
}

func (r *Readiness) Check(ctx context.Context) (ok bool, reasons []string) {
	r.mu.Lock()
	if time.Since(r.cachedAt) < r.ttl {
		ok, reasons := r.cachedOK, r.cached
		r.mu.Unlock()
		return ok, reasons
	}
	r.mu.Unlock()

	var rs []string
	if r.cfg.ModerationAPIKey == "" {
		rs = append(rs, "moderation platform key not configured (SUBAI_MODERATION_API_KEY)")
	}
	// Price admission (review P1-2): synthetic seed prices must never reach
	// the data path in production mode.
	var origin string
	err := r.db.Pool.QueryRow(ctx,
		`SELECT origin FROM price_versions WHERE status='active' ORDER BY activated_at DESC NULLS LAST LIMIT 1`).Scan(&origin)
	if err != nil {
		rs = append(rs, "no active price version")
	} else if origin == "synthetic" && !r.cfg.AllowSyntheticPrices {
		rs = append(rs, "active price version is synthetic (unverified); configure a verified official/manual version or set SUBAI_ALLOW_SYNTHETIC_PRICES=1 for explicit test mode")
	}
	if !r.cfg.ProductionReady {
		rs = append(rs, "operator switch off (set SUBAI_PRODUCTION_READY=1 after P0 verification)")
	}

	r.mu.Lock()
	r.cachedOK = len(rs) == 0
	r.cached = rs
	r.cachedAt = time.Now()
	r.mu.Unlock()
	return len(rs) == 0, rs
}

// NotReadyError renders the unified 503 with the concrete reasons.
func (r *Readiness) NotReadyError(rid string) *APIError {
	_, reasons := r.Check(context.Background())
	msg := "gateway not ready: "
	for i, reason := range reasons {
		if i > 0 {
			msg += "; "
		}
		msg += reason
	}
	return &APIError{HTTPStatus: 503, Code: "audit_unavailable", Message: msg, RequestID: rid, Retryable: true}
}
