package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"subai/internal/storage"
	"testing"
)

func TestR5CachedAdjustmentCost(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	_, err := e.db.Pool.Exec(ctx, `INSERT INTO requests(id,api_key_id,account_id,model,state,price_version_id) SELECT 'req_r5_cached',k.id,a.id,'gpt-5-codex','unknown',p.id FROM api_keys k CROSS JOIN accounts a CROSS JOIN price_versions p LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	result := e.resolveUnknownViaAPI(t, "req_r5_cached", 100, 80, 0)
	if result.Cost != "0.000056" {
		t.Fatalf("cost=%s", result.Cost)
	}
	var cost string
	var input, cached int64
	err = e.db.Pool.QueryRow(ctx, `SELECT cost::text,input_tokens,cached_input_tokens FROM usage_ledger WHERE request_id='req_r5_cached'`).Scan(&cost, &input, &cached)
	if err != nil {
		t.Fatal(err)
	}
	if input != 20 || cached != 80 || cost != "0.000056000000" {
		t.Fatalf("ledger: %s %d %d", cost, input, cached)
	}
}

func TestR5HoldBlocksActivationAndSelection(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var account, key string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM accounts LIMIT 1`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM api_keys LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO account_holds(account_id,reason) VALUES($1,'over_reserve')`, account); err != nil {
		t.Fatal(err)
	}
	token := "r5-session"
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO admin_sessions(member_id,token_hash,expires_at) SELECT id,$1,now()+interval '1 hour' FROM members LIMIT 1`, storage.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/accounts/"+account, strings.NewReader(`{"state":"active","version":1}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.admin.Routes().ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("activation=%d %s", rec.Code, rec.Body.String())
	}
	a, release, err := e.gw.Sched.AcquireAccount(ctx, key, 1, account)
	if err == nil {
		release()
		t.Fatalf("held account selected: %+v", a)
	}
}

func TestR5EqualPriorityRotation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var key, second string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM api_keys LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO accounts(label,credentials_ciphertext,priority) SELECT 'acctB',credentials_ciphertext,priority FROM accounts LIMIT 1 RETURNING id`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO key_routes(api_key_id,target_type,target_id,priority) VALUES($1,'account',$2,100)`, key, second); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		a, release, err := e.gw.Sched.AcquireAccount(ctx, key, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		counts[a.ID]++
		release()
	}
	if len(counts) != 2 {
		t.Fatalf("distribution=%v", counts)
	}
	for _, n := range counts {
		if n != 3 {
			t.Fatalf("distribution=%v", counts)
		}
	}
}
