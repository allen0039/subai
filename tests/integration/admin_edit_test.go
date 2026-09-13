package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

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
