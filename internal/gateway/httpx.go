package gateway

import (
	"net/http"
)

// MethodNotAllowed renders a uniform 405.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request, rid string) {
	(&APIError{HTTPStatus: 405, Code: "method_not_allowed", Message: "method not allowed", RequestID: rid}).write(w)
}

// UnsupportedError renders the §17.1 unsupported-transport mapping (D-010).
func UnsupportedError(w http.ResponseWriter, r *http.Request, rid, detail string) {
	errUnsupportedTransport(rid, detail).write(w)
}

// JSONError is used by integration tests to render gateway errors uniformly.
func JSONError(w http.ResponseWriter, e *APIError) { e.write(w) }
