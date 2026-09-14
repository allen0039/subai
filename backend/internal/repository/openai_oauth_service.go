package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// NewOpenAIOAuthClient creates a new OpenAI OAuth client
func NewOpenAIOAuthClient() service.OpenAIOAuthClient {
	return &openaiOAuthService{tokenURL: openai.ConfiguredTokenURL()}
}

type openaiOAuthService struct {
	tokenURL string
}

func (s *openaiOAuthService) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {

	if redirectURI == "" {
		redirectURI = openai.ConfiguredRedirectURI()
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID, _ = openai.OAuthClientConfigByPlatform(openai.OAuthPlatformOpenAI)
	}

	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", clientID)
	formData.Set("code", code)
	formData.Set("redirect_uri", redirectURI)
	formData.Set("code_verifier", codeVerifier)

	return s.tokenRequest(ctx, formData, proxyURL, "OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED")
}

func (s *openaiOAuthService) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	return s.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, "")
}

func (s *openaiOAuthService) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	// 调用方应始终传入正确的 client_id；为兼容旧数据，未指定时默认使用 OpenAI ClientID
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID, _ = openai.OAuthClientConfigByPlatform(openai.OAuthPlatformOpenAI)
	}
	return s.refreshTokenWithClientID(ctx, refreshToken, proxyURL, clientID)
}

func (s *openaiOAuthService) refreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL, clientID string) (*openai.TokenResponse, error) {

	formData := url.Values{}
	formData.Set("grant_type", "refresh_token")
	formData.Set("refresh_token", refreshToken)
	formData.Set("client_id", clientID)

	return s.tokenRequest(ctx, formData, proxyURL, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED")
}

// SubAI's login transport: standard Go HTTP, form encoding, bounded body and
// no provider response body in errors (it may contain credentials).
func (s *openaiOAuthService) tokenRequest(ctx context.Context, form url.Values, proxyURL, reason string) (*openai.TokenResponse, error) {
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxyURL, Timeout: 20 * time.Second})
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_OAUTH_CLIENT_INIT_FAILED", "invalid OAuth proxy configuration")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "invalid token endpoint")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Do not forward authorization codes to a redirected endpoint.
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := transport.Do(request)
	if err != nil {
		if shouldReturnOpenAINoProxyHint(ctx, proxyURL, err) {
			return nil, newOpenAINoProxyHintError(err)
		}
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "OAuth token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, infraerrors.Newf(http.StatusBadGateway, reason, "token endpoint status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, infraerrors.New(http.StatusBadGateway, reason, "invalid token response size")
	}
	var token openai.TokenResponse
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, infraerrors.New(http.StatusBadGateway, reason, "invalid token response or missing access_token")
	}
	return &token, nil
}

func shouldReturnOpenAINoProxyHint(ctx context.Context, proxyURL string, err error) bool {
	if strings.TrimSpace(proxyURL) != "" || err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func newOpenAINoProxyHintError(cause error) error {
	return infraerrors.New(
		http.StatusBadGateway,
		"OPENAI_OAUTH_PROXY_REQUIRED",
		"OpenAI OAuth request failed: no proxy is configured and this server could not reach OpenAI directly. Select a proxy that can access OpenAI, then retry; if the authorization code has expired, regenerate the authorization URL.",
	).WithCause(cause)
}
