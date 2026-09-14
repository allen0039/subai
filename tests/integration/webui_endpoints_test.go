package integration

import (
	"net/http"
	"strings"
	"testing"
)

// These endpoints are consumed by the production WebUI. Exercise them
// against the same isolated PostgreSQL stack as the gateway tests so the
// dashboard never silently falls back to fixture data.
func TestWebUIOperationalEndpoints(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)

	// Produce real request, ledger and audit data for the aggregate views.
	if rec := e.post(t, "/v1/responses", e.keyFull, body(true, "dashboard verification")); rec.Code != http.StatusOK {
		t.Fatalf("seed request: status=%d body=%s", rec.Code, rec.Body.String())
	}

	dashboard := adminRequest(t, e, http.MethodGet, "/api/admin/dashboard?range=24h", "")
	if dashboard.Code != http.StatusOK {
		t.Fatalf("dashboard: status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}
	for _, field := range []string{"production_ready", "accounts", "quota", "series"} {
		if !strings.Contains(dashboard.Body.String(), `"`+field+`"`) {
			t.Fatalf("dashboard missing %q: %s", field, dashboard.Body.String())
		}
	}
	if rec := adminRequest(t, e, http.MethodGet, "/api/admin/dashboard?range=30d", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid dashboard range: status=%d body=%s", rec.Code, rec.Body.String())
	}

	search := adminRequest(t, e, http.MethodGet, "/api/admin/search?q=ac", "")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "acctA") {
		t.Fatalf("search: status=%d body=%s", search.Code, search.Body.String())
	}
	// The account is stored with encrypted credentials; search DTOs must never
	// serialize either the ciphertext field or the synthetic upstream token.
	if strings.Contains(search.Body.String(), "credentials") || strings.Contains(search.Body.String(), "up-token-1") {
		t.Fatalf("search leaked a credential: %s", search.Body.String())
	}
	if rec := adminRequest(t, e, http.MethodGet, "/api/admin/search?q=a", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("short search: status=%d body=%s", rec.Code, rec.Body.String())
	}

	events := adminRequest(t, e, http.MethodGet, "/api/admin/events", "")
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"data"`) {
		t.Fatalf("events: status=%d body=%s", events.Code, events.Body.String())
	}

	member := adminRequest(t, e, http.MethodGet, "/api/admin/me/overview", "")
	if member.Code != http.StatusOK {
		t.Fatalf("member overview: status=%d body=%s", member.Code, member.Body.String())
	}
	for _, forbidden := range []string{"accounts", "proxies", "egress", "up-token-1"} {
		if strings.Contains(member.Body.String(), forbidden) {
			t.Fatalf("member overview leaked %q: %s", forbidden, member.Body.String())
		}
	}
}
