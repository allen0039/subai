// Package admin implements the management API (§17.2). All routes require
// admin session auth except the login and OAuth callback endpoints. Writes
// use optimistic locking (version field); conflicts return 409. Secrets are
// never echoed back after creation.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"subai/internal/accounts"
	"subai/internal/audit"
	"subai/internal/auth"
	"subai/internal/storage"
)

type Server struct {
	DB       *storage.DB
	Auth     *auth.Service
	OAuth    *accounts.Manager
	Rules    *audit.Engine // shared compiled ruleset for local validation
	Reloader func()        // called after config-changing writes; reloads rules/routes caches
	Ready    ReadyChecker  // SAME gate as the data plane (review P1-2)

	mu         sync.Mutex
	loginFails map[string][]time.Time
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg}})
}

// RequireAdmin wraps a handler with session auth.
func (s *Server) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memberID, ok := s.Auth.AdminIdentity(r.Context(), r)
		if !ok {
			s.writeErr(w, http.StatusUnauthorized, "admin session required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey, memberID)))
	}
}

// notifyMutation triggers cache/rule reloads after ANY config-changing write
// (review round-2: members/keys 等写操作此前未接 Reloader).
func (s *Server) notifyMutation() {
	if s.Reloader != nil {
		s.Reloader()
	}
}

// ReadyChecker is satisfied by gateway.Readiness; keeps admin decoupled.
type ReadyChecker interface {
	Check(ctx context.Context) (ok bool, reasons []string)
}

type ctxKey string

const actorKey ctxKey = "actor"

func actorFrom(r *http.Request) string {
	if v, ok := r.Context().Value(actorKey).(string); ok {
		return v
	}
	return "unknown"
}

// ── Sessions ─────────────────────────────────────────────────────────────────

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			s.writeErr(w, 400, "invalid body")
			return
		}
		ip := clientIP(r)
		token, err := s.Auth.Login(r.Context(), req.Username, req.Password, ip, r.UserAgent())
		if err != nil {
			s.writeErr(w, http.StatusUnauthorized, "invalid credentials or rate limited")
			return
		}
		auth.SetSessionCookie(w, token, 12*3600)
		// The credential is delivered only as an HttpOnly cookie. Returning it
		// in JSON encourages browser storage and defeats that protection.
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodDelete:
		s.Auth.Logout(r.Context(), r)
		auth.SetSessionCookie(w, "", -1)
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodGet:
		mid, ok := s.Auth.AdminIdentity(r.Context(), r)
		if !ok {
			s.writeErr(w, 401, "no session")
			return
		}
		s.writeJSON(w, 200, map[string]string{"member_id": mid})
	default:
		s.writeErr(w, 405, "method not allowed")
	}
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// ── Generic list/read helpers ────────────────────────────────────────────────

// pageParams applies §16.2 pagination defaults (20, max 100) with a sort
// whitelist per table.
func pageParams(r *http.Request, sortWhitelist []string) (limit, offset int, order string) {
	limit = 20
	offset = 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	order = "created_at DESC"
	if s := r.URL.Query().Get("order"); s != "" {
		for _, allowed := range sortWhitelist {
			if s == allowed || s == allowed+" DESC" || s == allowed+" ASC" {
				order = s
				break
			}
		}
	}
	return limit, offset, order
}

func decodeBody[T any](r *http.Request, max int64) (*T, error) {
	var v T
	if err := json.NewDecoder(io.LimitReader(r.Body, max)).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// expectVersion applies optimistic-lock discipline (§17.2: 写接口提交version).
func expectVersion(version int) error {
	if version <= 0 {
		return fmt.Errorf("version required for updates")
	}
	return nil
}

var _ = storage.NowUTC
