// Package server assembles the full SubAI HTTP surface (data plane, admin
// tree, OAuth callback, health) so both the production entrypoint and the
// integration tests exercise the SAME routing (review P1-6: 测试应启动实际
// 主路由).
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"subai/internal/admin"
	"subai/internal/buildinfo"
	"subai/internal/gateway"
	"subai/internal/storage"
)

// Deps carries the wired collaborators.
type Deps struct {
	DB      *storage.DB
	Gateway *gateway.Server
	Admin   *admin.Server
}

// Build returns the complete HTTP handler.
func Build(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Public runtime build identity. The UI uses the server value so operators
	// can verify which container image is actually serving the request.
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(buildinfo.Current())
	})

	// Data plane (§17.1).
	mux.HandleFunc("/v1/models", d.Gateway.Models)
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			gateway.MethodNotAllowed(w, r, d.Gateway.RequestIDHint())
			return
		}
		d.Gateway.Responses(w, r)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		gateway.UnsupportedError(w, r, d.Gateway.RequestIDHint(), "chat/completions is not supported in this version (D-010)")
	})
	// WebSocket attempts are explicitly unsupported (D-010).
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			gateway.UnsupportedError(w, r, d.Gateway.RequestIDHint(), "websocket is not supported in this version (D-010)")
			return
		}
		http.NotFound(w, r)
	})

	// Admin tree + public OAuth callback on the SAME outer mux
	// (review P1-6: the callback previously lived on an unreachable sub-mux).
	adminMux := d.Admin.Routes()
	mux.Handle("/api/admin/", adminMux)
	mux.HandleFunc("/api/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		d.Admin.OAuthCallback(w, r)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.DB.Pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unavailable"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// Admin UI static build, when present.
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	return securityHeaders(logRequests(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			// Path-only logging: no bodies, no Authorization values (§9).
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
