package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"subai/internal/storage"
)

// TestR5AdminInputValidation tests R5-05: UUID validation on admin endpoints
func TestR5AdminInputValidation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	// Create admin session
	token := "r5-admin-token"
	_, err := e.db.Pool.Exec(ctx, `
		INSERT INTO admin_sessions(member_id, token_hash, expires_at)
		SELECT id, $1, now() + interval '1 hour'
		FROM members LIMIT 1`, storage.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "invalid proxy ID in test endpoint",
			path:       "/api/admin/proxies/not-a-uuid/test",
			wantStatus: 400,
			wantBody:   "invalid proxy ID",
		},
		{
			name:       "invalid proxy ID in patch endpoint",
			path:       "/api/admin/proxies/also-not-uuid",
			wantStatus: 400,
			wantBody:   "invalid proxy ID",
		},
		{
			name:       "invalid event ID in review endpoint",
			path:       "/api/admin/audit/events/malformed-id/review",
			wantStatus: 400,
			wantBody:   "invalid event ID",
		},
		{
			name:       "SQL injection attempt in proxy ID",
			path:       "/api/admin/proxies/" + url.PathEscape("' OR '1'='1") + "/test",
			wantStatus: 400,
			wantBody:   "invalid proxy ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := http.MethodPost
			body := ""
			if strings.HasSuffix(tt.path, "/review") {
				method = http.MethodPost
			}
			if !strings.HasSuffix(tt.path, "/test") && !strings.HasSuffix(tt.path, "/review") {
				method = http.MethodPatch
				body = `{"status":"active","version":1}`
			}
			req := httptest.NewRequest(method, tt.path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)

			rec := httptest.NewRecorder()
			e.admin.Routes().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}
