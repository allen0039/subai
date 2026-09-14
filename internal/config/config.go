// Package config loads gateway configuration from environment variables.
// Defaults follow plan §22. Secrets are only ever provided via environment or
// the admin UI; they are never written into the repository.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr           string
	DatabaseURL          string
	MasterKey            string // hex, 32 bytes; encrypts credentials at rest (§22)
	ProductionReady      bool
	AllowSyntheticPrices bool // explicit test mode: synthetic price versions may serve the data plane (review P1-2)
	PublicRegistration   bool

	PerKeyConcurrency     int
	PerAccountConcurrency int
	RequestBodyLimit      int64
	AuditBodyMemoryLimit  int64

	AuditMode         string // pre_call (only supported mode in v1)
	AuditCoverage     string // full_visible_required
	AuditFailureMode  string // stop
	AuditCacheTTL     time.Duration
	AuditWaitQueueMax int // global WAIT bound (review P2-12: waits counted separately from executions)
	AuditMaxInFlight  int // max concurrent moderation executions
	AuditPerKeyQueue  int
	AuditWaitTimeout  time.Duration
	AuditCallTimeout  time.Duration
	AuditTotalBudget  time.Duration
	AuditRetries      int

	ModerationEndpoint string
	ModerationModel    string
	ModerationAPIKey   string // Platform key, separate from Pro OAuth credentials (§5)
	ModerationProxy    string // optional dedicated egress for audit calls

	UpstreamBaseURL string        // Codex upstream, mockable in tests (D-003)
	UpstreamTimeout time.Duration // per-dispatch budget, isolated from finalization (review R2-05)
	OutputBound     int           // default injected max_output_tokens (D-001)
	BillingMode     string        // metered (Sub2API-style) or strict_reservation
	PriceSourceURL  string        // LiteLLM/Sub2API-compatible model price catalog
	PriceHashURL    string        // optional SHA-256 sidecar
	PriceSyncPeriod time.Duration // background catalog check interval

	SessionSecret string
	SingletonKey  int // advisory lock key for single-active enforcement (D-007)

	DevBootstrapAdmin string // optional "user:password" to bootstrap first admin; empty in prod

	// Log retention and admin CORS remain internal defaults until their runtime
	// policies are explicitly exposed.
}

func Load() (*Config, error) {
	const defaultPriceSource = "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json"
	const defaultPriceHash = "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.sha256"
	priceSource := env("SUBAI_PRICE_SOURCE_URL", defaultPriceSource)
	priceHashDefault := defaultPriceHash
	if strings.TrimSpace(os.Getenv("SUBAI_PRICE_SOURCE_URL")) != "" && strings.TrimSpace(os.Getenv("SUBAI_PRICE_HASH_URL")) == "" {
		// A sidecar belongs to one exact source. Do not accidentally verify a
		// custom catalog against the default repository's digest.
		priceHashDefault = ""
	}
	c := &Config{
		ListenAddr:            env("SUBAI_LISTEN", ":8080"),
		DatabaseURL:           env("SUBAI_DATABASE_URL", "postgres://subai:subai@localhost:5432/subai?sslmode=disable"),
		MasterKey:             env("SUBAI_MASTER_KEY", ""),
		ProductionReady:       envBool("SUBAI_PRODUCTION_READY", false),
		AllowSyntheticPrices:  envBool("SUBAI_ALLOW_SYNTHETIC_PRICES", false),
		PublicRegistration:    false, // fixed per §22
		PerKeyConcurrency:     envInt("SUBAI_PER_KEY_CONCURRENCY", 1),
		PerAccountConcurrency: envInt("SUBAI_PER_ACCOUNT_CONCURRENCY", 1),
		RequestBodyLimit:      int64(envInt("SUBAI_REQUEST_BODY_LIMIT_MIB", 8)) << 20,
		AuditBodyMemoryLimit:  int64(envInt("SUBAI_AUDIT_BODY_MEMORY_LIMIT_MIB", 128)) << 20,
		AuditMode:             "pre_call",
		AuditCoverage:         "full_visible_required",
		AuditFailureMode:      "stop",
		AuditCacheTTL:         time.Duration(envInt("SUBAI_AUDIT_CACHE_TTL_S", 600)) * time.Second,
		AuditWaitQueueMax:     envInt("SUBAI_AUDIT_QUEUE_MAX", 50),
		AuditMaxInFlight:      envInt("SUBAI_AUDIT_MAX_INFLIGHT", 8),
		AuditPerKeyQueue:      envInt("SUBAI_AUDIT_PER_KEY_QUEUE", 10),
		AuditWaitTimeout:      time.Duration(envInt("SUBAI_AUDIT_WAIT_TIMEOUT_S", 30)) * time.Second,
		AuditCallTimeout:      time.Duration(envInt("SUBAI_AUDIT_CALL_TIMEOUT_S", 10)) * time.Second,
		AuditTotalBudget:      time.Duration(envInt("SUBAI_AUDIT_TOTAL_BUDGET_S", 60)) * time.Second,
		AuditRetries:          envInt("SUBAI_AUDIT_RETRIES", 2),
		ModerationEndpoint:    env("SUBAI_MODERATION_ENDPOINT", "https://api.openai.com/v1/moderations"),
		ModerationModel:       env("SUBAI_MODERATION_MODEL", "omni-moderation-latest"),
		ModerationAPIKey:      env("SUBAI_MODERATION_API_KEY", ""),
		ModerationProxy:       env("SUBAI_MODERATION_PROXY", ""),
		UpstreamBaseURL:       env("SUBAI_UPSTREAM_BASE_URL", "https://chatgpt.com/backend-api/codex"),
		UpstreamTimeout:       time.Duration(envInt("SUBAI_UPSTREAM_TIMEOUT_S", 600)) * time.Second,
		OutputBound:           envInt("SUBAI_OUTPUT_BOUND", 4096),
		BillingMode:           env("SUBAI_BILLING_MODE", "metered"),
		PriceSourceURL:        priceSource,
		PriceHashURL:          env("SUBAI_PRICE_HASH_URL", priceHashDefault),
		PriceSyncPeriod:       time.Duration(envInt("SUBAI_PRICE_SYNC_INTERVAL_MIN", 60)) * time.Minute,
		SessionSecret:         env("SUBAI_SESSION_SECRET", ""),
		SingletonKey:          envInt("SUBAI_SINGLETON_KEY", 0x53554241), // "SUBA"
		DevBootstrapAdmin:     env("SUBAI_DEV_BOOTSTRAP_ADMIN", ""),
	}
	if c.MasterKey == "" {
		return nil, fmt.Errorf("SUBAI_MASTER_KEY is required (32-byte hex; encrypts stored credentials)")
	}
	if c.SessionSecret == "" {
		c.SessionSecret = c.MasterKey // acceptable fallback: same root of trust
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate rejects impossible resource limits at startup. Letting these reach
// queue and retry code changes failure behaviour and can otherwise panic.
func (c *Config) Validate() error {
	if c.PerKeyConcurrency <= 0 || c.PerAccountConcurrency <= 0 {
		return fmt.Errorf("per-key and per-account concurrency must be positive")
	}
	if c.RequestBodyLimit <= 0 || c.AuditBodyMemoryLimit <= 0 || c.OutputBound <= 0 {
		return fmt.Errorf("request body, audit memory and output bounds must be positive")
	}
	if c.AuditWaitQueueMax <= 0 || c.AuditMaxInFlight <= 0 || c.AuditPerKeyQueue <= 0 {
		return fmt.Errorf("audit queue and concurrency limits must be positive")
	}
	if c.AuditWaitTimeout <= 0 || c.AuditCallTimeout <= 0 || c.AuditTotalBudget <= 0 || c.UpstreamTimeout <= 0 {
		return fmt.Errorf("audit and upstream timeouts must be positive")
	}
	if c.AuditCallTimeout > c.AuditTotalBudget {
		return fmt.Errorf("audit call timeout cannot exceed total audit budget")
	}
	if c.AuditRetries < 0 {
		return fmt.Errorf("audit retries cannot be negative")
	}
	if c.AuditCacheTTL <= 0 {
		return fmt.Errorf("cache TTL must be positive")
	}
	if c.BillingMode != "metered" && c.BillingMode != "strict_reservation" {
		return fmt.Errorf("SUBAI_BILLING_MODE must be metered or strict_reservation")
	}
	if c.PriceSyncPeriod <= 0 {
		return fmt.Errorf("price sync interval must be positive")
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
