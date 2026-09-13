package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"subai/internal/buildinfo"
)

func TestVersionEndpointReportsRunningBuild(t *testing.T) {
	original := buildinfo.Current()
	buildinfo.Version = "1.2.3"
	buildinfo.Revision = "abcdef123456"
	buildinfo.BuiltAt = "2026-09-13T12:00:00Z"
	t.Cleanup(func() {
		buildinfo.Version = original.Version
		buildinfo.Revision = original.Revision
		buildinfo.BuiltAt = original.BuiltAt
	})

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	recorder := httptest.NewRecorder()
	Build(Deps{}).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var got buildinfo.Info
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != buildinfo.Current() {
		t.Fatalf("response = %+v, want %+v", got, buildinfo.Current())
	}
}
