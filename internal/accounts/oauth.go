// Package accounts implements Codex OAuth: PKCE + random state, short-lived
// single-use sessions bound to the admin session that started them, and
// per-account mutually-exclusive token refresh (§8, §22, P1-02).
//
// The authorization/refresh endpoints are configurable; real end-to-end
// behaviour is pending P0-01 verification and tests run against a synthetic
// authorization server.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"subai/internal/storage"
)

var (
	ErrSessionExpired       = errors.New("oauth session expired")
	ErrSessionConsumed      = errors.New("oauth session already used")
	ErrStateMismatch        = errors.New("state mismatch")
	ErrPendingSessionExists = errors.New("an authorization is already in progress for this account")
)

// These are public OAuth client settings used by the Codex CLI flow. The
// client ID is not a secret. Its registered redirect URI is localhost, so a
// remote admin UI completes the flow by submitting the resulting callback URL.
const (
	DefaultAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	DefaultTokenURL     = "https://auth.openai.com/oauth/token"
	DefaultClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultRedirectURI  = "http://localhost:1455/auth/callback"
)

type Manager struct {
	DB           *storage.DB
	AuthorizeURL string // upstream authorization endpoint (configurable, P0-01)
	TokenURL     string // upstream token endpoint
	ClientID     string
	RedirectURI  string // must be an upstream-registered callback (§22)
	HTTP         *http.Client

	mu      sync.Mutex
	refresh map[string]bool // accountID → refreshing
}

func NewManager(db *storage.DB, authorizeURL, tokenURL, clientID, redirectURI string) *Manager {
	return &Manager{
		DB: db, AuthorizeURL: authorizeURL, TokenURL: tokenURL,
		ClientID: clientID, RedirectURI: redirectURI,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		refresh: map[string]bool{},
	}
}

// StartSession creates a pending OAuth session: random state, PKCE verifier,
// 10-minute validity, single consumption (§22). P1-03: when reauthorizing,
// enforce one pending session per account to prevent credential version races.
func (m *Manager) StartSession(ctx context.Context, accountID *string) (sessionID, state, authorizeURL, verifier string, err error) {
	if err = m.validateConfig(); err != nil {
		return "", "", "", "", err
	}
	if accountID != nil {
		if *accountID == "" {
			return "", "", "", "", errors.New("reuse account ID is empty")
		}
		var exists bool
		if err = m.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, *accountID).Scan(&exists); err != nil {
			return "", "", "", "", err
		}
		if !exists {
			return "", "", "", "", errors.New("reuse account not found")
		}
		// P1-02 fix: Database-level enforcement via partial unique index removes
		// the check-then-insert race. The INSERT below will fail with ON CONFLICT
		// if a pending session already exists.
	}
	stateBytes := make([]byte, 32)
	verBytes := make([]byte, 48)
	if _, err = rand.Read(stateBytes); err != nil {
		return
	}
	if _, err = rand.Read(verBytes); err != nil {
		return
	}
	state = base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier = base64.RawURLEncoding.EncodeToString(verBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// An abandoned reauthorization must not block the account forever.
	if accountID != nil {
		if _, err = m.DB.Pool.Exec(ctx, `
			UPDATE oauth_sessions SET status='expired'
			WHERE account_id=$1 AND status='pending' AND expires_at <= now()`, *accountID); err != nil {
			return "", "", "", "", err
		}
	}

	err = m.DB.Pool.QueryRow(ctx, `
		INSERT INTO oauth_sessions(account_id, state, verifier, redirect_uri, expires_at)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (account_id) WHERE status='pending' DO NOTHING
		RETURNING id`,
		accountID, state, verifier, m.RedirectURI, time.Now().Add(10*time.Minute)).Scan(&sessionID)
	if err != nil {
		// ON CONFLICT DO NOTHING returns no rows when conflict occurs
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", "", err
		}
		// No rows means conflict - account already has pending session
		return "", "", "", "", ErrPendingSessionExists
	}
	authorizeURL = m.buildAuthorizeURL(state, challenge)
	return sessionID, state, authorizeURL, verifier, nil
}

func (m *Manager) buildAuthorizeURL(state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", m.ClientID)
	q.Set("redirect_uri", m.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", "openid email profile offline_access")
	q.Set("prompt", "login")
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	return strings.TrimRight(m.AuthorizeURL, "?") + "?" + q.Encode()
}

func (m *Manager) validateConfig() error {
	for name, value := range map[string]string{
		"authorize URL": m.AuthorizeURL,
		"token URL":     m.TokenURL,
		"client ID":     m.ClientID,
		"redirect URI":  m.RedirectURI,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("OAuth %s is not configured", name)
		}
	}
	for name, raw := range map[string]string{"authorize URL": m.AuthorizeURL, "token URL": m.TokenURL, "redirect URI": m.RedirectURI} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("OAuth %s is invalid", name)
		}
	}
	return nil
}

// CompleteCallbackURL supports CPA's remote-browser flow: OpenAI redirects to
// the registered localhost URI, and the administrator pastes that URL back
// into the authenticated management UI. No request is made to the pasted URL.
func (m *Manager) CompleteCallbackURL(ctx context.Context, sessionID, rawCallbackURL string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("OAuth session ID is required")
	}
	if len(rawCallbackURL) > 16*1024 {
		return "", errors.New("OAuth callback URL is too long")
	}
	callback, err := url.Parse(strings.TrimSpace(rawCallbackURL))
	if err != nil || callback.Scheme == "" || callback.Host == "" {
		return "", errors.New("invalid OAuth callback URL")
	}
	expected, err := url.Parse(m.RedirectURI)
	if err != nil || !strings.EqualFold(callback.Scheme, expected.Scheme) ||
		!strings.EqualFold(callback.Host, expected.Host) || callback.Path != expected.Path {
		return "", errors.New("callback URL does not match the configured redirect URI")
	}
	if providerErr := callback.Query().Get("error"); providerErr != "" {
		description := callback.Query().Get("error_description")
		if description != "" {
			return "", fmt.Errorf("OAuth provider rejected authorization: %s (%s)", providerErr, description)
		}
		return "", fmt.Errorf("OAuth provider rejected authorization: %s", providerErr)
	}
	state, code := callback.Query().Get("state"), callback.Query().Get("code")
	if state == "" || code == "" {
		return "", errors.New("callback URL must contain state and code")
	}
	return m.completeCallback(ctx, sessionID, state, code)
}

// CompleteCallback exchanges the code for tokens. The state must exist, be
// pending, unexpired and is consumed exactly once within the same transaction
// (single-use guarantee).
func (m *Manager) CompleteCallback(ctx context.Context, state, code string) (accountID string, err error) {
	return m.completeCallback(ctx, "", state, code)
}

func (m *Manager) completeCallback(ctx context.Context, expectedSessionID, state, code string) (accountID string, err error) {
	tx, err := m.DB.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var sessionID, verifier, status string
	var reuseAccountID *string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, verifier, expires_at, status, account_id::text FROM oauth_sessions
		WHERE state=$1 FOR UPDATE`, state).Scan(&sessionID, &verifier, &expiresAt, &status, &reuseAccountID)
	if err != nil {
		return "", ErrStateMismatch
	}
	if expectedSessionID != "" && sessionID != expectedSessionID {
		return "", ErrStateMismatch
	}
	if status != "pending" {
		return "", ErrSessionConsumed
	}
	if time.Now().After(expiresAt) {
		if _, err := tx.Exec(ctx, `UPDATE oauth_sessions SET status='expired', verifier='' WHERE id=$1`, sessionID); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", ErrSessionExpired
	}

	tokens, err := m.exchangeCode(ctx, code, verifier)
	if err != nil {
		return "", err
	}
	sealed, err := sealTokens(m.DB, tokens)
	if err != nil {
		return "", err
	}
	// P1-01 fix: Keep original state value instead of setting to empty string.
	// Status='completed' already prevents state reuse, and state contains no sensitive data.
	// This prevents UNIQUE constraint violations on concurrent completions.
	if _, err := tx.Exec(ctx, `UPDATE oauth_sessions SET status='completed', completed_at=now(), verifier='' WHERE id=$1`, sessionID); err != nil {
		return "", err
	}
	if reuseAccountID != nil {
		// Reauthorization rotates credentials on the existing account. Preserve
		// deliberate pauses and unrelated hold reasons; clearing the reauth hold
		// alone may reactivate a solely reauth-blocked account.
		if _, err := tx.Exec(ctx, `DELETE FROM account_holds WHERE account_id=$1 AND reason='reauth_required'`, *reuseAccountID); err != nil {
			return "", err
		}
		err = tx.QueryRow(ctx, `
			UPDATE accounts SET credentials_ciphertext=$2, credential_version=credential_version+1,
				expires_at=$3,
				state=CASE WHEN state IN ('reauth_required','refreshing') AND NOT EXISTS
					(SELECT 1 FROM account_holds WHERE account_id=$1) THEN 'active' ELSE state END,
				updated_at=now()
			WHERE id=$1 RETURNING id::text`, *reuseAccountID, sealed, tokens.ExpiresAt).Scan(&accountID)
		if err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return accountID, nil
	}

	// Create a new account when the session was not bound to an existing one.
	// Label derives from the upstream account id or a timestamp so names stay
	// unique without operator input here.
	label := tokens.Label
	if label == "" {
		if tokens.AccountID != "" {
			label = "codex-" + tokens.AccountID[:minInt(8, len(tokens.AccountID))]
		} else {
			label = fmt.Sprintf("codex-%d", time.Now().Unix())
		}
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO accounts(provider, label, credentials_ciphertext, credential_version, expires_at, state)
		VALUES('codex', $1, $2, 1, $3, 'active')
		RETURNING id::text`, label, sealed, tokens.ExpiresAt).Scan(&accountID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE oauth_sessions SET account_id=$2 WHERE id=$1`, sessionID, accountID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return accountID, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type tokenSet struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	IDToken      string     `json:"id_token,omitempty"`
	AccountID    string     `json:"account_id,omitempty"`
	Email        string     `json:"email,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Label        string     `json:"-"`
}

func (m *Manager) exchangeCode(ctx context.Context, code, verifier string) (*tokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", m.ClientID)
	form.Set("redirect_uri", m.RedirectURI)
	form.Set("code_verifier", verifier)
	return m.tokenRequest(ctx, form)
}

func (m *Manager) tokenRequest(ctx context.Context, form url.Values) (*tokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}
	ts := &tokenSet{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, IDToken: raw.IDToken, AccountID: raw.AccountID}
	if raw.IDToken != "" {
		accountID, email, err := identityFromIDToken(raw.IDToken)
		if err != nil {
			return nil, fmt.Errorf("parse OAuth id_token: %w", err)
		}
		if ts.AccountID == "" {
			ts.AccountID = accountID
		}
		ts.Email = email
	}
	if raw.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
		ts.ExpiresAt = &t
	}
	return ts, nil
}

func identityFromIDToken(token string) (accountID, email string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 256*1024 {
		return "", "", errors.New("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errors.New("invalid JWT payload encoding")
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", errors.New("invalid JWT claims")
	}
	return strings.TrimSpace(claims.Auth.ChatGPTAccountID), strings.TrimSpace(claims.Email), nil
}

// Refresh rotates tokens for one account with per-account mutual exclusion:
// concurrent refresh attempts coalesce (§8). Optimistic credential_version
// guards against a stale refresh clobbering a newer credential set.
//
// Multi-instance protection: PostgreSQL advisory lock prevents duplicate OAuth
// calls across service instances. The first instance acquires the lock and refreshes;
// others wait and then see fresh credentials, skipping the OAuth call.
func (m *Manager) Refresh(ctx context.Context, accountID string) error {
	m.mu.Lock()
	if m.refresh[accountID] {
		m.mu.Unlock()
		return errors.New("refresh already in progress")
	}
	m.refresh[accountID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.refresh, accountID)
		m.mu.Unlock()
	}()

	// Acquire distributed lock to serialize across processes
	tx, err := m.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// pg_advisory_xact_lock(bigint) - transaction-scoped lock
	lockKey := int64(fnv1a(accountID))
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return err
	}

	// Re-check: another process may have just refreshed while we waited
	var sealed []byte
	var credVersion int
	var expiresAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT credentials_ciphertext, credential_version, expires_at FROM accounts WHERE id=$1`, accountID).
		Scan(&sealed, &credVersion, &expiresAt); err != nil {
		return err
	}

	// If credentials were refreshed while we waited for the lock, skip OAuth call
	if expiresAt != nil && time.Until(*expiresAt) > 5*time.Minute {
		return tx.Commit(ctx) // fresh enough, nothing to do
	}
	plain, err := m.DB.Decrypt(sealed)
	if err != nil {
		return err
	}
	var creds tokenSet
	if err := json.Unmarshal(plain, &creds); err != nil {
		return err
	}
	if creds.RefreshToken == "" {
		return m.setReauthRequired(ctx, accountID, "no refresh token available")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.RefreshToken)
	form.Set("client_id", m.ClientID)
	fresh, err := m.tokenRequest(ctx, form)
	if err != nil {
		_ = m.setReauthRequired(ctx, accountID, "refresh token request failed")
		return err
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = creds.RefreshToken // upstream may not rotate refresh tokens
	}
	if fresh.IDToken == "" {
		fresh.IDToken = creds.IDToken
	}
	if fresh.AccountID == "" {
		fresh.AccountID = creds.AccountID
	}
	if fresh.Email == "" {
		fresh.Email = creds.Email
	}
	sealedFresh, err := sealTokens(m.DB, fresh)
	if err != nil {
		return err
	}
	// Optimistic lock still guards against external credential updates
	tag, err := tx.Exec(ctx, `
		UPDATE accounts SET credentials_ciphertext=$2, credential_version=credential_version+1,
			expires_at=$3, updated_at=now()
		WHERE id=$1 AND credential_version=$4`, accountID, sealedFresh, fresh.ExpiresAt, credVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("credential changed concurrently; refresh discarded")
	}
	// Review R3-02: credential refresh only updates credentials, never account state.
	// Hold reasons (over_reserve, unknown_pending) persist across refresh and must
	// be cleared through their own resolution paths.
	return tx.Commit(ctx)
}

// SetState is used by the health model (§19 account states).
func (m *Manager) SetState(ctx context.Context, accountID, state string) error {
	_, err := m.DB.Pool.Exec(ctx, `UPDATE accounts SET state=$2, updated_at=now() WHERE id=$1`, accountID, state)
	return err
}

// setReauthRequired transitions the account to reauth_required state and records
// the reason as an account hold for audit trail consistency (R3-01 extension).
func (m *Manager) setReauthRequired(ctx context.Context, accountID, reason string) error {
	tx, err := m.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := AddHoldTx(ctx, tx, accountID, HoldReasonReauthRequired, map[string]any{"reason": reason}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET state='reauth_required', updated_at=now() WHERE id=$1`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func sealTokens(db *storage.DB, tokens *tokenSet) ([]byte, error) {
	b, err := json.Marshal(tokens)
	if err != nil {
		return nil, err
	}
	return db.Encrypt(b)
}

// fnv1a hashes string to int64 for PostgreSQL advisory lock key.
// FNV-1a is fast and provides good distribution for lock keys.
func fnv1a(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
