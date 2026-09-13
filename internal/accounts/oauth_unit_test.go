package accounts

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestCodexAuthorizeURLMatchesCPAFlow(t *testing.T) {
	m := NewManager(nil, DefaultAuthorizeURL, DefaultTokenURL, DefaultClientID, DefaultRedirectURI)
	raw := m.buildAuthorizeURL("state-1", "challenge-1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != DefaultAuthorizeURL {
		t.Fatalf("authorization endpoint = %q", got)
	}
	want := map[string]string{
		"client_id":                  DefaultClientID,
		"response_type":              "code",
		"redirect_uri":               DefaultRedirectURI,
		"scope":                      "openid email profile offline_access",
		"state":                      "state-1",
		"code_challenge":             "challenge-1",
		"code_challenge_method":      "S256",
		"prompt":                     "login",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
	}
	for key, value := range want {
		if got := u.Query().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

func TestIdentityFromIDToken(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"email":                       "operator@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-123"},
	})
	token := strings.Join([]string{"header", base64.RawURLEncoding.EncodeToString(payload), "signature"}, ".")
	accountID, email, err := identityFromIDToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "acct-123" || email != "operator@example.com" {
		t.Fatalf("identity = (%q, %q)", accountID, email)
	}
}

func TestCompleteCallbackURLRejectsWrongRedirectBeforeDatabaseAccess(t *testing.T) {
	m := NewManager(nil, DefaultAuthorizeURL, DefaultTokenURL, DefaultClientID, DefaultRedirectURI)
	_, err := m.CompleteCallbackURL(t.Context(), "session-1", "https://attacker.example/callback?code=x&state=y")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateConfigRejectsEmptyClientID(t *testing.T) {
	m := NewManager(nil, DefaultAuthorizeURL, DefaultTokenURL, "", DefaultRedirectURI)
	if err := m.validateConfig(); err == nil || !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("error = %v", err)
	}
}
