package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ModerationClient is the official adapter (§5). It outputs the unified
// Decision set and never lets downstream-controlled URLs or headers redirect
// the audit target (§5: 审核出口固定为配置的官方端点).
type ModerationClient struct {
	Endpoint string
	Model    string
	APIKey   string
	HTTP     *http.Client

	CallTimeout time.Duration
	Retries     int
	TotalBudget time.Duration

	// ImageSupport records P0-verified capability; until verified the adapter
	// reports unsupported for image inputs instead of trusting zero scores (§5).
	ImageSupport bool
}

type moderationRequest struct {
	Model string           `json:"model"`
	Input []moderationText `json:"input"`
}

type moderationText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var ErrModerationUnavailable = errors.New("moderation unavailable")

// ModerationOutcome is the parsed official response.
type ModerationOutcome struct {
	Flagged    bool
	Categories map[string]any
}

// Check sends the segments for classification. unavailable on 429/5xx/network
// after bounded retries with Retry-After; unsupported when inputs fall outside
// the verified capability set (§5). Timeouts never become "allow" (§6.3).
func (m *ModerationClient) Check(ctx context.Context, doc *AuditDocument) (*ModerationOutcome, Decision, string, error) {
	if doc == nil || len(doc.Segments) == 0 {
		return nil, DecisionUnsupported, "no_auditable_input", fmt.Errorf("%w: empty audit document", ErrUnsupportedInput)
	}
	var reqTexts []moderationText
	for _, seg := range doc.Segments {
		switch seg.Type {
		case "text", "json":
			reqTexts = append(reqTexts, moderationText{Type: "text", Text: seg.Text})
		case "image_ref":
			if !m.ImageSupport {
				return nil, DecisionUnsupported, "image_not_verified", fmt.Errorf("%w: image moderation not verified for this deployment", ErrUnsupportedInput)
			}
			// image inputs would be encoded here once P0-01/03 verifies formats
		}
	}
	if len(reqTexts) == 0 {
		return nil, DecisionUnsupported, "no_text_input", fmt.Errorf("%w: no text input", ErrUnsupportedInput)
	}

	body, _ := json.Marshal(moderationRequest{Model: m.Model, Input: reqTexts})
	deadline := time.Now().Add(m.TotalBudget)
	attempts := m.Retries + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		if time.Now().After(deadline) {
			break
		}
		out, decision, errType, err := m.callOnce(ctx, body)
		if err == nil {
			return out, decision, errType, nil
		}
		lastErr = err
		var ra retryAfter
		if errors.As(err, &ra) {
			wait := ra.delay
			if wait > deadline.Sub(time.Now()) {
				break
			}
			if wait > 0 {
				select {
				case <-ctx.Done():
					return nil, DecisionUnavailable, "client_cancelled", ctx.Err()
				case <-time.After(wait):
				}
			}
			continue
		}
		if !errors.Is(err, ErrModerationUnavailable) {
			break // non-retryable
		}
	}
	return nil, DecisionUnavailable, "moderation_unavailable", lastErr
}

type retryAfter struct{ delay time.Duration }

func (r retryAfter) Error() string { return "retry after " + r.delay.String() }

func (m *ModerationClient) callOnce(ctx context.Context, body []byte) (*ModerationOutcome, Decision, string, error) {
	callCtx, cancel := context.WithTimeout(ctx, m.CallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, m.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, DecisionUnavailable, "request_build", err
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, DecisionUnavailable, "network", fmt.Errorf("%w: %v", ErrModerationUnavailable, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 4<<20)
	switch {
	case resp.StatusCode == http.StatusOK:
	default:
		io.Copy(io.Discard, limited)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || resp.StatusCode == 408 {
			err := fmt.Errorf("%w: status %d", ErrModerationUnavailable, resp.StatusCode)
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, perr := strconv.Atoi(ra); perr == nil && secs >= 0 {
					err = retryAfter{delay: time.Duration(secs) * time.Second}
				}
			}
			return nil, DecisionUnavailable, fmt.Sprintf("http_%d", resp.StatusCode), err
		}
		return nil, DecisionUnavailable, fmt.Sprintf("http_%d", resp.StatusCode), fmt.Errorf("moderation returned status %d", resp.StatusCode)
	}
	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Results []struct {
			// Flagged is a pointer: a MISSING field is distinguishable from a
			// real `false` (review P1-8 — absent verdicts must never become allow).
			Flagged        *bool              `json:"flagged"`
			Categories     map[string]bool    `json:"categories"`
			CategoryScores map[string]float64 `json:"category_scores"`
		} `json:"results"`
	}
	dec := json.NewDecoder(limited)
	if err := dec.Decode(&out); err != nil {
		// invalid JSON is a provider-side failure, never a pass (§11 故障用例)
		return nil, DecisionUnavailable, "invalid_json", fmt.Errorf("%w: invalid moderation JSON: %v", ErrModerationUnavailable, err)
	}
	if len(out.Results) != 1 {
		return nil, DecisionUnavailable, "unexpected_results_count",
			fmt.Errorf("%w: moderation returned %d results, want exactly 1", ErrModerationUnavailable, len(out.Results))
	}
	r := out.Results[0]
	if r.Flagged == nil {
		// {"results":[{}]} or a truncated schema: stop the request —
		// "合法 JSON"不等于"有效审核决定" (review P1-8).
		return nil, DecisionUnavailable, "missing_flagged_field",
			fmt.Errorf("%w: moderation result missing flagged verdict", ErrModerationUnavailable)
	}
	cats := map[string]any{}
	for k, v := range r.Categories {
		cats[k] = v
	}
	for k, v := range r.CategoryScores {
		cats["score:"+k] = v
	}
	return &ModerationOutcome{
		Flagged:    *r.Flagged,
		Categories: cats,
	}, decisionFromFlag(*r.Flagged), "", nil
}

func decisionFromFlag(flagged bool) Decision {
	if flagged {
		return DecisionBlock
	}
	return DecisionAllow
}

// ModelName reports the applied moderation model for audit events (§9).
func ModelName(respModel string, configured string) string {
	if strings.TrimSpace(respModel) != "" {
		return respModel
	}
	return configured
}
