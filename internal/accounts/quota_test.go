package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQuotaClientFetch(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "account-1" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		if got := r.Header.Get("Origin"); got != "https://chatgpt.com" {
			t.Errorf("Origin = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usage":
			_, _ = w.Write([]byte(`{
				"plan_type":"pro",
				"rate_limit":{"allowed":true,"limit_reached":false,
					"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":900,"reset_at":1780000000},
					"secondary_window":{"used_percent":60,"limit_window_seconds":604800,"reset_after_seconds":450000,"reset_at":1780400000}}
			}`))
		case "/accounts":
			_, _ = w.Write([]byte(`{"accounts":{"account-1":{"account":{"account_id":"account-1","plan_type":"pro","email":"user@example.com","is_default":true},"entitlement":{"expires_at":"2026-12-31T00:00:00Z"}}}}`))
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &QuotaClient{
		UsageURL:        server.URL + "/usage",
		AccountsURL:     server.URL + "/accounts",
		SubscriptionURL: server.URL + "/subscription",
	}
	got, err := client.Fetch(context.Background(), server.Client(), QuotaCredentials{AccessToken: "access-token", AccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got.PlanType != "pro" || got.Email != "user@example.com" || got.RateLimit == nil {
		t.Fatalf("unexpected snapshot: %#v", got)
	}
	if got.RateLimit.PrimaryWindow == nil || got.RateLimit.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("unexpected primary window: %#v", got.RateLimit.PrimaryWindow)
	}
	if got.RateLimit.SecondaryWindow == nil || got.RateLimit.SecondaryWindow.LimitWindowSeconds != 604800 {
		t.Fatalf("unexpected secondary window: %#v", got.RateLimit.SecondaryWindow)
	}
	wantExpiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if got.SubscriptionExpiresAt == nil || !got.SubscriptionExpiresAt.Equal(wantExpiry) {
		t.Fatalf("subscription expiry = %v", got.SubscriptionExpiresAt)
	}
}

func TestQuotaClientFallsBackToSubscriptionEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usage":
			_, _ = w.Write([]byte(`{"rate_limit":{"allowed":true}}`))
		case "/accounts":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/subscription":
			if got := r.URL.Query().Get("account_id"); got != "account-1" {
				t.Errorf("account_id = %q", got)
			}
			_, _ = w.Write([]byte(`{"plan_type":"plus","active_until":"2027-01-02T03:04:05Z"}`))
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &QuotaClient{UsageURL: server.URL + "/usage", AccountsURL: server.URL + "/accounts", SubscriptionURL: server.URL + "/subscription"}
	got, err := client.Fetch(context.Background(), server.Client(), QuotaCredentials{AccessToken: "token", AccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanType != "plus" || got.SubscriptionExpiresAt == nil {
		t.Fatalf("unexpected fallback snapshot: %#v", got)
	}
}

func TestQuotaClientRejectsUsageFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &QuotaClient{UsageURL: server.URL, AccountsURL: server.URL, SubscriptionURL: server.URL}
	if _, err := client.Fetch(context.Background(), server.Client(), QuotaCredentials{AccessToken: "bad", AccountID: "account-1"}); err == nil {
		t.Fatal("expected usage request to fail")
	}
}

func TestQuotaClientMergesResetCreditDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usage":
			_, _ = w.Write([]byte(`{"rate_limit":{"allowed":true},"rate_limit_reset_credits":{"available_count":1}}`))
		case "/reset-credits":
			_, _ = w.Write([]byte(`{
				"availableCount": 2,
				"items": [
					{"id":"secret-credit-1","status":"available","resetType":"codex_rate_limits","expiresAt":"2026-10-01T00:00:00Z"},
					{"credit_id":"secret-credit-2","status":"available","reset_type":"codex_rate_limits","expires_at":"2026-11-01T00:00:00Z"},
					{"id":"other-credit","status":"available","reset_type":"other","expires_at":"2026-12-01T00:00:00Z"}
				]
			}`))
		case "/accounts", "/subscription":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &QuotaClient{
		UsageURL:        server.URL + "/usage",
		ResetCreditsURL: server.URL + "/reset-credits",
		AccountsURL:     server.URL + "/accounts",
		SubscriptionURL: server.URL + "/subscription",
	}
	got, err := client.Fetch(context.Background(), server.Client(), QuotaCredentials{AccessToken: "token", AccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RateLimitResetCredits == nil || got.RateLimitResetCredits.AvailableCount != 2 || len(got.RateLimitResetCredits.Credits) != 2 {
		t.Fatalf("unexpected reset credits: %#v", got.RateLimitResetCredits)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsAny(string(raw), "secret-credit-1", "secret-credit-2", "other-credit") {
		t.Fatalf("snapshot exposed an upstream credit identifier: %s", raw)
	}
}

func TestQuotaClientKeepsUsageCountWhenResetCreditDetailsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usage":
			_, _ = w.Write([]byte(`{"rate_limit":{"allowed":true},"rate_limit_reset_credits":{"available_count":3}}`))
		case "/reset-credits":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/accounts", "/subscription":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &QuotaClient{
		UsageURL:        server.URL + "/usage",
		ResetCreditsURL: server.URL + "/reset-credits",
		AccountsURL:     server.URL + "/accounts",
		SubscriptionURL: server.URL + "/subscription",
	}
	got, err := client.Fetch(context.Background(), server.Client(), QuotaCredentials{AccessToken: "token", AccountID: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RateLimitResetCredits == nil || got.RateLimitResetCredits.AvailableCount != 3 {
		t.Fatalf("usage reset-credit count was not retained: %#v", got.RateLimitResetCredits)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
