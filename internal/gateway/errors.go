// Package gateway implements the data plane: /v1/responses and /v1/models,
// the request state machine (§19) and the protocol error mapping (§17.1).
package gateway

import (
	"encoding/json"
	"net/http"
)

// APIError is the unified error body (§17.1): error.code, error.message,
// request_id, retryable, optional retry_after_seconds / reset_at.
type APIError struct {
	HTTPStatus int
	Code       string
	Message    string
	RequestID  string
	Retryable  bool
	RetryAfter *int
	ResetAt    *string
}

func (e *APIError) body() []byte {
	inner := map[string]any{
		"code":       e.Code,
		"message":    e.Message,
		"request_id": e.RequestID,
		"retryable":  e.Retryable,
	}
	if e.RetryAfter != nil {
		inner["retry_after_seconds"] = *e.RetryAfter
	}
	if e.ResetAt != nil {
		inner["reset_at"] = *e.ResetAt
	}
	b, _ := json.Marshal(map[string]any{"error": inner})
	return b
}

func (e *APIError) write(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Request-Id", e.RequestID)
	if e.RetryAfter != nil {
		h.Set("Retry-After", itoa(*e.RetryAfter))
	}
	w.WriteHeader(e.HTTPStatus)
	_, _ = w.Write(e.body())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Error constructors per the §17.1 mapping table.
func errInvalidKey(rid string) *APIError {
	return &APIError{401, "invalid_api_key", "invalid, expired or revoked gateway key", rid, false, nil, nil}
}
func errAccessDenied(rid, msg string) *APIError {
	return &APIError{403, "access_denied", msg, rid, false, nil, nil}
}
func errAuditBlocked(rid, msg string) *APIError {
	return &APIError{400, "audit_blocked", msg, rid, false, nil, nil}
}
func errAuditUnsupported(rid, msg string) *APIError {
	return &APIError{422, "audit_unsupported", msg, rid, false, nil, nil}
}
func errQueueFull(rid string, retryAfter int) *APIError {
	return &APIError{429, "audit_queue_full", "moderation queue is full; retry later", rid, true, &retryAfter, nil}
}
func errConcurrency(rid string, retryAfter int) *APIError {
	return &APIError{429, "concurrency_exceeded", "concurrency limit reached; retry later", rid, true, &retryAfter, nil}
}
func errBudget(rid, resetAt string) *APIError {
	return &APIError{429, "budget_exceeded", "budget exhausted for the current period", rid, false, nil, &resetAt}
}
func errAuditUnavailable(rid string) *APIError {
	return &APIError{503, "audit_unavailable", "moderation service unavailable; request not processed", rid, true, nil, nil}
}
func errNoHealthyAccount(rid string) *APIError {
	return &APIError{503, "no_healthy_account", "no upstream account is currently available", rid, true, nil, nil}
}
func errUpstream(rid, msg string) *APIError {
	return &APIError{502, "upstream_error", msg, rid, true, nil, nil}
}
func errBodyTooLarge(rid string) *APIError {
	return &APIError{413, "request_too_large", "request body exceeds the configured limit", rid, false, nil, nil}
}
func errUnsupportedTransport(rid, detail string) *APIError {
	return &APIError{422, "unsupported_transport", detail, rid, false, nil, nil}
}
