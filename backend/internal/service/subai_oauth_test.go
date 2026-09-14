package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type subaiOAuthClient struct {
	calls    atomic.Int32
	fail     bool
	verifier string
	mu       sync.Mutex
}

func (c *subaiOAuthClient) ExchangeCode(_ context.Context, code, verifier, redirect, proxy, client string) (*openai.TokenResponse, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.verifier = verifier
	c.mu.Unlock()
	if c.fail {
		return nil, errors.New("simulated upstream timeout")
	}
	identity := base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"client-string","email":"test@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`))
	return &openai.TokenResponse{AccessToken: "test-at", RefreshToken: "test-rt", IDToken: "header." + identity + ".sig", ExpiresIn: 3600}, nil
}
func (c *subaiOAuthClient) RefreshToken(ctx context.Context, token, proxy string) (*openai.TokenResponse, error) {
	return c.RefreshTokenWithClientID(ctx, token, proxy, "")
}
func (c *subaiOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return &openai.TokenResponse{AccessToken: "new-at", ExpiresIn: 3600}, nil
}

func TestSubAIOAuth_CallbackContractAndIdentity(t *testing.T) {
	client := &subaiOAuthClient{}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	result, err := svc.GenerateAuthURL(context.Background(), nil, "", PlatformOpenAI, 7)
	require.NoError(t, err)
	auth, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, "login", auth.Query().Get("prompt"))
	require.Equal(t, "openid email profile offline_access", auth.Query().Get("scope"))
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Len(t, session.CodeVerifier, 64)
	require.Len(t, session.State, 43)
	require.Equal(t, openai.GenerateCodeChallenge(session.CodeVerifier), auth.Query().Get("code_challenge"))
	callback := session.RedirectURI + "?code=valid&state=" + url.QueryEscape(session.State)
	for _, tc := range []struct {
		name, callback string
		owner          int64
	}{
		{"wrong owner", callback, 8}, {"wrong host", "http://attacker.invalid/auth/callback?code=c&state=" + session.State, 7},
		{"wrong path", "http://localhost:1455/wrong?code=c&state=" + session.State, 7},
		{"wrong state", session.RedirectURI + "?code=c&state=wrong", 7},
		{"missing state", session.RedirectURI + "?code=c", 7},
		{"duplicate code", callback + "&code=second", 7}, {"fragment", callback + "#fragment", 7},
		{"provider denied", callback + "&error=access_denied", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: result.SessionID, CallbackURL: tc.callback, OwnerID: tc.owner})
			require.Error(t, err)
			require.Zero(t, client.calls.Load())
		})
	}
	info, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: result.SessionID, CallbackURL: callback, OwnerID: 7})
	require.NoError(t, err)
	require.Equal(t, "acct-1", info.ChatGPTAccountID)
	require.Equal(t, "test@example.com", info.Email)
	_, err = svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: result.SessionID, CallbackURL: callback, OwnerID: 7})
	require.Error(t, err)
	require.Equal(t, int32(1), client.calls.Load())
}

func TestSubAIOAuth_ExpiredAndAmbiguousExchangeCannotReplay(t *testing.T) {
	client := &subaiOAuthClient{fail: true}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	expired := &openai.OAuthSession{State: "s", CreatedAt: time.Now().Add(-11 * time.Minute)}
	require.NoError(t, svc.sessionStore.Set("expired", expired))
	_, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "expired", Code: "c", State: "s"})
	require.Error(t, err)
	require.Zero(t, client.calls.Load())
	require.NoError(t, svc.sessionStore.Set("sid", &openai.OAuthSession{State: "s", CreatedAt: time.Now(), RedirectURI: openai.DefaultRedirectURI}))
	for range 2 {
		_, err = svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "sid", Code: "c", State: "s"})
		require.Error(t, err)
	}
	require.Equal(t, int32(1), client.calls.Load(), "failed upstream exchange may have consumed the authorization code")
}

func TestSubAIOAuth_RefreshPreservesOriginalIdentity(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, &subaiOAuthClient{})
	defer svc.Stop()
	info, err := svc.RefreshAccountToken(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "old-rt", "id_token": "old-id", "chatgpt_account_id": "old-account", "email": "old-email"}})
	require.NoError(t, err)
	require.Equal(t, "new-at", info.AccessToken)
	require.Equal(t, "old-rt", info.RefreshToken)
	require.Equal(t, "old-id", info.IDToken)
	require.Equal(t, "old-account", info.ChatGPTAccountID)
	require.Equal(t, "old-email", info.Email)
}
