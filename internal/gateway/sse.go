package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SSE parsing (review P1-7): full CRLF/LF handling, multi-line data fields
// joined with \n per the SSE spec, a hard per-frame size cap, and explicit
// reporting of abnormal stream ends. EOF with a pending complete frame still
// dispatches that frame; read errors are never swallowed as success.

const maxSSEFrameSize = 4 << 20 // 4MiB per frame; protects against unbounded buffers

var ErrSSEFrameTooLarge = errors.New("sse frame exceeds size limit")

// scanSSE reads the upstream stream and emits one callback per complete
// frame. `raw` preserves the original bytes so unknown fields survive
// pass-through to the client.
func scanSSE(r io.Reader, onFrame func(SSEFrame, []byte) error) error {
	lr := &sseLineReader{src: r}
	var lines []string
	var raw bytes.Buffer
	flush := func() error {
		if len(lines) == 0 {
			raw.Reset()
			return nil
		}
		frame, ok := parseSSELines(lines)
		lines = lines[:0]
		rawBytes := append([]byte(nil), raw.Bytes()...)
		raw.Reset()
		if !ok {
			return nil // comment-only or empty frame
		}
		if !bytes.HasSuffix(rawBytes, []byte("\n\n")) {
			rawBytes = append(rawBytes, '\n', '\n')
		}
		return onFrame(frame, rawBytes)
	}
	for {
		line, err := lr.nextLine(&raw)
		if err != nil {
			if err == io.EOF {
				if ferr := flush(); ferr != nil {
					return ferr
				}
				return nil
			}
			return err // abnormal end: surfaced, never treated as success
		}
		if raw.Len() > maxSSEFrameSize {
			return fmt.Errorf("%w (> %d bytes)", ErrSSEFrameTooLarge, maxSSEFrameSize)
		}
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		lines = append(lines, line)
	}
}

// parseSSELines interprets one frame's field lines. Multi-line data values
// are joined with "\n" per the SSE spec.
func parseSSELines(lines []string) (SSEFrame, bool) {
	var event string
	var data strings.Builder
	hasData := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, ":"):
			// comment: ignore
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			v = strings.TrimPrefix(v, " ") // single optional leading space
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(v)
			hasData = true
		case strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
			// accepted and ignored: the gateway does not resume upstream streams
		}
	}
	if !hasData && event == "" {
		return SSEFrame{}, false
	}
	return SSEFrame{Event: event, Data: []byte(data.String())}, true
}

// ParseSSEFrame parses a complete frame block (kept for tests/tools).
func ParseSSEFrame(block []byte) (SSEFrame, bool) {
	var lines []string
	for _, raw := range strings.Split(string(block), "\n") {
		lines = append(lines, strings.TrimSuffix(raw, "\r"))
	}
	return parseSSELines(lines)
}

// sseLineReader splits on \n, \r\n or lone \r, preserving the exact consumed
// bytes (terminators included) in raw for pass-through.
type sseLineReader struct {
	src io.Reader
	buf []byte
	eof bool
}

func (s *sseLineReader) nextLine(raw *bytes.Buffer) (string, error) {
	for {
		if idx := bytes.IndexAny(s.buf, "\r\n"); idx >= 0 {
			line := string(s.buf[:idx])
			consumed := idx + 1
			if s.buf[idx] == '\r' {
				if consumed < len(s.buf) {
					if s.buf[consumed] == '\n' {
						consumed++
					}
				} else if !s.eof {
					// \r at the buffer edge: one more byte decides \r\n vs \r
					if err := s.fill(); err != nil {
						return "", err
					}
					if len(s.buf) > consumed && s.buf[consumed] == '\n' {
						consumed++
					}
				}
			}
			raw.Write(s.buf[:consumed])
			s.buf = s.buf[consumed:]
			return line, nil
		}
		if len(s.buf) > maxSSEFrameSize {
			return "", fmt.Errorf("%w during line accumulation", ErrSSEFrameTooLarge)
		}
		if s.eof {
			if len(s.buf) > 0 {
				line := string(s.buf)
				raw.Write(s.buf)
				s.buf = nil
				return line, nil
			}
			return "", io.EOF
		}
		if err := s.fill(); err != nil {
			return "", err
		}
	}
}

func (s *sseLineReader) fill() error {
	tmp := make([]byte, 64<<10)
	n, err := s.src.Read(tmp)
	if n > 0 {
		s.buf = append(s.buf, tmp[:n]...)
	}
	if err == io.EOF {
		s.eof = true
		return nil
	}
	return err
}
