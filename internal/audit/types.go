// Package audit implements the pre-call moderation pipeline: input extraction
// into a coverage-aware AuditDocument (§21), the local declarative rule engine
// (§4, §20), the official OpenAI Moderation adapter (§5), the HMAC cache
// (§6.2) and the fair queue (§6.3).
package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// ExceptionLookup checks an exact, unexpired review exception. It is injected
// by the gateway to keep this package independent of the storage layer.
type ExceptionLookup func(ctx context.Context, keyID string, contentHMAC []byte, ruleID string) (bool, error)

// Segment is one auditable piece of the request (§21).
type Segment struct {
	Role     string // user|assistant|system|tool|developer|""
	Source   string // input|instructions|tool_definition|tool_result|quoted_text|code
	Type     string // text|json|image_ref
	Text     string
	ImageRef string
	Position int
}

// Coverage states what was actually auditable (§21). full_conversation is
// deliberately not a state: the gateway can never prove conversation-wide
// visibility.
const (
	CoverageFull        = "full_visible"
	CoveragePartial     = "partial"
	CoverageUnsupported = "unsupported"
)

type AuditDocument struct {
	Segments     []Segment
	Coverage     string
	MissingParts []string
	ContentHMAC  []byte
}

// HMACScope binds cache and ledger identity to the key scope, rule versions
// and moderation config (§6.2). Uses unambiguous encoding with length prefixes
// to prevent collision attacks (P1-01).
func ContentHMAC(key []byte, scope string, segments []Segment) []byte {
	mac := hmac.New(sha256.New, key)
	writeField := func(data string) {
		length := uint32(len(data))
		mac.Write([]byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)})
		mac.Write([]byte(data))
	}
	writeField(scope)
	for _, s := range segments {
		writeField(s.Role)
		writeField(s.Source)
		writeField(s.Type)
		writeField(s.Text)
		writeField(s.ImageRef)
	}
	return mac.Sum(nil)
}

func HexID(b []byte) string { return hex.EncodeToString(b[:8]) }

// Decision is the unified adapter outcome (§5).
type Decision string

const (
	DecisionAllow       Decision = "allow"
	DecisionBlock       Decision = "block"
	DecisionReview      Decision = "review"
	DecisionFlag        Decision = "flag"
	DecisionUnavailable Decision = "unavailable"
	DecisionUnsupported Decision = "unsupported"
)

// Result is the pipeline output consumed by the gateway.
type Result struct {
	Decision      Decision
	Coverage      string
	MissingParts  []string
	Hits          []RuleHit
	Categories    map[string]any // official categories+scores, kept for audit events (§9)
	ModelVersion  string
	PolicyVersion int64
	RuleVersion   string
	CacheState    string // miss|hit
	DurationMS    int64
	ErrorType     string
	Summary       string // sanitized digest only (§9)
	SecretBlocked bool   // true when a secret hit prevented any external transmission (§4)
	ContentHMAC   []byte // exact normalized content fingerprint for review exceptions
}

type RuleHit struct {
	RuleID        string   `json:"rule_id"`
	Category      string   `json:"category"`
	Action        string   `json:"action"`
	Severity      string   `json:"severity"`
	Message       string   `json:"message"`
	Ranges        [][2]int `json:"ranges,omitempty"` // positions in the normalized segment text
	MaskedExample string   `json:"masked_sample,omitempty"`
}
