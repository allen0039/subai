package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"subai/internal/egress"
)

func TestDispatchOpenAICompatibleRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer relay-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "" {
			t.Errorf("relay must not receive ChatGPT account id, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := (&Upstream{}).Dispatch(context.Background(), server.Client(), egress.Profile{}, "openai_compatible", server.URL+"/v1", Credentials{AccessToken: "relay-key", AccountID: "must-not-leak"}, []byte(`{"stream":true}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	resp.Body.Close()
}

func TestDispatchCodexKeepsNativeAccountHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "chatgpt-account" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := (&Upstream{}).Dispatch(context.Background(), server.Client(), egress.Profile{}, "codex", server.URL+"/backend-api/codex", Credentials{AccessToken: "oauth-token", AccountID: "chatgpt-account"}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	resp.Body.Close()
}

func TestResponsesEndpointAcceptsExplicitResponsesURL(t *testing.T) {
	if got := responsesEndpoint("https://relay.example/v1/responses/"); got != "https://relay.example/v1/responses" {
		t.Fatalf("endpoint = %q", got)
	}
}
