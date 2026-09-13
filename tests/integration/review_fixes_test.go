package integration

// Regression tests for the 2026-09-12 independent review (P1/P2 fixes).
// Each test maps to a numbered finding in docs/INDEPENDENT_REVIEW_2026-09-12.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"subai/internal/accounts"
	"subai/internal/billing"
	"subai/internal/config"
	"subai/internal/gateway"
	"subai/internal/server"
	"subai/internal/storage"
)

// newTestEnvWithCfg is newTestEnv with a config mutation hook (P1-2 variants).
func newTestEnvWithCfg(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	if mutate != nil {
		mutate(e.gw.Cfg)
	}
	return e
}

// Review P1-8: a 200 response whose result lacks the flagged field must be
// unavailable — never allow — and the upstream must see zero calls.
func TestModerationMissingFlaggedField(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.moder.badBody = `{"id":"m","model":"omni-moderation-latest","results":[{}]}`
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 503 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "audit_unavailable") {
		t.Fatalf("want audit_unavailable, got %s", rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream saw %d calls, want 0", e.up.count())
	}
}

// Review P1-2: synthetic prices + production mode ⇒ data plane refuses with
// the concrete reason and the upstream sees zero calls.
func TestUnverifiedPriceZeroUpstream(t *testing.T) {
	requireDB(t)
	e := newTestEnvWithCfg(t, func(c *config.Config) { c.AllowSyntheticPrices = false })
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 503 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "synthetic") {
		t.Fatalf("reason must name the synthetic price, got %s", rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream saw %d calls, want 0", e.up.count())
	}
}

// Review P1-3 (round-2 hardening): cancelling the client request mid-stream
// must not abort settlement. The upstream SIGNALS after its first frame so
// the cancel is deterministically mid-stream (no fixed sleeps).
func TestClientDisconnectStillSettles(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	firstFrame := make(chan struct{})
	upMux := http.NewServeMux()
	upMux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"response\":{\"id\":\"r\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(firstFrame) // deterministic sync point
		// The gateway reads on a detached scope, so this frame still arrives
		// after the client disappeared.
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "event: response.completed\ndata: {\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":30,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	})
	upSrv := httptest.NewServer(upMux)
	defer upSrv.Close()
	e.gw.Upstream.BaseURL = upSrv.URL
	e.gw.Cfg.UpstreamBaseURL = upSrv.URL

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body(true, "hi")))
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+e.keyFull)
	rec := httptest.NewRecorder()
	go func() {
		<-firstFrame
		cancel() // client disconnect exactly between the frames
	}()
	e.gw.Responses(rec, req)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := e.db.Pool.QueryRow(context.Background(), `SELECT state FROM requests`).Scan(&state); err == nil && state == "completed" {
			var charges int
			if err := e.db.Pool.QueryRow(context.Background(),
				`SELECT count(*) FROM usage_ledger WHERE entry_type='charge'`).Scan(&charges); err == nil && charges == 1 {
				return // PASS: settlement survived the disconnect
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("request did not settle after client disconnect (state/ledger check timed out)")
}

// Review P1-5: a stream that delivered its first frame then broke must NOT be
// replayed on a fallback egress — upstream sees exactly one request, and the
// request converges to unknown.
func TestFallbackNoReplayAfterFirstFrame(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	// Upstream: first request delivers one frame, then the connection breaks.
	var upstreamHits int32
	upMux := http.NewServeMux()
	upMux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&upstreamHits, 1)
		if n > 1 {
			return // a replay would show up here
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("no hijacker")
			return
		}
		conn, buf, _ := hj.Hijack()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		buf.WriteString("event: response.created\ndata: {\"response\":{\"id\":\"r\"}}\n\n")
		buf.Flush()
		conn.Close() // break mid-stream after the first frame
	})
	upSrv := httptest.NewServer(upMux)
	defer upSrv.Close()
	e.gw.Upstream.BaseURL = upSrv.URL
	e.gw.Cfg.UpstreamBaseURL = upSrv.URL

	// Egress policy: primary direct + fallback direct, failure_mode=fallback.
	var directProfile, policyID string
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO proxy_profiles(name, kind) VALUES('direct-1','direct') RETURNING id::text`).Scan(&directProfile); err != nil {
		t.Fatal(err)
	}
	var fbProfile string
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO proxy_profiles(name, kind) VALUES('direct-fb','direct') RETURNING id::text`).Scan(&fbProfile); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO egress_policies(name, primary_proxy_id, failure_mode, ordered_fallback_proxy_ids)
		 VALUES('fb',$1,'fallback',$2) RETURNING id::text`,
		directProfile, []string{fbProfile}).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE accounts SET egress_policy_id=$1`, policyID); err != nil {
		t.Fatal(err)
	}

	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	_ = rec
	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Fatalf("upstream received %d requests, want exactly 1 (no fallback replay)", got)
	}
	var state string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT state FROM requests`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("state = %s, want unknown (unconfirmable stream)", state)
	}
	var held int
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM reservations WHERE state='unknown'`).Scan(&held); err != nil || held == 0 {
		t.Fatalf("unknown reservations = %d err=%v, want >0 retained", held, err)
	}
}

// Review P1-4: startup recovery must reconcile funds and accounts, not only
// request states.
func TestStartupRecoveryReleasesFundsAndHoldsAccount(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var keyID, accountID, periodID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM accounts`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	// budget periods are created lazily on first reserve; seed one explicitly.
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO budget_periods(policy_id, period_start, period_end, timezone, limit_snapshot)
		SELECT id, CURRENT_DATE, CURRENT_DATE + 7, 'UTC', 100 FROM budget_policies WHERE owner_type='account' LIMIT 1
		RETURNING id::text`).Scan(&periodID); err != nil {
		t.Fatal(err)
	}

	// Pre-dispatch request WITH a held reservation.
	preID := "req_rec_predispatch"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state) VALUES($1,$2,'gpt-5-codex','reserved')`, preID, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES($1,$2,0.5,'held')`, preID, periodID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE budget_periods SET reserved = reserved + 0.5 WHERE id=$1`, periodID); err != nil {
		t.Fatal(err)
	}

	// Post-dispatch request WITHOUT ledger charge, bound to the account.
	postID := "req_rec_postdispatch"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state, account_id) VALUES($1,$2,'gpt-5-codex','streaming',$3)`,
		postID, keyID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES($1,$2,0.25,'held')`, postID, periodID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE budget_periods SET reserved = reserved + 0.25 WHERE id=$1`, periodID); err != nil {
		t.Fatal(err)
	}

	st := gateway.NewStateTracker(e.db.Pool)
	summary, err := st.OnStartupRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cancelled != 1 || summary.Unknown != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	var resState string
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT state FROM reservations WHERE request_id=$1`, preID).Scan(&resState); err != nil {
		t.Fatal(err)
	}
	if resState != "released" {
		t.Fatalf("pre-dispatch reservation = %s, want released (review P1-4)", resState)
	}
	var reserved string
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT reserved::text FROM budget_periods WHERE id=$1`, periodID).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != "0.250000000000" {
		t.Fatalf("budget reserved = %s, want 0.25 (post-dispatch funds retained; pre-dispatch 0.5 released)", reserved)
	}
	var resState2 string
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT state FROM reservations WHERE request_id=$1`, postID).Scan(&resState2); err != nil {
		t.Fatal(err)
	}
	if resState2 != "unknown" {
		t.Fatalf("post-dispatch reservation = %s, want unknown (funds retained)", resState2)
	}
	var acctState string
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT state FROM accounts WHERE id=$1`, accountID).Scan(&acctState); err != nil {
		t.Fatal(err)
	}
	if acctState != "recovery_hold" {
		t.Fatalf("account state = %s, want recovery_hold", acctState)
	}
}

// Review P1-6: the OAuth callback must be reachable on the REAL outer router.
func TestOAuthCallbackOnMainRouter(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	// synthetic token endpoint
	codeCh := make(chan string, 1)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "authorization_code" {
			w.WriteHeader(400)
			return
		}
		if r.FormValue("code") != <-codeCh {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	oauth := accounts.NewManager(e.db, "http://auth.test/authorize", tokenSrv.URL, "client-1", "http://cb.test")
	e.admin.OAuth = oauth
	e.gw.Refresh = oauth

	handler := server.Build(server.Deps{DB: e.db, Gateway: e.gw, Admin: e.admin})

	_, state, _, _, err := oauth.StartSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	code := "code-1"
	codeCh <- code
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?state="+state+"&code="+code, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("callback status %d body %s", rec.Code, rec.Body.String())
	}
	// single-use replay must fail
	req2 := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?state="+state+"&code="+code, nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code == 200 {
		t.Fatalf("state reuse must fail on the main router")
	}
}

// Review P2-9: unknown requests are resolvable through the admin API with an
// evidence-based adjustment, converging to a terminal state and releasing the
// account when no unknowns remain.
func TestResolveUnknownFlow(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var keyID, accountID, periodID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM accounts`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO budget_periods(policy_id, period_start, period_end, timezone, limit_snapshot)
		SELECT id, CURRENT_DATE, CURRENT_DATE + 7, 'UTC', 100 FROM budget_policies WHERE owner_type='account' LIMIT 1
		RETURNING id::text`).Scan(&periodID); err != nil {
		t.Fatal(err)
	}
	reqID := "req_unknown_resolve"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state, account_id, price_version_id)
		 SELECT $1,$2,'gpt-5-codex','unknown',$3,(SELECT id FROM price_versions WHERE status='active')`,
		reqID, keyID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES($1,$2,1,'unknown')`, reqID, periodID); err != nil {
		t.Fatal(err)
	}

	handler := e.admin.Routes()
	// Admin session row created directly (seeded admin has no known password).
	sessionToken := "test-session-token"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO admin_sessions(member_id, token_hash, expires_at)
		 SELECT id, $1, now() + interval '1 hour' FROM members WHERE role='admin' LIMIT 1`,
		storage.HashToken(sessionToken)); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50,"note":"evidence"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/requests/"+reqID+"/resolve-unknown", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("resolve status %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Cost string `json:"cost"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	var reqState string
	if err := e.db.Pool.QueryRow(ctx, `SELECT state FROM requests WHERE id=$1`, reqID).Scan(&reqState); err != nil {
		t.Fatal(err)
	}
	if reqState != "completed" {
		t.Fatalf("request state = %s, want completed", reqState)
	}
	var adjustments int
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM usage_ledger WHERE request_id=$1 AND entry_type='adjustment'`, reqID).Scan(&adjustments); err != nil {
		t.Fatal(err)
	}
	if adjustments != 1 {
		t.Fatalf("adjustment rows = %d, want 1", adjustments)
	}
	var acctState string
	if err := e.db.Pool.QueryRow(ctx, `SELECT state FROM accounts WHERE id=$1`, accountID).Scan(&acctState); err != nil {
		t.Fatal(err)
	}
	if acctState != "active" {
		t.Fatalf("account state = %s, want active after all unknowns resolved", acctState)
	}
}

// Review P2-11: a saturated account must never be picked when another
// account in the same tier has capacity.
func TestSchedulerSkipsFullAccount(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var keyID, groupID, accountA string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM account_groups`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM accounts`).Scan(&accountA); err != nil {
		t.Fatal(err)
	}
	var accountB string
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO accounts(label, credentials_ciphertext, concurrency_limit, priority, state)
		 VALUES('acctB',(SELECT credentials_ciphertext FROM accounts WHERE id=$1),5,200,'active') RETURNING id::text`,
		accountA).Scan(&accountB); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO account_group_members(group_id, account_id) VALUES($1,$2)`, groupID, accountB); err != nil {
		t.Fatal(err)
	}
	// Saturate A (limit 1) from a different key.
	e.gw.Slots.Acquire("other-key", 0, accountA, 1)

	acct, release, err := e.gw.Sched.AcquireAccount(ctx, keyID, 1, "")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	if acct.ID != accountB {
		t.Fatalf("picked %s, want the free account %s", acct.ID, accountB)
	}
}

var _ = billing.ActivePriceVersion
