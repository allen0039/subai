package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"subai/internal/billing"
	"subai/internal/storage"
)

func TestAdminAccountAndProxyEditing(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	token := "admin-edit-test"
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO admin_sessions(member_id,token_hash,expires_at) SELECT id,$1,now()+interval '1 hour' FROM members LIMIT 1`, storage.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.admin.Routes().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	result := call("POST", "/api/admin/proxies", `{"name":"editable","kind":"socks5","endpoint":"localhost:1080","username":"original","password":"secret"}`, 201)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(result), &created); err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/proxies/" + created.ID
	call("PATCH", path, `{"version":1,"name":"updated","kind":"http","endpoint":"localhost:8081","username":"changed","password":""}`, 200)
	var name, kind, endpoint string
	var sealed []byte
	if err := e.db.Pool.QueryRow(ctx, `SELECT name,kind,endpoint,credentials_ciphertext FROM proxy_profiles WHERE id=$1`, created.ID).Scan(&name, &kind, &endpoint, &sealed); err != nil {
		t.Fatal(err)
	}
	plain, err := e.db.Decrypt(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var creds map[string]string
	if err := json.Unmarshal(plain, &creds); err != nil {
		t.Fatal(err)
	}
	if name != "updated" || kind != "http" || endpoint != "localhost:8081" || creds["Username"] != "changed" || creds["Password"] != "secret" {
		t.Fatal("proxy details or credentials did not persist")
	}
	call("PATCH", path, `{"version":1,"status":"disabled"}`, 409)
	call("PATCH", path, `{"version":2,"clear_credentials":true}`, 200)
	var accountID string
	var version int
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text,version FROM accounts LIMIT 1`).Scan(&accountID, &version); err != nil {
		t.Fatal(err)
	}
	call("PATCH", "/api/admin/accounts/"+accountID, fmt.Sprintf(`{"version":%d,"label":"renamed","proxy_id":%q}`, version, created.ID), 200)
	var proxyID, label string
	if err := e.db.Pool.QueryRow(ctx, `SELECT a.label,p.primary_proxy_id::text FROM accounts a JOIN egress_policies p ON p.id=a.egress_policy_id WHERE a.id=$1`, accountID).Scan(&label, &proxyID); err != nil {
		t.Fatal(err)
	}
	if label != "renamed" || proxyID != created.ID {
		t.Fatal("account changes did not persist")
	}
	call("GET", "/api/admin/accounts", "", 200)
	call("DELETE", path, `{"version":3}`, 409)
	result = call("POST", "/api/admin/proxies", `{"name":"unused","kind":"direct"}`, 201)
	if err := json.Unmarshal([]byte(result), &created); err != nil {
		t.Fatal(err)
	}
	call("DELETE", "/api/admin/proxies/"+created.ID, `{"version":2}`, 409)
	call("DELETE", "/api/admin/proxies/"+created.ID, `{"version":1}`, 200)
}

func TestManualPriceOverridePreservesCatalogAndOnlyOverridesEditedModels(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	token := "admin-price-override-test"
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO admin_sessions(member_id,token_hash,expires_at) SELECT id,$1,now()+interval '1 hour' FROM members LIMIT 1`, storage.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	var activeID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM price_versions WHERE status='active'`).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO model_prices(price_version_id,model,input_per_mtok,cached_input_per_mtok,output_per_mtok)
		VALUES($1,'catalog-only',1,0.1,5)`, activeID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/prices/overrides", strings.NewReader(`{
		"models":[{"model":"gpt-5-codex","input_per_mtok":"3","cached_input_per_mtok":"0.3","output_per_mtok":"12"}],
		"notes":"test override"
	}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.admin.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("override status %d body %s", rec.Code, rec.Body.String())
	}
	var modelCount, overrideCount int
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT count(*),count(*) FILTER (WHERE is_manual_override)
		FROM model_prices m JOIN price_versions v ON v.id=m.price_version_id
		WHERE v.status='active'`).Scan(&modelCount, &overrideCount); err != nil {
		t.Fatal(err)
	}
	if modelCount != 2 || overrideCount != 1 {
		t.Fatalf("manual version models=%d overrides=%d", modelCount, overrideCount)
	}

	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"gpt-5-codex":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000002,"output_cost_per_token":0.00001},
			"catalog-only":{"input_cost_per_token":0.000004,"cache_read_input_token_cost":0.0000004,"output_cost_per_token":0.00002},
			"new-model":{"input_cost_per_token":0.000005,"cache_read_input_token_cost":0.0000005,"output_cost_per_token":0.000025}
		}`))
	}))
	defer catalog.Close()
	result, err := billing.SyncPriceCatalog(ctx, e.db.Pool, catalog.Client(), catalog.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	var overriddenInput, catalogInput string
	if err := e.db.Pool.QueryRow(ctx, `SELECT input_per_mtok::text FROM model_prices WHERE price_version_id=$1 AND model='gpt-5-codex'`, result.VersionID).Scan(&overriddenInput); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT input_per_mtok::text FROM model_prices WHERE price_version_id=$1 AND model='catalog-only'`, result.VersionID).Scan(&catalogInput); err != nil {
		t.Fatal(err)
	}
	if overriddenInput != "3.000000000000" || catalogInput != "4.000000000000" || result.Models != 3 {
		t.Fatalf("override input=%s catalog input=%s result=%#v", overriddenInput, catalogInput, result)
	}
}
