package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Rule is the declarative rule schema (§20). Only declarative matchers are
// supported; arbitrary executable scripts are rejected by design (§4).
type Rule struct {
	ID          string   `yaml:"id" json:"rule_id"`
	Version     int      `yaml:"version" json:"version"`
	Category    string   `yaml:"category" json:"category"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Scope       []string `yaml:"scope" json:"scope"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	Action      string   `yaml:"action" json:"action"`
	Severity    string   `yaml:"severity" json:"severity"`
	Message     string   `yaml:"message" json:"message"`
	FixtureSet  string   `yaml:"fixture_set" json:"fixture_set"`
	Matcher     Matcher  `yaml:"matcher" json:"matcher"`
}

type Matcher struct {
	Type      string     `yaml:"type" json:"type"`
	Pattern   string     `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	All       [][]string `yaml:"all,omitempty" json:"all,omitempty"`           // groups whose every term must appear
	Any       [][]string `yaml:"any,omitempty" json:"any,omitempty"`           // groups where one term suffices
	Distance  int        `yaml:"distance,omitempty" json:"distance,omitempty"` // max runes between matched groups
	URI       []string   `yaml:"uri,omitempty" json:"uri,omitempty"`
	Sensitive []string   `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	Senders   []string   `yaml:"senders,omitempty" json:"senders,omitempty"`
}

// Ruleset is an immutable compiled snapshot; reload swaps the pointer.
type Ruleset struct {
	Version   string // content hash, part of cache keys (§6.2)
	PolicyVer int64
	Rules     []*compiledRule
}

type compiledRule struct {
	def     Rule
	regex   *regexp.Regexp
	groups  [][]string
	phrases map[string]bool
}

// Compile validates and precompiles patterns with the restricted stdlib
// regexp engine (§20: 有运行时间保障的正则实现). Go regexp guarantees linear
// time (RE2), which bounds catastrophic backtracking.
func Compile(rules []Rule, policyVer int64) (*Ruleset, error) {
	rs := &Ruleset{PolicyVer: policyVer}
	h := sha256.New()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if err := validateRule(r); err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		cr := &compiledRule{def: r}
		switch r.Matcher.Type {
		case "regex", "pem_private_key", "connection_string", "access_key", "injection_override":
			if r.Matcher.Pattern != "" {
				if len(r.Matcher.Pattern) > 2048 {
					return nil, fmt.Errorf("rule %s: pattern too long", r.ID)
				}
				re, err := regexp.Compile(r.Matcher.Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %s: %w", r.ID, err)
				}
				cr.regex = re
			}
		}
		if len(r.Matcher.All) > 0 || len(r.Matcher.Any) > 0 {
			cr.groups = append(r.Matcher.All, r.Matcher.Any...)
			cr.phrases = map[string]bool{}
			for _, g := range r.Matcher.All {
				for _, term := range g {
					cr.phrases[strings.ToLower(term)] = true
				}
			}
			if r.Matcher.Distance == 0 {
				r.Matcher.Distance = 400
			}
		}
		rs.Rules = append(rs.Rules, cr)
		hashLine(h, r)
	}
	rs.Version = hex.EncodeToString(h.Sum(nil))[:16]
	return rs, nil
}

func hashLine(h interface{ Write([]byte) (int, error) }, r Rule) {
	fmt.Fprintf(h, "%s|%d|%s|%t|%s|%v|%v\n", r.ID, r.Version, r.Action, r.Enabled, r.Matcher.Type, r.Matcher.All, r.Matcher.Any)
}

func validateRule(r Rule) error {
	switch r.Category {
	case "secret", "content", "injection", "exfil", "format":
	default:
		return fmt.Errorf("unknown category %q", r.Category)
	}
	switch r.Action {
	case "flag", "review", "block", "reject", "unsupported":
	default:
		return fmt.Errorf("unknown action %q", r.Action)
	}
	switch r.Matcher.Type {
	case "pem_private_key", "secret_file", "connection_string", "access_key", "keyword_combo",
		"injection_override", "exfil_combo", "regex", "structural":
	default:
		return fmt.Errorf("unknown matcher type %q", r.Matcher.Type)
	}
	return nil
}

// Engine evaluates a ruleset against a document. It always operates on the
// normalized copy — case-folded, NFKC-lite, zero-width stripped — while the
// original request bytes stay untouched (§4).
type Engine struct {
	mu     sync.RWMutex
	active *Ruleset
}

func NewEngine(rs *Ruleset) *Engine { return &Engine{active: rs} }

func (e *Engine) Ruleset() *Ruleset {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.active
}

func (e *Engine) Swap(rs *Ruleset) {
	e.mu.Lock()
	e.active = rs
	e.mu.Unlock()
}

// RunLocal executes local rules. Secrets are evaluated before any external
// call (§4); when a secret rule hits, result.SecretBlocked is set and the
// pipeline must not contact the moderation API with the content.
func (e *Engine) RunLocal(doc *AuditDocument) (hits []RuleHit, secretBlocked bool) {
	rs := e.Ruleset()
	if rs == nil {
		return nil, false
	}
	var secretHits []RuleHit
	var otherHits []RuleHit
	for _, seg := range doc.Segments {
		if seg.Type == "image_ref" {
			continue // textual rules do not apply to image refs
		}
		norm := Normalize(seg.Text)
		for _, cr := range rs.Rules {
			if !scopeApplies(cr.def.Scope, seg) {
				continue
			}
			ranges, masked, ok := cr.match(norm)
			if !ok {
				continue
			}
			hit := RuleHit{
				RuleID: cr.def.ID, Category: cr.def.Category, Action: cr.def.Action,
				Severity: cr.def.Severity, Message: cr.def.Message, Ranges: ranges, MaskedExample: masked,
			}
			if cr.def.Category == "secret" {
				secretHits = append(secretHits, hit)
			} else {
				otherHits = append(otherHits, hit)
			}
		}
	}
	for _, h := range secretHits {
		if h.Action == "block" || h.Action == "review" {
			secretBlocked = true
		}
	}
	return append(secretHits, otherHits...), secretBlocked
}

func scopeApplies(scope []string, seg Segment) bool {
	if len(scope) == 0 {
		return true
	}
	for _, s := range scope {
		if s == seg.Source {
			return true
		}
	}
	return false
}

// match returns hit ranges in the normalized text plus a masked example that
// never exposes the full secret value (§9, §20). "structural" rules are
// enforced by the gateway pipeline (body size, JSON depth, unsupported input)
// and never match here.
func (cr *compiledRule) match(norm string) ([][2]int, string, bool) {
	if cr.def.Matcher.Type == "structural" {
		return nil, "", false
	}
	switch cr.def.Matcher.Type {
	case "pem_private_key", "connection_string", "access_key", "injection_override", "regex":
		if cr.regex == nil {
			return nil, "", false
		}
		return cr.matchRegex(norm)
	case "secret_file", "keyword_combo", "exfil_combo":
		return cr.matchCombo(norm)
	}
	return nil, "", false
}

func (cr *compiledRule) matchRegex(norm string) ([][2]int, string, bool) {
	switch cr.def.Matcher.Type {
	case "pem_private_key":
		return cr.matchPEM(norm)
	case "connection_string":
		return cr.matchConnString(norm)
	case "access_key":
		return cr.matchAccessKey(norm)
	default:
		locs := toPairs(cr.regex.FindAllStringIndex(norm, 5))
		if len(locs) == 0 {
			return nil, "", false
		}
		return locs, maskAt(norm, locs[0]), true
	}
}

func toPairs(locs [][]int) [][2]int {
	out := make([][2]int, 0, len(locs))
	for _, l := range locs {
		out = append(out, [2]int{l[0], l[1]})
	}
	return out
}

func (cr *compiledRule) matchPEM(norm string) ([][2]int, string, bool) {
	if cr.regex == nil {
		return nil, "", false
	}
	locs := toPairs(cr.regex.FindAllStringIndex(norm, 1))
	if len(locs) == 0 {
		return nil, "", false
	}
	segText := norm[locs[0][0]:locs[0][1]]
	// payload must be real-looking: reject placeholder keys (XXXX, <...>, ...)
	body := strings.TrimSpace(segText)
	if placeholders.MatchString(body) {
		return nil, "", false
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 3 { // header + ≥1 payload line + footer
		return nil, "", false
	}
	return locs, maskAt(norm, locs[0]), true
}

var placeholders = regexp.MustCompile(`(?i)(x{8,}|your[_-]?key|placeholder|<[^>]{3,}>|\$\{[^}]{1,64}\}|\.\.\.)`)

func (cr *compiledRule) matchConnString(norm string) ([][2]int, string, bool) {
	if cr.regex == nil {
		return nil, "", false
	}
	for _, loc := range toPairs(cr.regex.FindAllStringIndex(norm, 8)) {
		cred := norm[loc[0]:loc[1]]
		if placeholders.MatchString(cred) {
			continue // ${PASSWORD}, <password>, ... are templates, still sent to moderation (§20)
		}
		return [][2]int{loc}, maskAt(norm, loc), true
	}
	return nil, "", false
}

func (cr *compiledRule) matchAccessKey(norm string) ([][2]int, string, bool) {
	if cr.regex == nil {
		return nil, "", false
	}
	for _, loc := range toPairs(cr.regex.FindAllStringIndex(norm, 8)) {
		tok := norm[loc[0]:loc[1]]
		if placeholders.MatchString(tok) {
			continue
		}
		return [][2]int{loc}, maskAt(norm, loc), true
	}
	return nil, "", false
}

// matchCombo evaluates all/any term groups with an inter-group distance bound.
func (cr *compiledRule) matchCombo(norm string) ([][2]int, string, bool) {
	if len(cr.groups) == 0 {
		return nil, "", false
	}
	distance := cr.def.Matcher.Distance
	if distance <= 0 {
		distance = 400
	}
	// Position each group's earliest hit.
	pos := make([]int, len(cr.groups))
	for i, g := range cr.groups {
		best := -1
		for _, term := range g {
			if p := strings.Index(norm, strings.ToLower(term)); p >= 0 && (best == -1 || p < best) {
				best = p
			}
		}
		if best == -1 {
			return nil, "", false
		}
		pos[i] = best
	}
	lo, hi := pos[0], pos[0]
	for _, p := range pos[1:] {
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}
	if hi-lo > distance {
		return nil, "", false
	}
	return [][2]int{{lo, hi + 24}}, maskAt(norm, [2]int{lo, hi + 24}), true
}

// maskAt masks everything but the first/last 4 runes of the hit.
func maskAt(text string, loc [2]int) string {
	if loc[1] > len(text) {
		loc[1] = len(text)
	}
	runes := []rune(text[loc[0]:loc[1]])
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", len(runes)-8) + string(runes[len(runes)-4:])
}

// Normalize builds the audit copy: strip zero-width characters and lowercase
// (case-insensitive matching). It never mutates the original request (§4).
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// LoadRuleFile parses a versioned YAML rule package (§20).
func LoadRuleFile(data []byte) ([]Rule, error) {
	var pkg struct {
		Rules []Rule `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}
	if len(pkg.Rules) == 0 {
		return nil, fmt.Errorf("rule package contains no rules")
	}
	for _, r := range pkg.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("rule missing id")
		}
	}
	return pkg.Rules, nil
}
