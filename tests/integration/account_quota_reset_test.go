package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"subai/internal/accounts"
	"subai/internal/egress"
)

func TestAdminConsumesResetCreditAndRefreshesSnapshot(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	var accountID string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT id::text FROM accounts LIMIT 1`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	var consumeCalls int32
	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/consume":
			if r.Method != http.MethodPost {
				t.Fatalf("consume method = %s", r.Method)
			}
			atomic.AddInt32(&consumeCalls, 1)
			_, _ = fmt.Fprint(w, `{"code":"ok","windows_reset":2}`)
		case "/usage":
			_, _ = fmt.Fprint(w, `{"plan_type":"pro","rate_limit":{"allowed":true},"rate_limit_reset_credits":{"available_count":1}}`)
		case "/credits":
			_, _ = fmt.Fprint(w, `{"available_count":1,"credits":[{"status":"available","reset_type":"codex_rate_limits","expires_at":"2026-12-01T00:00:00Z"}]}`)
		case "/accounts", "/subscription":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer quotaServer.Close()
	e.admin.Quota = &accounts.QuotaClient{
		UsageURL:        quotaServer.URL + "/usage",
		ResetCreditsURL: quotaServer.URL + "/credits",
		ResetConsumeURL: quotaServer.URL + "/consume",
		AccountsURL:     quotaServer.URL + "/accounts",
		SubscriptionURL: quotaServer.URL + "/subscription",
	}
	e.admin.Egress = egress.NewResolver(e.db)

	rec := adminRequest(t, e, http.MethodPost, "/api/admin/accounts/"+accountID+"/quota/reset-credits/consume", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&consumeCalls); got != 1 {
		t.Fatalf("consume calls = %d, want 1", got)
	}
	var available int
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT (snapshot->'rate_limit_reset_credits'->>'available_count')::int FROM account_quota_snapshots WHERE account_id=$1`, accountID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("saved available_count = %d, want 1", available)
	}
	var auditCount int
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_events WHERE action='account.quota_reset_credit_consume' AND target_id=$1`, accountID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("consume audit events = %d, want 1", auditCount)
	}
}
