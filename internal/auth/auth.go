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
	ID                string
	MemberID          string
	MemberStatus      string
	ClientID          *string
	SubscriptionID    string
	SubscriptionLimit int
	Name              string
	Status            string
	ExpiresAt         *time.Time
	ConcurrencyLimit  int
	AllowedModels     []string // nil = all
	AuditPolicyID     *string
}

// Identity is the authenticated interactive-console principal. It is separate
// from KeyInfo: browser users act as themselves, while data-plane requests act
// through an API key bound to a member/subscription.
type Identity struct {
	MemberID string
	Name     string
	Role     string
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
		       k.concurrency_limit, k.allowed_models, k.audit_policy_id, m.status,
		       k.user_subscription_id::text, us.status, us.starts_at, us.expires_at,
		       COALESCE(us.concurrency_override,pv.concurrency_limit),
		       COALESCE(us.allowed_models_override,pv.allowed_models)
		FROM api_keys k JOIN members m ON m.id = k.member_id
		LEFT JOIN user_subscriptions us ON us.id=k.user_subscription_id AND us.member_id=k.member_id
		LEFT JOIN plan_versions pv ON pv.id=us.plan_version_id
		WHERE k.key_hash = $1`, hash)
	var k KeyInfo
	var clientID, policyID, subscriptionID, subscriptionStatus *string
	var subscriptionStarts, subscriptionExpires *time.Time
	var subscriptionLimit *int
	var allowedModels, subscriptionModels []string
	if err := row.Scan(&k.ID, &k.MemberID, &clientID, &k.Name, &k.Status, &k.ExpiresAt,
		&k.ConcurrencyLimit, &allowedModels, &policyID, &k.MemberStatus,
		&subscriptionID, &subscriptionStatus, &subscriptionStarts, &subscriptionExpires, &subscriptionLimit, &subscriptionModels); err != nil {
		return nil, ErrInvalidKey
	}
	k.ClientID = clientID
	k.AuditPolicyID = policyID
	if subscriptionID != nil {
		if subscriptionStatus == nil || subscriptionStarts == nil || subscriptionExpires == nil || subscriptionLimit == nil || *subscriptionStatus != "active" || time.Now().Before(*subscriptionStarts) || !time.Now().Before(*subscriptionExpires) {
			return nil, ErrMemberDenied
		}
		k.SubscriptionID = *subscriptionID
		k.SubscriptionLimit = *subscriptionLimit
		if k.ConcurrencyLimit > *subscriptionLimit {
			k.ConcurrencyLimit = *subscriptionLimit
		}
		// A subscription model list is a hard ceiling. A Key may narrow it but
		// never expand it, even if a stale admin UI tried to write a wider list.
		if subscriptionModels != nil {
			if len(allowedModels) == 0 {
				allowedModels = subscriptionModels
			} else if !modelsSubset(allowedModels, subscriptionModels) {
				return nil, ErrInvalidKey
			}
		}
	}
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

func modelsSubset(candidate, allowed []string) bool {
	for _, model := range candidate {
		found := false
		for _, permitted := range allowed {
			if model == permitted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
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
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || status != "active" {
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
	s.db.LogAdminEvent(ctx, memberID, "member.login", "member", memberID, map[string]any{"ip": ip, "role": role}, "")
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

func sessionToken(r *http.Request) string {
	token := Bearer(r)
	if token == "" {
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
	}
	return token
}

// Identity authenticates any active console user from the session cookie or
// Authorization bearer token. Role is looked up on every request, so a user
// disablement or role change takes effect immediately.
func (s *Service) Identity(ctx context.Context, r *http.Request) (Identity, bool) {
	token := sessionToken(r)
	if token == "" {
		return Identity{}, false
	}
	var mid string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT member_id FROM admin_sessions
		WHERE token_hash=$1 AND expires_at > now()`, storage.HashToken(token)).Scan(&mid)
	if err != nil {
		return Identity{}, false
	}
	var identity Identity
	var status string
	identity.MemberID = mid
	if err := s.db.Pool.QueryRow(ctx, `SELECT name, role, status FROM members WHERE id=$1`, mid).Scan(&identity.Name, &identity.Role, &status); err != nil || status != "active" {
		return Identity{}, false
	}
	return identity, true
}

// AdminIdentity preserves the existing admin-only API contract.
func (s *Service) AdminIdentity(ctx context.Context, r *http.Request) (memberID string, ok bool) {
	identity, ok := s.Identity(ctx, r)
	if !ok || identity.Role != "admin" {
		return "", false
	}
	return identity.MemberID, true
}

func (s *Service) Logout(ctx context.Context, r *http.Request) {
	token := sessionToken(r)
	if token != "" {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash=$1`, storage.HashToken(token))
	}
}

// RevokeMemberSessions invalidates all interactive sessions for a member.
// Password resets use it so a stolen browser cookie cannot survive the reset.
func (s *Service) RevokeMemberSessions(ctx context.Context, memberID string) error {
	_, err := s.db.Pool.Exec(ctx, `DELETE FROM admin_sessions WHERE member_id=$1`, memberID)
	return err
}

// RevokeOtherMemberSessions keeps the caller's current session alive while
// invalidating all other sessions belonging to the same member.
func (s *Service) RevokeOtherMemberSessions(ctx context.Context, r *http.Request, memberID string) error {
	token := sessionToken(r)
	if token == "" {
		return s.RevokeMemberSessions(ctx, memberID)
	}
	_, err := s.db.Pool.Exec(ctx,
		`DELETE FROM admin_sessions WHERE member_id=$1 AND token_hash<>$2`, memberID, storage.HashToken(token))
	return err
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
