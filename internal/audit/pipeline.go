package audit

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// PipelineConfig carries the tunables from §6.3 / §22.
type PipelineConfig struct {
	KeyScope        string // key id, binds cache keys (§6.2)
	HMACKey         []byte
	WaitQueueMax    int
	MaxInFlight     int
	PerKeyQueue     int
	WaitTimeout     time.Duration
	TotalBudget     time.Duration
	CallTimeout     time.Duration
	Retries         int
	CacheTTL        time.Duration
	ModerationModel string
	Strict          bool // strict mode: unsupported coverage rejects the request (§6.1)
}

// Pipeline runs extract → local rules → cache → queue → official moderation.
// It must complete before any upstream dispatch (§3: 审核不得与上游推理并行).
type Pipeline struct {
	Engine *Engine
	Cache  *Cache
	Queue  *Queue
	Moder  *ModerationClient
	// ExceptionLookup suppresses only exact, still-valid non-secret rule hits.
	// It is injected by the gateway so audit remains storage independent.
	ExceptionLookup ExceptionLookup
	cfgTemplate     PipelineConfig

	mu        sync.Mutex
	policyVer int64
}

func NewPipeline(engine *Engine, moder *ModerationClient, cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		Engine:      engine,
		Cache:       NewCache(cfg.CacheTTL),
		Queue:       NewQueue(cfg.WaitQueueMax, cfg.PerKeyQueue, cfg.MaxInFlight),
		Moder:       moder,
		cfgTemplate: cfg,
	}
}

// Run audits one request. The returned Result carries the unified decision.
// Any error in moderation surfaces as Decision=unavailable — the gateway must
// stop the request, never proceed unreviewed (§6.3 audit_failure_mode=stop).
func (p *Pipeline) Run(ctx context.Context, doc *AuditDocument, keyScope string) *Result {
	start := time.Now()
	cfg := p.cfgTemplate
	cfg.KeyScope = keyScope
	res := &Result{Coverage: doc.Coverage, MissingParts: doc.MissingParts, CacheState: "miss"}

	// 1. Coverage gate (§6.1): unsupported means reject or declare unsupported;
	// partial coverage is allowed but recorded, never presented as full.
	if doc.Coverage == CoverageUnsupported {
		if cfg.Strict {
			res.Decision = DecisionUnsupported
			res.ErrorType = "coverage_unsupported"
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
		// non-strict deployments still mark the limited coverage on the event
	}

	// 2. Local rules first. A review exception is deliberately scoped to the
	// normalized request fingerprint, API key and rule id. Secret findings are
	// never suppressible because they must not leave the process (§4).
	contentHMAC := ContentHMAC(cfg.HMACKey, keyScope, doc.Segments)
	res.ContentHMAC = append([]byte(nil), contentHMAC...)
	hits, _ := p.Engine.RunLocal(doc)
	if p.ExceptionLookup != nil {
		filtered, err := p.filterExceptions(ctx, keyScope, contentHMAC, hits)
		if err != nil {
			res.Decision = DecisionUnavailable
			res.ErrorType = "audit_exception_lookup"
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
		hits = filtered
	}
	res.Hits = hits
	res.RuleVersion = p.Engine.Ruleset().Version
	if ver := p.PolicyVersion(); ver > 0 {
		res.PolicyVersion = ver
	}
	secretBlocked := false
	for _, h := range hits {
		if h.Category == "secret" && (h.Action == "block" || h.Action == "review" || h.Action == "reject") {
			secretBlocked = true
			break
		}
	}
	if secretBlocked {
		res.Decision = DecisionReview // high-certainty secret: pause pending human review (§20)
		res.SecretBlocked = true
		res.Summary = summarize(hits)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	// R4-05: Handle all local rule actions explicitly
	for _, h := range hits {
		switch h.Action {
		case "block":
			res.Decision = DecisionBlock
			res.Summary = summarize(hits)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		case "review":
			res.Decision = DecisionReview
			res.Summary = summarize(hits)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		case "reject":
			res.Decision = DecisionBlock // reject is a variant of block
			res.Summary = summarize(hits)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		case "unsupported":
			res.Decision = DecisionUnsupported
			res.Summary = summarize(hits)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		case "flag":
			// flag is informational only, continue to external audit
			continue
		default:
			// Unknown action should have been rejected at rule validation
			res.Decision = DecisionUnavailable
			res.ErrorType = "invalid_rule_action"
			res.Summary = fmt.Sprintf("unknown action: %s", h.Action)
			res.DurationMS = time.Since(start).Milliseconds()
			return res
		}
	}

	// 3. Cache lookup (§6.2): identical normalized input + scope + versions.
	ck := CacheKey(contentHMAC, keyScope, res.RuleVersion, cfg.ModerationModel, res.PolicyVersion)
	if cached, ok := p.Cache.Get(ck); ok {
		res.Decision = cached.Decision
		res.Categories = cached.Categories
		res.ModelVersion = cached.ModelVersion
		res.CacheState = "hit"
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	// 4. Fair queue (§6.3): bounded waits, per-key fairness, cancellation-aware.
	release, err := p.Queue.Acquire(ctx, keyScope, cfg.WaitTimeout)
	if err != nil {
		res.Decision = DecisionUnavailable
		switch err {
		case ErrQueueFull:
			res.ErrorType = "audit_queue_full"
		case ErrQueueTimout:
			res.ErrorType = "audit_queue_timeout"
		default:
			res.ErrorType = "client_cancelled"
		}
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer release()

	totalCtx, cancel := context.WithTimeout(ctx, cfg.TotalBudget)
	defer cancel()
	outcome, decision, errType, err := p.Moder.Check(totalCtx, doc)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Decision = decision
		if decision == DecisionUnsupported {
			res.ErrorType = errType
			return res
		}
		res.Decision = DecisionUnavailable
		res.ErrorType = errType
		log.Printf("audit: moderation unavailable key=%s errType=%s err=%v", keyScope, errType, err)
		return res
	}

	res.Decision = decision
	res.Categories = outcome.Categories
	res.ModelVersion = ModelName("", cfg.ModerationModel)
	if decision == DecisionAllow {
		// Only allow results are cached; blocks stay uncached so policy changes
		// and manual reviews always re-evaluate (§6.2 保守语义).
		p.Cache.Put(ck, CachedResult{Decision: decision, Categories: outcome.Categories, ModelVersion: res.ModelVersion})
	}
	return res
}

func (p *Pipeline) filterExceptions(ctx context.Context, keyID string, contentHMAC []byte, hits []RuleHit) ([]RuleHit, error) {
	filtered := make([]RuleHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Category != "secret" {
			exempt, err := p.ExceptionLookup(ctx, keyID, contentHMAC, hit.RuleID)
			if err != nil {
				return nil, err
			}
			if exempt {
				continue
			}
		}
		filtered = append(filtered, hit)
	}
	return filtered, nil
}

// summarize builds a sanitized digest: rule ids + masked samples only (§9).
func summarize(hits []RuleHit) string {
	if len(hits) == 0 {
		return ""
	}
	s := ""
	for i, h := range hits {
		if i > 0 {
			s += "; "
		}
		s += fmt.Sprintf("%s[%s]", h.RuleID, h.Action)
		if h.MaskedExample != "" {
			s += " " + h.MaskedExample
		}
	}
	return s
}

func (p *Pipeline) PolicyVersion() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policyVer
}

func (p *Pipeline) SetPolicyVersion(v int64) {
	p.mu.Lock()
	p.policyVer = v
	p.mu.Unlock()
}
