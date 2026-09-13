package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"subai/internal/egress"
)

// Upstream is the Codex adapter (D-003). The real endpoint and SSE schema are
// pending P0-01 verification; tests use the synthetic upstream. The gateway
// never forwards the caller's Authorization header — upstream credentials are
// injected here exclusively (§17.1).
type Upstream struct {
	BaseURL string
}

// Credentials is the decrypted account credential envelope.
type Credentials struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	AccountID    string     `json:"account_id"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func (u *Upstream) Dispatch(ctx context.Context, client *http.Client, prof egress.Profile, creds Credentials, payload []byte) (*http.Response, error) {
	url := strings.TrimRight(u.BaseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// Credential injection happens only here (§17.1); never copied from inbound.
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if creds.AccountID != "" {
		req.Header.Set("chatgpt-account-id", creds.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SSEFrame is one parsed server-sent event from upstream (parsed in sse.go).
type SSEFrame struct {
	Event string
	Data  []byte
}

// Usage is the metered usage captured from the upstream stream (§8).
type Usage struct {
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
}

// ExtractUsageFromEvent pulls usage out of a response.completed-style event.
// Returns ok=false when the event carries no usage (kept unknown, §18.3).
// Validates that usage is complete and within valid ranges (review R3-03, R4-06):
// - Event must be a terminal event type (response.completed, response.failed)
// - input_tokens and output_tokens must be present and non-negative
// - cached_tokens must not exceed input_tokens
func ExtractUsageFromEvent(frame SSEFrame) (Usage, bool) {
	// R4-06: Only accept usage from terminal events
	if frame.Event != "response.completed" && frame.Event != "response.failed" {
		return Usage{}, false
	}

	var payload struct {
		Response struct {
			Usage *struct {
				InputTokens        *int64 `json:"input_tokens"`
				OutputTokens       *int64 `json:"output_tokens"`
				InputTokensDetails *struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(frame.Data, &payload); err != nil {
		return Usage{}, false
	}
	u := payload.Response.Usage
	if u == nil {
		return Usage{}, false
	}
	// Required fields must be explicitly present and non-negative (review R3-03).
	// R5-04: API returns total input_tokens including cached portion.
	// billing.Usage.InputTokens expects only uncached count.
	if u.InputTokens == nil || *u.InputTokens < 0 {
		return Usage{}, false
	}
	if u.OutputTokens == nil || *u.OutputTokens < 0 {
		return Usage{}, false
	}
	var cached int64
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
		// R5-05: Validate cached tokens are non-negative and do not exceed total input
		if cached < 0 || cached > *u.InputTokens {
			return Usage{}, false
		}
	}
	// R5-04: Subtract cached from total to get uncached input count
	return Usage{InputTokens: *u.InputTokens - cached, CachedTokens: cached, OutputTokens: *u.OutputTokens}, true
}

// SSEErrorEvent renders a protocol-internal error event for streaming clients
// (§17.1: SSE 需使用协议内错误，不输出伪造成功响应).
func SSEErrorEvent(e *APIError) string {
	return "event: error\ndata: " + string(e.body()) + "\n\n"
}
