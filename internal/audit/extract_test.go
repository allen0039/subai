package audit

import (
	"strings"
	"testing"
)

// TestExtractResponsesFullCoverage verifies §21 extraction surfaces.
func TestExtractResponsesFullCoverage(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5-codex",
		"instructions": "be helpful",
		"input": [
			{"type":"message","role":"user","content":"hello world"},
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"second part"},
				{"type":"input_image","image_url":"https://example.test/x.png"}
			]},
			{"type":"function_call","name":"run_shell","arguments":"{\"command\":\"ls -la\"}"},
			{"type":"function_call_output","output":"{\"stdout\":\"total 0\",\"note\":\"plain text inside json\"}"}
		],
		"tools":[{"type":"function","name":"run_shell","description":"run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string","description":"the command"}}}}]
	}`)
	doc, err := ExtractResponses(body, true)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Coverage != CoverageFull {
		t.Fatalf("expected full_visible, got %s (%v)", doc.Coverage, doc.MissingParts)
	}
	joined := ""
	for _, s := range doc.Segments {
		joined += s.Text + "\n"
	}
	for _, want := range []string{"be helpful", "hello world", "second part", "ls -la", "plain text inside json", "run a shell command", "the command"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extraction missing %q", want)
		}
	}
	hasImage := false
	for _, s := range doc.Segments {
		if s.Type == "image_ref" && s.ImageRef == "https://example.test/x.png" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Errorf("image ref not recorded")
	}
}

// TestExtractResponsesUnsupportedHistory enforces §6.1: invisible context
// cannot be presented as fully audited in strict mode.
func TestExtractResponsesUnsupportedHistory(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"hi","previous_response_id":"resp_123"}`)
	doc, err := ExtractResponses(body, true)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if doc.Coverage != CoverageUnsupported {
		t.Fatalf("expected unsupported coverage, got %s", doc.Coverage)
	}
	if len(doc.MissingParts) == 0 {
		t.Fatalf("missing parts must be recorded")
	}
}

// TestJSONDepthLimit enforces FORMAT-002 structural bounds.
func TestJSONDepthLimit(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100)
	if err := CheckJSONDepth([]byte(deep), 64, 20000); err == nil {
		t.Fatalf("expected depth error")
	}
	okBody := `{"model":"gpt-5","input":[1,2,3],"nested":{"x":{"y":"z"}}}`
	if err := CheckJSONDepth([]byte(okBody), 64, 20000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestUnsupportedInputItemType enforces strict rejection of unknown types (§21).
func TestUnsupportedInputItemType(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"mystery_blob","data":"x"}]}`)
	_, err := ExtractResponses(body, true)
	if err == nil {
		t.Fatalf("strict mode must reject unknown input item types")
	}
	if !strings.Contains(err.Error(), "mystery_blob") {
		t.Fatalf("error should name the unsupported type: %v", err)
	}
}
