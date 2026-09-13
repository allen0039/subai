package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"subai/internal/accounts"
	"testing"
)

func TestOAuthRemoteCallbackAndRefresh(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	calls := 0
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"test@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-test"}}`)) + ".sig"
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		if r.FormValue("grant_type") == "refresh_token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "expires_in": 3600})
			return
		}
		if r.FormValue("redirect_uri") != accounts.DefaultRedirectURI || r.FormValue("code_verifier") == "" {
			t.Error("missing exchange parameters")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "initial", "refresh_token": "refresh", "id_token": jwt, "expires_in": 1})
	}))
	defer token.Close()
	m := accounts.NewManager(e.db, accounts.DefaultAuthorizeURL, token.URL, accounts.DefaultClientID, accounts.DefaultRedirectURI)
	id, state, _, _, err := m.StartSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, _, err := m.StartSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cb := accounts.DefaultRedirectURI + "?" + url.Values{"state": {state}, "code": {"code"}}.Encode()
	if _, err := m.CompleteCallbackURL(ctx, other, cb); !errors.Is(err, accounts.ErrStateMismatch) {
		t.Fatalf("wrong session: %v", err)
	}
	if calls != 0 {
		t.Fatal("wrong session called token endpoint")
	}
	account, err := m.CompleteCallbackURL(ctx, id, cb)
	if err != nil {
		t.Fatal(err)
	}
	var linked string
	if err := e.db.Pool.QueryRow(ctx, `SELECT account_id::text FROM oauth_sessions WHERE id=$1`, id).Scan(&linked); err != nil || linked != account {
		t.Fatalf("session account: %s %v", linked, err)
	}
	if _, err := m.CompleteCallbackURL(ctx, id, cb); !errors.Is(err, accounts.ErrSessionConsumed) {
		t.Fatalf("replay: %v", err)
	}
	if err := m.Refresh(ctx, account); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := e.db.Pool.QueryRow(ctx, `SELECT credentials_ciphertext FROM accounts WHERE id=$1`, account).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	plain, err := e.db.Decrypt(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var creds map[string]any
	if err := json.Unmarshal(plain, &creds); err != nil {
		t.Fatal(err)
	}
	if creds["account_id"] != "acct-test" || creds["access_token"] != "refreshed" || creds["refresh_token"] != "refresh" {
		t.Fatal("refresh lost credentials or account identity")
	}
	expired, _, _, _, err := m.StartSession(ctx, &account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE oauth_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1`, expired); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := m.StartSession(ctx, &account); err != nil {
		t.Fatalf("expired session blocks retry: %v", err)
	}
}
