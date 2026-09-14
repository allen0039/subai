package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestModelClientFetchCodexModelsUsesManifestAndFiltersNonGPT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("chatgpt-account-id") != "account" {
			t.Fatal("missing Codex credentials")
		}
		if r.Header.Get("openai-beta") != "codex-1" || r.Header.Get("originator") != "codex-tui" || r.URL.Query().Get("client_version") != "0.146.0" || r.Header.Get("Version") != "0.146.0" {
			t.Fatal("missing official Codex client context")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5-codex"},{"id":"gpt-5"},{"model":"omni-moderation-latest"},{"slug":"claude-test"}]}`))
	}))
	defer server.Close()

	client := &ModelClient{CodexModelsURL: server.URL}
	got, err := client.FetchCodexModels(context.Background(), server.Client(), QuotaCredentials{AccessToken: "token", AccountID: "account"})
	if err != nil {
		t.Fatalf("FetchCodexModels: %v", err)
	}
	want := []string{"gpt-5", "gpt-5-codex"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

func TestModelClientFetchCodexModelsAcceptsOpenAICompatibleData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6"},{"id":"not-a-gpt-model"}]}`))
	}))
	defer server.Close()

	client := &ModelClient{CodexModelsURL: server.URL}
	got, err := client.FetchCodexModels(context.Background(), server.Client(), QuotaCredentials{AccessToken: "token", AccountID: "account"})
	if err != nil {
		t.Fatalf("FetchCodexModels: %v", err)
	}
	want := []string{"gpt-5.6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}
