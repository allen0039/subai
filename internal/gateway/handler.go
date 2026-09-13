package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"subai/internal/audit"
	"subai/internal/auth"
	"subai/internal/billing"
	"subai/internal/config"
	"subai/internal/egress"
	"subai/internal/scheduler"
	"subai/internal/storage"
)

// Server wires the data plane together.
type Server struct {
	Cfg      *config.Config
	DB       *storage.DB
	Auth     *auth.Service
	State    *StateTracker
	Pipeline *audit.Pipeline
	Sched    *scheduler.Scheduler
	Egress   *egress.Resolver
	Upstream *Upstream
	Slots    *scheduler.Slots
	Sticky   *StickyStore
	Ready    *Readiness          // unified production gate (review P1-2)
	Refresh  CredentialRefresher // optional token refresh hook (review P1-6)
	BodyMem  *BodyMemoryGate     // in-flight audit body budget (review P2-12)
}

// CredentialRefresher refreshes an account's OAuth tokens before expiry.
type CredentialRefresher interface {
	Refresh(ctx context.Context, accountID string) error
}

// RequestIDHint supplies a fresh request id for pre-auth error responses.
func (s *Server) RequestIDHint() string { return NewRequestID() }

// ── Sticky store (§19) ───────────────────────────────────────────────────────

type StickyStore struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]stickyEntry
}

type stickyEntry struct {
	accountID string
	at        time.Time
}

func NewStickyStore(ttl time.Duration) *StickyStore {
	return &StickyStore{ttl: ttl, m: map[string]stickyEntry{}}
}

func (s *StickyStore) Get(key, session string) string {
	if session == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key+"|"+session]
	if !ok || time.Since(e.at) > s.ttl {
		delete(s.m, key+"|"+session)
		return ""
	}
	return e.accountID
}

func (s *StickyStore) Put(key, session, accountID string) {
	if session == "" || accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Bounded memory: opportunistically drop expired entries on growth.
	if len(s.m) > 10_000 {
		now := time.Now()
		for k, v := range s.m {
			if now.Sub(v.at) > s.ttl {
				delete(s.m, k)
			}
		}
		if len(s.m) > 10_000 {
			s.m = map[string]stickyEntry{}
		}
	}
	s.m[key+"|"+session] = stickyEntry{accountID: accountID, at: time.Now()}
}

// ── Body memory gate (review P2-12) ─────────────────────────────────────────

// BodyMemoryGate bounds the total bytes of request bodies held in memory
// between admission and audit completion (§22 audit_body_memory_limit).
type BodyMemoryGate struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

func NewBodyMemoryGate(limit int64) *BodyMemoryGate { return &BodyMemoryGate{limit: limit} }

// TryReserve admits n bytes; false means the budget is exhausted.
func (b *BodyMemoryGate) TryReserve(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

func (b *BodyMemoryGate) Release(n int64) {
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

func (b *BodyMemoryGate) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// ── /v1/models ───────────────────────────────────────────────────────────────

func (s *Server) Models(w http.ResponseWriter, r *http.Request) {
	rid := NewRequestID()
	info, err := s.Auth.LookupKey(r.Context(), auth.Bearer(r))
	if err != nil {
		errInvalidKey(rid).write(w)
		return
	}
	if ok, _ := s.Ready.Check(r.Context()); !ok {
		s.Ready.NotReadyError(rid).write(w)
		return
	}
	version, err := billing.ActivePriceVersion(r.Context(), s.DB.Pool)
	if err != nil {
		s.Ready.NotReadyError(rid).write(w)
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT model FROM model_prices WHERE price_version_id=$1 ORDER BY model`, version)
	if err != nil {
		errUpstream(rid, "model list unavailable").write(w)
		return
	}
	defer rows.Close()
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	var out []model
	now := time.Now().Unix()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			continue
		}
		if info.AllowedModels != nil && !contains(info.AllowedModels, m) {
			continue
		}
		out = append(out, model{ID: m, Object: "model", Created: now, OwnedBy: "subai"})
	}
	if out == nil {
		out = []model{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": out})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ── /v1/responses lifecycle ──────────────────────────────────────────────────

// Responses implements POST /v1/responses with the full pipeline lifecycle.
// Everything after `dispatching` runs on a detached, timeout-bounded context
// so a client disconnect can never abort settlement or unknown-marking
// (review P1-3).
func (s *Server) Responses(w http.ResponseWriter, r *http.Request) {
	rid := NewRequestID()
	ctx := r.Context()

	// 1. Key auth first (§17.1), then the unified readiness gate (review P1-2).
	info, err := s.Auth.LookupKey(ctx, auth.Bearer(r))
	if err != nil {
		errInvalidKey(rid).write(w)
		return
	}
	if ok, _ := s.Ready.Check(ctx); !ok {
		s.Ready.NotReadyError(rid).write(w)
		return
	}

	// 2. Size-limited, budget-accounted body read (FORMAT-001 §20 + global
	// in-flight body budget reserved DURING the read, reviews P2-12/R2-09).
	body, err := readBodyWithBudget(r, s.Cfg.RequestBodyLimit, s.BodyMem)
	if err != nil {
		var memFull errMemoryFull
		if errors.As(err, &memFull) {
			errQueueFull(rid, 5).write(w)
		} else {
			errBodyTooLarge(rid).write(w)
		}
		return
	}
	defer s.BodyMem.Release(int64(len(body)))

	// 3. Route gate: no route ⇒ deny (§16.2).
	routes, err := s.Sched.RoutesFor(ctx, info.ID)
	if err != nil || len(routes) == 0 {
		errAccessDenied(rid, "key has no route to any account or group").write(w)
		return
	}

	hmacSum := sha256.Sum256(body)

	// Request row is created before validation so every rejection is auditable.
	if err := s.State.Create(ctx, rid, info.ID, r.Header.Get("X-Client-Request-Id"), hmacSum[:], ""); err != nil {
		logStorageErr("create_request", err)
		errUpstream(rid, "storage unavailable").write(w)
		return
	}
	if err := s.State.Transition(ctx, rid, "validated", ""); err != nil {
		logStorageErr("transition_validated", err)
		errUpstream(rid, "storage unavailable").write(w)
		return
	}

	// 4. Structural validation + audit document extraction (§21).
	doc, err := audit.ExtractResponses(body, true)
	if err != nil {
		code := "audit_unsupported"
		if errors.Is(err, audit.ErrUnsupportedInput) && strings.Contains(err.Error(), "invalid JSON") {
			code = "invalid_request"
		}
		_ = s.State.Transition(ctx, rid, "rejected", code)
		errAuditUnsupported(rid, err.Error()).write(w)
		return
	}
	model := extractModel(body)
	if model == "" {
		_ = s.State.Transition(ctx, rid, "rejected", "missing_model")
		errAuditUnsupported(rid, "model is required").write(w)
		return
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE requests SET model=$2 WHERE id=$1`, rid, model); err != nil {
		logStorageErr("set_model", err)
		errUpstream(rid, "storage unavailable").write(w)
		return
	}

	// 5. Model check against the key allow-list (re-checked again after audit).
	if !modelAllowed(info, model) {
		_ = s.State.Transition(ctx, rid, "rejected", "model_not_allowed")
		errAccessDenied(rid, "model not allowed for this key").write(w)
		return
	}
	priceVersion, err := billing.ActivePriceVersion(ctx, s.DB.Pool)
	if err != nil {
		_ = s.State.Transition(ctx, rid, "rejected", "no_price_version")
		s.Ready.NotReadyError(rid).write(w)
		return
	}
	price, err := billing.ModelPriceFor(ctx, s.DB.Pool, priceVersion, model)
	if err != nil {
		_ = s.State.Transition(ctx, rid, "rejected", "unknown_model_price")
		errAuditUnsupported(rid, "model has no price mapping; strict budget refuses the call").write(w)
		return
	}

	// 6. Audit pipeline (§3: always before upstream selection & dispatch).
	if err := s.State.Transition(ctx, rid, "audit_queued", ""); err != nil {
		logStorageErr("transition_audit_queued", err)
		errUpstream(rid, "storage unavailable").write(w)
		return
	}
	result := s.Pipeline.Run(ctx, doc, info.ID)
	if err := s.recordAuditEvent(ctx, rid, info.ID, result); err != nil {
		logStorageErr("record_audit_event", err)
		// §9: loggable requests must stop before upstream when audit logging fails.
		_ = s.State.Transition(ctx, rid, "failed_before_dispatch", "audit_log_write_failed")
		errAuditUnavailable(rid).write(w)
		return
	}
	if result.Coverage == audit.CoverageFull {
		_ = s.State.Transition(ctx, rid, "local_checked", "")
	}

	switch result.Decision {
	case audit.DecisionBlock:
		_ = s.State.Transition(ctx, rid, "audit_failed", "audit_blocked")
		errAuditBlocked(rid, "request blocked by moderation policy").write(w)
		return
	case audit.DecisionReview:
		_ = s.State.Transition(ctx, rid, "audit_failed", "audit_review")
		msg := "request paused pending human review"
		if len(result.Hits) > 0 {
			msg = result.Hits[0].Message
		}
		errAuditBlocked(rid, msg).write(w)
		return
	case audit.DecisionUnsupported:
		_ = s.State.Transition(ctx, rid, "audit_failed", "audit_unsupported")
		errAuditUnsupported(rid, "moderation cannot process this input: "+result.ErrorType).write(w)
		return
	case audit.DecisionUnavailable:
		_ = s.State.Transition(ctx, rid, "audit_failed", "audit_unavailable")
		code := errAuditUnavailable(rid)
		if result.ErrorType == "audit_queue_full" || result.ErrorType == "audit_queue_timeout" {
			code = errQueueFull(rid, 5)
		}
		code.write(w)
		return
	}
	_ = s.State.Transition(ctx, rid, "audit_passed", "")

	// 7. Fresh key re-check after auditing (§3 + review P2-10): a NEW DB
	// snapshot, with full revalidation of model allow-list and concurrency
	// limit — the earlier read may be stale by the audit wait.
	info, err = s.Auth.LookupKeyFresh(ctx, auth.Bearer(r))
	if err != nil {
		_ = s.State.Transition(ctx, rid, "failed_before_dispatch", "key_changed_during_audit")
		errInvalidKey(rid).write(w)
		return
	}
	if !modelAllowed(info, model) {
		_ = s.State.Transition(ctx, rid, "failed_before_dispatch", "model_not_allowed")
		errAccessDenied(rid, "model not allowed for this key (permission changed during audit)").write(w)
		return
	}

	// 8. Account selection + two-layer concurrency acquire as one step
	// (§1, §19 + review P2-11): capacity-aware pick closes the pick/race window.
	stickySession := r.Header.Get("X-Session-Id")
	stickyID := s.Sticky.Get(info.ID, stickySession)
	acct, releaseSlots, err := s.Sched.AcquireAccount(ctx, info.ID, info.ConcurrencyLimit, stickyID)
	if err != nil {
		_ = s.State.Transition(ctx, rid, "failed_before_dispatch", "no_account")
		switch {
		case errors.Is(err, scheduler.ErrNoRoute):
			errAccessDenied(rid, "no route configured").write(w)
		case errors.Is(err, scheduler.ErrConcurrency):
			errConcurrency(rid, 3).write(w)
		default:
			errNoHealthyAccount(rid).write(w)
		}
		return
	}
	defer releaseSlots()

	groupID := acct.GroupID
	if err := s.State.SetRouting(ctx, rid, acct.ID, groupID, result.Coverage, priceVersion); err != nil {
		logStorageErr("set_routing", err)
		errUpstream(rid, "storage unavailable").write(w)
		return
	}

	// 9. Budget reservation before any upstream byte (§18.3).
	candidate := estimateReservation(body, price, s.Cfg)
	if _, err := billing.Reserve(ctx, s.DB.Pool, info.MemberID, info.ID, acct.ID, groupID, rid, candidate, time.Now()); err != nil {
		_ = s.State.Transition(ctx, rid, "failed_before_dispatch", "budget_exceeded")
		errBudget(rid, nextPeriodResetHint()).write(w)
		return
	}
	_ = s.State.Transition(ctx, rid, "reserved", "")

	// 10. Dispatch through the account's egress policy (§8).
	if err := s.State.Transition(ctx, rid, "dispatching", ""); err != nil {
		// Funds are already reserved: never leave `reserved` dangling —
		// release or converge to a recoverable path (review R2-05).
		logStorageErr("transition_dispatching", err)
		fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if relErr := billing.Release(fallbackCtx, s.DB.Pool, rid); relErr != nil {
			logStorageErr("release_after_transition_failure", relErr)
		}
		_ = s.State.Transition(fallbackCtx, rid, "failed_before_dispatch", "dispatch_transition_failed")
		errUpstream(rid, "storage unavailable").write(w)
		return
	}

	// Two SEPARATE detached scopes (review R2-05):
	//   - upstreamCtx runs the dispatch itself under the configured timeout;
	//   - the finalization scope is created only when the dispatch has FINISHED,
	//     so an expired upstream budget can never starve MarkUnknown/Settle.
	upstreamCtx, cancelUpstream := context.WithTimeout(context.WithoutCancel(ctx), s.Cfg.UpstreamTimeout)
	defer cancelUpstream()

	payload := s.prepareUpstreamPayload(body)
	creds, err := s.accountCredentials(upstreamCtx, acct.ID)
	if err != nil {
		logStorageErr("credentials", err)
		// Never dispatched: release funds, terminal state, explicit error.
		finishCtx, cancel := s.finalizeContext()
		defer cancel()
		if relErr := billing.Release(finishCtx, s.DB.Pool, rid); relErr != nil {
			logStorageErr("release_after_credential_error", relErr)
		}
		_ = s.State.Transition(finishCtx, rid, "failed_before_dispatch", "credential_error")
		errNoHealthyAccount(rid).write(w)
		return
	}

	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if canFlush {
		flusher.Flush()
	}

	var usageOut Usage
	usageKnown := false
	streamedAny := false
	terminalEvent := ""

	streamErr := s.dispatchViaEgress(upstreamCtx, acct, creds, payload, func(frame SSEFrame, raw []byte) error {
		if !streamedAny {
			streamedAny = true
			_ = s.State.Transition(upstreamCtx, rid, "streaming", "")
			s.Sticky.Put(info.ID, stickySession, acct.ID)
		}
		if u, ok := ExtractUsageFromEvent(frame); ok {
			usageOut = u
			usageKnown = true
		}
		if frame.Event == "response.failed" || (terminalEvent == "" && frame.Event == "response.completed") {
			terminalEvent = frame.Event
		}
		if canFlush {
			_, _ = w.Write(raw)
			flusher.Flush()
		}
		return nil
	})

	// The upstream call is over; finalization gets its OWN fresh deadline
	// (review R2-05) and is disconnect-safe (review P1-3).
	finishCtx, cancelFinish := s.finalizeContext()
	defer cancelFinish()

	// Unified failure convergence: ANY dispatch failure — stream break,
	// pre-response break, unconfirmable outcome — lands in unknown with
	// funds retained and the account held (§19, reviews P1-3/P1-5/R2-05).
	if streamErr != nil {
		logStorageErr("upstream_stream", streamErr)
		s.convergeUnknown(finishCtx, rid, acct.ID, streamedAny, "stream_interrupted", "upstream_error_before_stream")
		if !streamedAny && canFlush {
			_, _ = w.Write([]byte(SSEErrorEvent(errUpstream(rid, "upstream request failed"))))
		}
		return
	}

	// 11. Settlement (§18.3) — idempotent, exact accounting, disconnect-safe.
	// P1-02: Persist terminal outcome BEFORE settlement so recovery can distinguish
	// completed vs failed after dispatch.
	_ = s.State.Transition(finishCtx, rid, "settling", "")
	if !usageKnown {
		// Missing usage is never treated as zero (§23 禁止条款).
		s.convergeUnknown(finishCtx, rid, acct.ID, streamedAny, "usage_missing", "usage_missing")
		return
	}
	terminalOutcome := "completed"
	if terminalEvent == "response.failed" {
		terminalOutcome = "failed"
	}
	if err := s.State.SetTerminalOutcome(finishCtx, rid, terminalOutcome); err != nil {
		logStorageErr("set_terminal_outcome", err)
		s.convergeUnknown(finishCtx, rid, acct.ID, streamedAny, "outcome_persist_failed", "outcome_persist_failed")
		return
	}
	if _, err := billing.Settle(finishCtx, s.DB.Pool, rid, 0, info.ID, acct.ID, billing.Usage{
		InputTokens:  usageOut.InputTokens,
		CachedTokens: usageOut.CachedTokens,
		OutputTokens: usageOut.OutputTokens,
	}, price, priceVersion); err != nil {
		logStorageErr("settle", err)
		// Settle failed: funds stay unknown AND the account must stop taking
		// new work until reconciliation (review R2-05: 收敛函数统一处理).
		s.convergeUnknown(finishCtx, rid, acct.ID, streamedAny, "settle_failed", "settle_failed")
		return
	}
	state, reason := "completed", "settled"
	if terminalOutcome == "failed" {
		state, reason = "failed_after_dispatch", "upstream_response_failed"
	}
	if err := s.State.Transition(finishCtx, rid, state, reason); err != nil {
		// Ledger is already committed; crash before this point is reconciled
		// by startup recovery via the ledger check (review P1-4).
		logStorageErr("transition_completed", err)
	}
}

// finalizeContext returns a fresh detached, timeout-bounded scope for
// post-dispatch database work.
func (s *Server) finalizeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(context.Background()), 2*time.Minute)
}

// convergeUnknown is the single failure sink after dispatch: mark funds
// unknown, hold the account, and record the terminal-branch state.
func (s *Server) convergeUnknown(ctx context.Context, rid, accountID string, streamedAny bool, streamedReason, preStreamReason string) {
	// Review R3-01: pass accountID to establish hold reason.
	if mkErr := billing.MarkUnknownWithAccount(ctx, s.DB.Pool, rid, accountID); mkErr != nil {
		logStorageErr("mark_unknown", mkErr)
	}
	if stErr := s.State.RetainUnknown(ctx, rid, accountID); stErr != nil {
		logStorageErr("retain_unknown", stErr)
	}
	if streamedAny {
		_ = s.State.Transition(ctx, rid, "unknown", streamedReason)
	} else {
		_ = s.State.Transition(ctx, rid, "unknown", preStreamReason)
	}
}

// dispatchViaEgress runs the upstream request through the account's egress
// policy. Failover to fallback egresses happens ONLY while nothing has been
// sent downstream and the failure is transport-level; once frames flowed the
// request is unconfirmable and must not be replayed (review P1-5).
func (s *Server) dispatchViaEgress(ctx context.Context, acct *scheduler.Account, creds Credentials, payload []byte, onFrame func(SSEFrame, []byte) error) error {
	// Transport budget mirrors the upstream scope (review R2-05: 可注入超时).
	timeout := s.Cfg.UpstreamTimeout
	sent := false
	call := func(client *http.Client, prof egress.Profile) error {
		resp, err := s.Upstream.Dispatch(ctx, client, prof, creds, payload)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			// Upstream received and answered the POST: replaying on another
			// egress could double-execute. Surface as unconfirmable.
			return &upstreamStatusError{status: resp.StatusCode}
		}
		return scanSSE(resp.Body, func(f SSEFrame, raw []byte) error {
			sent = true
			return onFrame(f, raw)
		})
	}
	var policy *egress.Policy
	if acct.EgressPolicyID != nil {
		p, err := s.Egress.GetPolicy(ctx, *acct.EgressPolicyID)
		if err != nil {
			return err
		}
		policy = p
	} else {
		policy = &egress.Policy{Name: "direct-default", Primary: egress.Profile{ID: "direct", Name: "direct", Kind: "direct", Status: "active"}, FailureMode: "stop"}
	}
	_, derr := policy.Dispatch(ctx, timeout, &sent, call)
	return derr
}

type upstreamStatusError struct{ status int }

func (u *upstreamStatusError) Error() string   { return "upstream status " + itoa(u.status) }
func (u *upstreamStatusError) HTTPStatus() int { return u.status }

func (s *Server) accountCredentials(ctx context.Context, accountID string) (Credentials, error) {
	creds, err := s.loadCredentials(ctx, accountID)
	if err != nil {
		return Credentials{}, err
	}
	// Proactive refresh (review P1-6): never send a known-expired token
	// upstream; Refresh holds the per-account mutex and is version-guarded.
	if creds.ExpiresAt != nil && creds.ExpiresAt.Before(time.Now().Add(60*time.Second)) {
		if s.Refresh != nil {
			if rerr := s.Refresh.Refresh(ctx, accountID); rerr != nil {
				logStorageErr("token_refresh", rerr)
				reloaded, lerr := s.loadCredentials(ctx, accountID)
				if lerr != nil {
					return Credentials{}, lerr
				}
				if reloaded.ExpiresAt != nil && reloaded.ExpiresAt.Before(time.Now()) {
					return Credentials{}, errors.New("account token expired and refresh failed")
				}
				return reloaded, nil
			}
			return s.loadCredentials(ctx, accountID)
		}
		if creds.ExpiresAt.Before(time.Now()) {
			return Credentials{}, errors.New("account token expired (no refresher configured)")
		}
	}
	return creds, nil
}

func (s *Server) loadCredentials(ctx context.Context, accountID string) (Credentials, error) {
	var sealed []byte
	var state string
	if err := s.DB.Pool.QueryRow(ctx,
		`SELECT credentials_ciphertext, state FROM accounts WHERE id=$1
		AND NOT EXISTS(SELECT 1 FROM account_holds h WHERE h.account_id=accounts.id)`, accountID).Scan(&sealed, &state); err != nil {
		return Credentials{}, err
	}
	if state != "active" {
		return Credentials{}, errors.New("account not active")
	}
	plain, err := s.DB.Decrypt(sealed)
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(plain, &c); err != nil {
		return Credentials{}, err
	}
	if c.AccessToken == "" {
		return Credentials{}, errors.New("account has no access token")
	}
	return c, nil
}

// prepareUpstreamPayload injects the output bound (D-001).
func (s *Server) prepareUpstreamPayload(body []byte) []byte {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	bound := s.Cfg.OutputBound
	if raw, ok := payload["max_output_tokens"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil && v > 0 && v < bound {
			bound = v
		}
	}
	payload["max_output_tokens"], _ = json.Marshal(bound)
	delete(payload, "previous_response_id") // unsupported coverage is rejected earlier; belt & braces
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

func modelAllowed(info *auth.KeyInfo, model string) bool {
	return info.AllowedModels == nil || contains(info.AllowedModels, model)
}
