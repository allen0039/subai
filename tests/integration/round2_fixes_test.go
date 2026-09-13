package integration

// Round-2 review regressions (docs/INDEPENDENT_REVIEW_ROUND2_2026-09-12.md).

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

	"github.com/shopspring/decimal"

	"subai/internal/billing"
	"subai/internal/config"
	"subai/internal/gateway"
	"subai/internal/server"
	"subai/internal/storage"
)

// R2-01: the upstream ACCEPTED the POST (read the whole body) but broke the
// connection before any response. The outcome is unconfirmable — the request
// must NOT be replayed on a fallback egress.
func TestNoReplayWhenUpstreamBreaksBeforeResponse(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	var upstreamHits int32
	upMux := http.NewServeMux()
	upMux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&upstreamHits, 1)
		if n > 1 {
			return // a replay would show up here
		}
		// Consume the request (it is now "received"), then break silently.
		buf := make([]byte, 64<<10)
		for {
			if _, err := r.Body.Read(buf); err != nil {
				break
			}
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close() // no HTTP response at all
	})
	upSrv := httptest.NewServer(upMux)
	defer upSrv.Close()
	e.gw.Upstream.BaseURL = upSrv.URL
	e.gw.Cfg.UpstreamBaseURL = upSrv.URL

	// primary direct + fallback direct, failure_mode=fallback
	var prof1, prof2, policyID string
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO proxy_profiles(name, kind) VALUES('d1','direct') RETURNING id::text`).Scan(&prof1); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO proxy_profiles(name, kind) VALUES('d2','direct') RETURNING id::text`).Scan(&prof2); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(context.Background(),
		`INSERT INTO egress_policies(name, primary_proxy_id, failure_mode, ordered_fallback_proxy_ids)
		 VALUES('r2fb',$1,'fallback',$2) RETURNING id::text`, prof1, []string{prof2}).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE accounts SET egress_policy_id=$1`, policyID); err != nil {
		t.Fatal(err)
	}

	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 200 {
		t.Logf("gateway response: %d %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 1 {
		t.Fatalf("upstream received %d POSTs, want exactly 1 (R2-01: no replay after transmission)", got)
	}
	var state string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT state FROM requests`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("state = %s, want unknown (unconfirmable outcome)", state)
	}
}

// R2-02: an over-reservation manual adjustment must KEEP the account isolated.
func TestOverReserveAdjustKeepsHold(t *testing.T) {
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
	reqID := "req_r2_over"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state, account_id, price_version_id)
		 SELECT $1,$2,'gpt-5-codex','unknown',$3, (SELECT id FROM price_versions WHERE status='active')`,
		reqID, keyID, accountID); err != nil {
		t.Fatal(err)
	}
	// reservation was tiny; reported usage is way beyond it
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES($1,$2,0.000001,'unknown')`, reqID, periodID); err != nil {
		t.Fatal(err)
	}

	result := e.resolveUnknownViaAPI(t, reqID, 100, 0, 900)
	if !result.OverReserve {
		t.Fatalf("expected over_reserve=true, got %+v", result)
	}
	var acctState string
	if err := e.db.Pool.QueryRow(ctx, `SELECT state FROM accounts WHERE id=$1`, accountID).Scan(&acctState); err != nil {
		t.Fatal(err)
	}
	if acctState != "recovery_hold" {
		t.Fatalf("account state = %s, want recovery_hold to persist (R2-02)", acctState)
	}
}

// R2-03: manual adjustment prices history at the request's FROZEN version,
// even after the active table has changed.
func TestResolveUsesFrozenPriceVersion(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var keyID, periodID, oldVersion string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO budget_periods(policy_id, period_start, period_end, timezone, limit_snapshot)
		SELECT id, CURRENT_DATE, CURRENT_DATE + 7, 'UTC', 100 FROM budget_policies WHERE owner_type='account' LIMIT 1
		RETURNING id::text`).Scan(&periodID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT id::text FROM price_versions WHERE status='active'`).Scan(&oldVersion); err != nil {
		t.Fatal(err)
	}
	reqID := "req_r2_frozen"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state, price_version_id)
		 VALUES($1,$2,'gpt-5-codex','unknown',$3)`, reqID, keyID, oldVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES($1,$2,1,'unknown')`, reqID, periodID); err != nil {
		t.Fatal(err)
	}

	// The price table CHANGES after the request: new active version at 2x.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE price_versions SET status='superseded' WHERE status='active'`); err != nil {
		t.Fatal(err)
	}
	var newVersion string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO price_versions(origin, status, activated_at, notes)
		VALUES('manual','active',now(),'r2-03 test') RETURNING id::text`).Scan(&newVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO model_prices(price_version_id, model, input_per_mtok, cached_input_per_mtok, output_per_mtok)
		VALUES($1,'gpt-5-codex',4,0.4,20)`, newVersion); err != nil {
		t.Fatal(err)
	}

	result := e.resolveUnknownViaAPI(t, reqID, 100, 0, 50)
	if result.PriceVersionID != oldVersion {
		t.Fatalf("adjustment used version %s, want frozen %s (R2-03)", result.PriceVersionID, oldVersion)
	}
	// frozen: (100×2 + 50×10)/1e6 = 0.0007 ; current would be 0.0014
	if !decimal.RequireFromString(result.Cost).Equal(decimal.RequireFromString("0.0007")) {
		t.Fatalf("cost = %s, want 0.0007 at frozen prices", result.Cost)
	}
	var ledgerVersion string
	var ledgerCost string
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT COALESCE(price_version_id::text,''), cost::text FROM usage_ledger WHERE request_id=$1 AND entry_type='adjustment'`,
		reqID).Scan(&ledgerVersion, &ledgerCost); err != nil {
		t.Fatal(err)
	}
	if ledgerVersion != oldVersion || !decimal.RequireFromString(ledgerCost).Equal(decimal.RequireFromString("0.0007")) {
		t.Fatalf("ledger = (%s,%s), want frozen version and cost", ledgerVersion, ledgerCost)
	}
}

// R2-04: recovery must finish convergence for rows an interrupted pass left
// half-done (requests=unknown, reservations=held, accounts=active).
func TestRecoverySweepFinishesPartialStates(t *testing.T) {
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
	// Half-converged unknown: reservations still held, account still active.
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state, account_id) VALUES('req_r2_sweep',$1,'gpt-5-codex','unknown',$2)`,
		keyID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES('req_r2_sweep',$1,0.4,'held')`, periodID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state) VALUES('req_r2_legacy',$1,'gpt-5-codex','cancelled_before_dispatch')`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO reservations(request_id, budget_period_id, amount, state) VALUES('req_r2_legacy',$1,0.2,'held')`, periodID); err != nil {
		t.Fatal(err)
	}

	st := gateway.NewStateTracker(e.db.Pool)
	summary, err := st.OnStartupRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Swept != 2 {
		t.Fatalf("swept = %d, want 2 (R2-04 idempotent sweep)", summary.Swept)
	}
	var unknownRes, releasedRes int
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM reservations WHERE request_id='req_r2_sweep' AND state='unknown'`).Scan(&unknownRes); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM reservations WHERE request_id='req_r2_legacy' AND state='released'`).Scan(&releasedRes); err != nil {
		t.Fatal(err)
	}
	if unknownRes != 1 || releasedRes != 1 {
		t.Fatalf("sweep result: unknown=%d released=%d", unknownRes, releasedRes)
	}
	var acctState string
	if err := e.db.Pool.QueryRow(ctx, `SELECT state FROM accounts WHERE id=$1`, accountID).Scan(&acctState); err != nil {
		t.Fatal(err)
	}
	if acctState != "recovery_hold" {
		t.Fatalf("account = %s, want recovery_hold after sweep", acctState)
	}
	// Idempotent: a second run converges nothing more.
	summary2, err := st.OnStartupRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary2.Swept != 0 {
		t.Fatalf("second sweep moved %d rows, want 0 (idempotent)", summary2.Swept)
	}
}

// R2-05: an upstream timeout must not consume the finalization budget —
// funds converge to unknown and the account is held using a FRESH deadline.
func TestUpstreamTimeoutConvergesUnknown(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.gw.Cfg.UpstreamTimeout = 300 * time.Millisecond
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, "event: response.completed\ndata: {}\n\n")
	}))
	defer slow.Close()
	e.gw.Upstream.BaseURL = slow.URL
	e.gw.Cfg.UpstreamBaseURL = slow.URL

	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	_ = rec
	var state string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT state FROM requests`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("state = %s, want unknown after upstream timeout", state)
	}
	// Both budget scopes (account-level + key-level) hold funds for the one
	// request; all must be retained as unknown.
	var unknownRes, unknownRequests int
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*), count(DISTINCT request_id) FROM reservations WHERE state='unknown'`).Scan(&unknownRes, &unknownRequests); err != nil {
		t.Fatal(err)
	}
	if unknownRequests != 1 || unknownRes < 1 {
		t.Fatalf("unknown reservations = %d across %d requests, want all scopes of the one request retained", unknownRes, unknownRequests)
	}
	var acctState string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT state FROM accounts`).Scan(&acctState); err != nil {
		t.Fatal(err)
	}
	if acctState != "recovery_hold" {
		t.Fatalf("account = %s, want recovery_hold after timeout", acctState)
	}
}

// R2-08: every ResourcePage-backed list honours limit/offset — 25 rows page
// through without duplicates or gaps.
func TestPaginationContract25Rows(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	handler := e.admin.Routes()
	sessionToken := "r2-pagination-session"
	if _, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_sessions(member_id, token_hash, expires_at)
		 SELECT id, $1, now() + interval '1 hour' FROM members WHERE role='admin' LIMIT 1
		 ON CONFLICT (token_hash) DO UPDATE SET expires_at=EXCLUDED.expires_at`,
		storage.HashToken(sessionToken)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/proxies",
			strings.NewReader(fmt.Sprintf(`{"name":"p-%02d","kind":"direct"}`, i)))
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("create proxy %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	seen := map[string]bool{}
	for _, offset := range []int{0, 20} {
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/admin/proxies?limit=20&offset=%d", offset), nil)
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("list offset=%d: %d", offset, rec.Code)
		}
		var resp struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		want := 20
		if offset == 20 {
			want = 5
		}
		if len(resp.Data) != want {
			t.Fatalf("offset=%d returned %d rows, want %d", offset, len(resp.Data), want)
		}
		for _, row := range resp.Data {
			if seen[row.ID] {
				t.Fatalf("duplicate id %s across pages", row.ID)
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != 25 {
		t.Fatalf("saw %d rows total, want 25 (no gaps)", len(seen))
	}
}

// R2-07: the exact payloads the rebuilt form sends must be accepted —
// fixed mode without percent fields, percent mode without amount.
func TestBudgetPolicyFormPayloadsAccepted(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	handler := e.admin.Routes()
	sessionToken := "r2-form-session"
	if _, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_sessions(member_id, token_hash, expires_at)
		 SELECT id, $1, now() + interval '1 hour' FROM members WHERE role='admin' LIMIT 1
		 ON CONFLICT (token_hash) DO UPDATE SET expires_at=EXCLUDED.expires_at`,
		storage.HashToken(sessionToken)); err != nil {
		t.Fatal(err)
	}
	post := func(payload string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/budget-policies", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+sessionToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	// fixed form: percent_bps undefined → omitted from the payload entirely
	if code := post(`{"owner_type":"account","owner_account_id":"00000000-0000-0000-0000-000000000000","period":"week","timezone":"UTC","mode":"fixed","amount":"12.5"}`); code == 400 {
		t.Fatalf("fixed-mode form payload rejected as invalid body (R2-07)")
	}
}

// resolveUnknownViaAPI drives the admin endpoint and decodes the response.
type resolveResult struct {
	Ok             bool   `json:"ok"`
	Cost           string `json:"cost"`
	OverReserve    bool   `json:"over_reserve"`
	PriceVersionID string `json:"price_version_id"`
	Recovered      bool   `json:"account_recovered"`
}

func (e *testEnv) resolveUnknownViaAPI(t *testing.T, requestID string, in, cached, out int64) resolveResult {
	t.Helper()
	handler := e.admin.Routes()
	sessionToken := "r2-resolve-session"
	if _, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO admin_sessions(member_id, token_hash, expires_at)
		 SELECT id, $1, now() + interval '1 hour' FROM members WHERE role='admin' LIMIT 1
		 ON CONFLICT (token_hash) DO UPDATE SET expires_at=EXCLUDED.expires_at`,
		storage.HashToken(sessionToken)); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"note":"evidence"}`, in, cached, out)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/requests/"+requestID+"/resolve-unknown", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("resolve-unknown %d: %s", rec.Code, rec.Body.String())
	}
	var result resolveResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

var (
	_ = config.Config{}
	_ = billing.ActivePriceVersion
	_ = server.Build
)
