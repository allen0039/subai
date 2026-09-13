package gateway

import (
	"strings"
	"testing"
)

// Review P1-7 regression: CRLF-framed SSE must parse identically to LF.
func TestScanSSECRLF(t *testing.T) {
	var frames []SSEFrame
	stream := "event: response.created\r\ndata: {\"id\":1}\r\n\r\n" +
		"event: response.completed\r\ndata: {\"id\":2}\r\ndata: {\"more\":true}\r\n\r\n"
	err := scanSSE(strings.NewReader(stream), func(f SSEFrame, raw []byte) error {
		frames = append(frames, f)
		return nil
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	if frames[0].Event != "response.created" || string(frames[0].Data) != `{"id":1}` {
		t.Fatalf("frame0 = %+v", frames[0])
	}
	// multi-line data joined with \n
	if string(frames[1].Data) != `{"id":2}`+"\n"+`{"more":true}` {
		t.Fatalf("multi-line data = %q", frames[1].Data)
	}
}

// Review P1-7: a stream ending without a terminator still yields its frame;
// read errors are surfaced, never silently treated as success.
func TestScanSSETrailingFrameAndErrors(t *testing.T) {
	var frames []SSEFrame
	err := scanSSE(strings.NewReader("data: trailing"), func(f SSEFrame, raw []byte) error {
		frames = append(frames, f)
		return nil
	})
	if err != nil || len(frames) != 1 {
		t.Fatalf("trailing frame: err=%v frames=%d", err, len(frames))
	}
	err = scanSSE(&failReader{}, func(SSEFrame, []byte) error { return nil })
	if err == nil || err.Error() != "boom" {
		t.Fatalf("read error must surface, got %v", err)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errBoom }

var errBoom = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }

// Review P1-7: unbounded frames hit the size cap instead of growing forever.
func TestScanSSEFrameTooLarge(t *testing.T) {
	big := "data: " + strings.Repeat("x", maxSSEFrameSize+1024)
	err := scanSSE(strings.NewReader(big), func(SSEFrame, []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

// Review P1-8: {"results":[{}]} — valid JSON without a flagged verdict — must
// be unavailable, never allow.
func TestParseSSEFrameMultiLineParity(t *testing.T) {
	f, ok := ParseSSEFrame([]byte("event: e\ndata: a\ndata: b"))
	if !ok || f.Event != "e" || string(f.Data) != "a\nb" {
		t.Fatalf("ParseSSEFrame = %+v ok=%v", f, ok)
	}
}
