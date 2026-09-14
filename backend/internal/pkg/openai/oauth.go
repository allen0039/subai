package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/redissession"
	"github.com/redis/go-redis/v9"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// OpenAI OAuth Constants (from CRS project - Codex CLI client)
const (
	// OAuth Client ID for OpenAI (Codex CLI official)
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// OAuth endpoints
	AuthorizeURL = "https://auth.openai.com/oauth/authorize"
	TokenURL     = "https://auth.openai.com/oauth/token"

	// Default redirect URI (can be customized)
	DefaultRedirectURI = "http://localhost:1455/auth/callback"

	// Scopes
	DefaultScopes = "openid email profile offline_access"
	// RefreshScopes - scope for token refresh (without offline_access, aligned with CRS project)
	RefreshScopes = "openid profile email"

	// Session TTL
	SessionTTL = 10 * time.Minute
)

const (
	// OAuthPlatformOpenAI uses OpenAI Codex-compatible OAuth client.
	OAuthPlatformOpenAI = "openai"
)

// OAuthSession stores OAuth flow state for OpenAI
type OAuthSession struct {
	ProxyID      *int64    `json:"proxy_id,omitempty"`
	OwnerID      int64     `json:"owner_id"`
	AccountID    int64     `json:"account_id,omitempty"`
	Status       string    `json:"status,omitempty"`
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	ClientID     string    `json:"client_id,omitempty"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	RedirectURI  string    `json:"redirect_uri"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionStore manages OAuth sessions in memory
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*OAuthSession
	stopOnce sync.Once
	stopCh   chan struct{}
	remote   *redissession.Store
	consumed map[string]bool
}

// NewSessionStore creates a new session store
func NewSessionStore() *SessionStore {
	store := &SessionStore{
		sessions: make(map[string]*OAuthSession),
		consumed: make(map[string]bool),
		stopCh:   make(chan struct{}),
	}
	// Start cleanup goroutine
	go store.cleanup()
	return store
}

// NewRedisSessionStore persists sessions across restarts and application instances.
// Redis failures fail closed: falling back to local state would allow code replay.
func NewRedisSessionStore(client *redis.Client) *SessionStore {
	s := NewSessionStore()
	if client != nil {
		s.remote = redissession.New(client, "subai:oauth:openai", SessionTTL)
	}
	return s
}

func (s *SessionStore) Set(id string, session *OAuthSession) error {
	if s.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return s.remote.Set(ctx, id, session)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *session
	s.sessions[id] = &copy
	delete(s.consumed, id)
	return nil
}

func (s *SessionStore) Get(id string) (*OAuthSession, bool) {
	if s.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var session OAuthSession
		ok, err := s.remote.Get(ctx, id, &session)
		return &session, ok && err == nil && time.Since(session.CreatedAt) < SessionTTL
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok || time.Since(session.CreatedAt) >= SessionTTL {
		return nil, false
	}
	copy := *session
	return &copy, true
}

// TryConsume claims the code before any upstream request. A network error can
// leave a code spent upstream, so the operator must start a fresh authorization.
func (s *SessionStore) TryConsume(id string) bool {
	if s.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ok, err := s.remote.TryConsume(ctx, id)
		return ok && err == nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok || s.consumed[id] || time.Since(session.CreatedAt) >= SessionTTL {
		return false
	}
	s.consumed[id] = true
	return true
}

func (s *SessionStore) Delete(id string) {
	if s.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Keep the spent marker until TTL: an in-flight reader must not consume twice.
		_ = s.remote.Set(ctx, id, &OAuthSession{CreatedAt: time.Unix(0, 0)})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	delete(s.consumed, id)
}

// Stop stops the cleanup goroutine
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// cleanup removes expired sessions periodically
func (s *SessionStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > SessionTTL {
					delete(s.sessions, id)
					delete(s.consumed, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GenerateRandomBytes generates cryptographically secure random bytes
func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// GenerateState generates a random state string for OAuth
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// GenerateSessionID generates a unique session ID
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier preserves SubAI’s 48-byte, base64url PKCE verifier.
func GenerateCodeVerifier() (string, error) {
	bytes, err := GenerateRandomBytes(48)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// GenerateCodeChallenge generates a PKCE code challenge using S256 method
// Uses base64url encoding as per RFC 7636
func GenerateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64URLEncode(hash[:])
}

// base64URLEncode encodes bytes to base64url without padding
func base64URLEncode(data []byte) string {
	encoded := base64.URLEncoding.EncodeToString(data)
	// Remove padding
	return strings.TrimRight(encoded, "=")
}

// BuildAuthorizationURL builds the OpenAI OAuth authorization URL
func BuildAuthorizationURL(state, codeChallenge, redirectURI string) string {
	return BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, OAuthPlatformOpenAI)
}

// BuildAuthorizationURLForPlatform builds authorization URL by platform.
func BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, platform string) string {
	if redirectURI == "" {
		redirectURI = ConfiguredRedirectURI()
	}

	clientID, codexFlow := OAuthClientConfigByPlatform(platform)

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", DefaultScopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	// OpenAI specific parameters
	params.Set("id_token_add_organizations", "true")
	params.Set("prompt", "login")
	if codexFlow {
		params.Set("codex_cli_simplified_flow", "true")
	}

	endpoint, err := url.Parse(ConfiguredAuthorizeURL())
	if err != nil {
		return ""
	}
	q := endpoint.Query()
	for key, values := range params {
		q[key] = values
	}
	endpoint.RawQuery = q.Encode()
	return endpoint.String()
}

// OAuthClientConfigByPlatform returns oauth client_id and whether codex simplified flow should be enabled.
func OAuthClientConfigByPlatform(platform string) (clientID string, codexFlow bool) {
	return configured("SUBAI_OAUTH_CLIENT_ID", ClientID), true
}

// TokenRequest represents the token exchange request body
type TokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	CodeVerifier string `json:"code_verifier"`
}

// TokenResponse represents the token response from OpenAI OAuth
type TokenResponse struct {
	AccountID    string `json:"account_id,omitempty"`
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// RefreshTokenRequest represents the refresh token request
type RefreshTokenRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
}

// IDTokenClaims represents the claims from OpenAI ID Token
type IDTokenClaims struct {
	// Standard claims
	Sub           string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Iss           string   `json:"iss"`
	Aud           []string `json:"aud"` // OpenAI returns aud as an array
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`

	// OpenAI specific claims (nested under https://api.openai.com/auth)
	OpenAIAuth *OpenAIAuthClaims `json:"https://api.openai.com/auth,omitempty"`
}

// OpenAIAuthClaims represents the OpenAI specific auth claims
type OpenAIAuthClaims struct {
	ChatGPTAccountID string              `json:"chatgpt_account_id"`
	ChatGPTUserID    string              `json:"chatgpt_user_id"`
	ChatGPTPlanType  string              `json:"chatgpt_plan_type"`
	UserID           string              `json:"user_id"`
	POID             string              `json:"poid"` // organization ID in access_token JWT
	Organizations    []OrganizationClaim `json:"organizations"`
}

// OrganizationClaim represents an organization in the ID Token
type OrganizationClaim struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Title     string `json:"title"`
	IsDefault bool   `json:"is_default"`
}

// BuildTokenRequest creates a token exchange request for OpenAI
func BuildTokenRequest(code, codeVerifier, redirectURI string) *TokenRequest {
	if redirectURI == "" {
		redirectURI = ConfiguredRedirectURI()
	}
	return &TokenRequest{
		GrantType:    "authorization_code",
		ClientID:     ClientID,
		Code:         code,
		RedirectURI:  redirectURI,
		CodeVerifier: codeVerifier,
	}
}

// BuildRefreshTokenRequest creates a refresh token request for OpenAI
func BuildRefreshTokenRequest(refreshToken string) *RefreshTokenRequest {
	return &RefreshTokenRequest{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		ClientID:     ClientID,
		Scope:        RefreshScopes,
	}
}

// ToFormData converts TokenRequest to URL-encoded form data
func (r *TokenRequest) ToFormData() string {
	params := url.Values{}
	params.Set("grant_type", r.GrantType)
	params.Set("client_id", r.ClientID)
	params.Set("code", r.Code)
	params.Set("redirect_uri", r.RedirectURI)
	params.Set("code_verifier", r.CodeVerifier)
	return params.Encode()
}

// ToFormData converts RefreshTokenRequest to URL-encoded form data
func (r *RefreshTokenRequest) ToFormData() string {
	params := url.Values{}
	params.Set("grant_type", r.GrantType)
	params.Set("client_id", r.ClientID)
	params.Set("refresh_token", r.RefreshToken)
	params.Set("scope", r.Scope)
	return params.Encode()
}

// DecodeIDToken decodes the ID Token JWT payload without validating expiration.
// Use this for best-effort extraction (e.g., during data import) where the token may be expired.
func DecodeIDToken(idToken string) (*IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode payload (second part)
	payload := parts[1]
	// Add padding if necessary
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try standard encoding
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
		}
	}

	var claims IDTokenClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	return &claims, nil
}

// ParseIDToken parses the ID Token JWT and extracts claims.
// 注意：当前仅解码 payload 并校验 exp，未验证 JWT 签名。
// 生产环境如需用 ID Token 做授权决策，应通过 OpenAI 的 JWKS 端点验证签名：
//
//	https://auth.openai.com/.well-known/jwks.json
func ParseIDToken(idToken string) (*IDTokenClaims, error) {
	claims, err := DecodeIDToken(idToken)
	if err != nil {
		return nil, err
	}

	// 校验 ID Token 是否已过期（允许 2 分钟时钟偏差，防止因服务器时钟略有差异误判刚颁发的令牌）
	const clockSkewTolerance = 120 // 秒
	now := time.Now().Unix()
	if claims.Exp > 0 && now > claims.Exp+clockSkewTolerance {
		return nil, fmt.Errorf("id_token has expired (exp: %d, now: %d, skew_tolerance: %ds)", claims.Exp, now, clockSkewTolerance)
	}

	return claims, nil
}

// UserInfo represents user information extracted from ID Token claims.
type UserInfo struct {
	Email            string
	ChatGPTAccountID string
	ChatGPTUserID    string
	PlanType         string
	UserID           string
	OrganizationID   string
	Organizations    []OrganizationClaim
}

// GetUserInfo extracts user info from ID Token claims
func (c *IDTokenClaims) GetUserInfo() *UserInfo {
	info := &UserInfo{
		Email: c.Email,
	}

	if c.OpenAIAuth != nil {
		info.ChatGPTAccountID = c.OpenAIAuth.ChatGPTAccountID
		info.ChatGPTUserID = c.OpenAIAuth.ChatGPTUserID
		info.PlanType = c.OpenAIAuth.ChatGPTPlanType
		info.UserID = c.OpenAIAuth.UserID
		info.Organizations = c.OpenAIAuth.Organizations

		// Get default organization ID
		for _, org := range c.OpenAIAuth.Organizations {
			if org.IsDefault {
				info.OrganizationID = org.ID
				break
			}
		}
		// If no default, use first org
		if info.OrganizationID == "" && len(c.OpenAIAuth.Organizations) > 0 {
			info.OrganizationID = c.OpenAIAuth.Organizations[0].ID
		}
	}

	return info
}

// These are the same operator-configurable settings as the original SubAI login.
func configured(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func ConfiguredAuthorizeURL() string { return configured("SUBAI_OAUTH_AUTHORIZE_URL", AuthorizeURL) }
func ConfiguredTokenURL() string     { return configured("SUBAI_OAUTH_TOKEN_URL", TokenURL) }
func ConfiguredRedirectURI() string {
	return configured("SUBAI_OAUTH_REDIRECT_URI", DefaultRedirectURI)
}

// ParseSubAIIdentity mirrors the original SubAI login identity extraction.
// The ID token is received directly from the token endpoint, never accepted as
// a platform login credential. Unrelated claims (e.g. string vs array aud) do
// not prevent extracting the GPT account identity.
func ParseSubAIIdentity(token string) (*IDTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 256*1024 {
		return nil, fmt.Errorf("invalid JWT format")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid JWT payload encoding")
	}
	var claims struct {
		Email string            `json:"email"`
		Auth  *OpenAIAuthClaims `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return nil, fmt.Errorf("invalid JWT claims")
	}
	return &IDTokenClaims{Email: strings.TrimSpace(claims.Email), OpenAIAuth: claims.Auth}, nil
}
