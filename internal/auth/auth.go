// Package auth implements admin session management (cookie based, rate
// limited login) and data-plane API key authentication (§17.1, §22).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"subai/internal/storage"
)

var (
	ErrInvalidKey   = errors.New("invalid api key")
	ErrKeyExpired   = errors.New("api key expired")
	ErrKeyRevoked   = errors.New("api key revoked")
	ErrMemberDenied = errors.New("member disabled")
)

type KeyInfo struct {
	ID               string
	MemberID         string
	MemberStatus     string
	ClientID         *string
	Name             string
	Status           string
	ExpiresAt        *time.Time
	ConcurrencyLimit int
	AllowedModels    []string // nil = all
	AuditPolicyID    *string
}

// LookupKey resolves a bearer key. Result is cached briefly to keep the hot
// path off the database, but member/key status changes take effect within the
// cache TTL; the pipeline re-checks critical state after auditing (§3).
func (s *Service) LookupKey(ctx context.Context, presented string) (*KeyInfo, error) {
	if presented == "" {
		return nil, ErrInvalidKey
	}
	hash := storage.HashToken(presented)
	s.mu.Lock()
	if ci, ok := s.keyCache[hash]; ok && time.Since(ci.at) < s.keyTTL {
		s.mu.Unlock()
		return ci.info, ci.err
	}
	s.mu.Unlock()

	info, err := s.lookupKeyDB(ctx, hash)
	s.mu.Lock()
	// Bounded cache (review: 鉴权缓存无容量清理): on overflow drop everything
	// expired, then everything — correctness unaffected, next hits re-read DB.
	if len(s.keyCache) > 20_000 {
		now := time.Now()
		for h, ci := range s.keyCache {
			if now.Sub(ci.at) >= s.keyTTL {
				delete(s.keyCache, h)
			}
		}
		if len(s.keyCache) > 20_000 {
			s.keyCache = map[string]cacheItem{}
		}
	}
	s.keyCache[hash] = cacheItem{info: info, err: err, at: time.Now()}
	s.mu.Unlock()
	return info, err
}

// LookupKeyFresh bypasses the cache entirely (review P2-10): used for the
// post-audit re-check so permission/concurrency changes made while a request
// waited in the audit queue are honoured immediately.
func (s *Service) LookupKeyFresh(ctx context.Context, presented string) (*KeyInfo, error) {
	if presented == "" {
		return nil, ErrInvalidKey
	}
	return s.lookupKeyDB(ctx, storage.HashToken(presented))
}

// InvalidateKeyCache drops all cached key lookups; admin mutations call this
// so revocations and limit changes take effect without waiting for the TTL.
func (s *Service) InvalidateKeyCache() {
	s.mu.Lock()
	s.keyCache = map[string]cacheItem{}
	s.mu.Unlock()
}

func (s *Service) lookupKeyDB(ctx context.Context, hash string) (*KeyInfo, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT k.id, k.member_id, k.client_id, k.name, k.status, k.expires_at,
		       k.concurrency_limit, k.allowed_models, k.audit_policy_id, m.status
		FROM api_keys k JOIN members m ON m.id = k.member_id
		WHERE k.key_hash = $1`, hash)
	var k KeyInfo
	var clientID, policyID *string
	var allowedModels []string
	if err := row.Scan(&k.ID, &k.MemberID, &clientID, &k.Name, &k.Status, &k.ExpiresAt,
		&k.ConcurrencyLimit, &allowedModels, &policyID, &k.MemberStatus); err != nil {
		return nil, ErrInvalidKey
	}
	k.ClientID = clientID
	k.AuditPolicyID = policyID
	if len(allowedModels) > 0 {
		k.AllowedModels = allowedModels
	}
	switch {
	case k.MemberStatus != "active":
		return nil, ErrMemberDenied
	case k.Status == "revoked":
		return nil, ErrKeyRevoked
	case k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()):
		return nil, ErrKeyExpired
	case k.Status != "active":
		return nil, ErrKeyRevoked
	}
	return &k, nil
}

type Service struct {
	db       *storage.DB
	mu       sync.Mutex
	keyCache map[string]cacheItem
	keyTTL   time.Duration

	loginMu    sync.Mutex
	loginFails map[string][]time.Time
}

type cacheItem struct {
	info *KeyInfo
	err  error
	at   time.Time
}

func NewService(db *storage.DB) *Service {
	return &Service{db: db, keyCache: map[string]cacheItem{}, keyTTL: 5 * time.Second, loginFails: map[string][]time.Time{}}
}

func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// ── Admin sessions ───────────────────────────────────────────────────────────

const sessionCookie = "subai_session"
const sessionTTL = 12 * time.Hour
const maxLoginFails = 10
const loginWindow = 15 * time.Minute
const maxLoginFailBuckets = 10_000

// Login verifies credentials with bcrypt and issues an opaque session token.
// Failed attempts are rate limited per username+IP (§17.2).
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (token string, err error) {
	if !s.allowLogin(username + "|" + ip) {
		return "", errors.New("too many failed logins; try later")
	}
	var memberID, hash string
	var role, status string
	err = s.db.Pool.QueryRow(ctx,
		`SELECT id, COALESCE(password_hash,''), role, status FROM members WHERE name=$1`, username).
		Scan(&memberID, &hash, &role, &status)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || status != "active" || role != "admin" {
		s.recordFail(username + "|" + ip)
		return "", errors.New("invalid credentials")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO admin_sessions(member_id, token_hash, expires_at, ip, user_agent)
		VALUES($1,$2,$3,$4,$5)`, memberID, storage.HashToken(token), time.Now().Add(sessionTTL), ip, userAgent)
	if err != nil {
		return "", err
	}
	s.db.LogAdminEvent(ctx, "system", "admin.login", "member", memberID, map[string]any{"ip": ip}, "")
	return token, nil
}

func (s *Service) allowLogin(id string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	s.pruneLoginFailsLocked(now)
	return len(s.loginFails[id]) < maxLoginFails
}

func (s *Service) recordFail(id string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	s.pruneLoginFailsLocked(now)
	if _, exists := s.loginFails[id]; !exists && len(s.loginFails) >= maxLoginFailBuckets {
		// P2-01: capacity full — evict the oldest bucket by timestamp rather than
		// random map iteration. This prevents attackers from resetting active
		// rate-limits by flooding with random keys.
		var oldestID string
		var oldestTime time.Time
		for bucketID, attempts := range s.loginFails {
			if len(attempts) == 0 {
				continue
			}
			earliest := attempts[0]
			if oldestID == "" || earliest.Before(oldestTime) {
				oldestID = bucketID
				oldestTime = earliest
			}
		}
		if oldestID != "" {
			delete(s.loginFails, oldestID)
		}
	}
	s.loginFails[id] = append(s.loginFails[id], now)
}

func (s *Service) pruneLoginFailsLocked(now time.Time) {
	for id, attempts := range s.loginFails {
		kept := attempts[:0]
		for _, attempt := range attempts {
			if now.Sub(attempt) < loginWindow {
				kept = append(kept, attempt)
			}
		}
		if len(kept) == 0 {
			delete(s.loginFails, id)
		} else {
			s.loginFails[id] = kept
		}
	}
}

// AdminIdentity authenticates an admin request from the session cookie or
// Authorization: Bearer <session token> header.
func (s *Service) AdminIdentity(ctx context.Context, r *http.Request) (memberID string, ok bool) {
	token := Bearer(r)
	if token == "" {
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		return "", false
	}
	var mid string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT member_id FROM admin_sessions
		WHERE token_hash=$1 AND expires_at > now()`, storage.HashToken(token)).Scan(&mid)
	if err != nil {
		return "", false
	}
	var role, status string
	if err := s.db.Pool.QueryRow(ctx, `SELECT role, status FROM members WHERE id=$1`, mid).Scan(&role, &status); err != nil || role != "admin" || status != "active" {
		return "", false
	}
	return mid, true
}

func (s *Service) Logout(ctx context.Context, r *http.Request) {
	token := Bearer(r)
	if token == "" {
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
	}
	if token != "" {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash=$1`, storage.HashToken(token))
	}
}

func SetSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// HashPassword uses bcrypt; no default/factory password exists (§22).
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}
