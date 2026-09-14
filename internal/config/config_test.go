package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		PerKeyConcurrency: 1, PerAccountConcurrency: 1, RequestBodyLimit: 1,
		AuditBodyMemoryLimit: 1, OutputBound: 1, AuditWaitQueueMax: 1,
		AuditMaxInFlight: 1, AuditPerKeyQueue: 1, AuditWaitTimeout: time.Second,
		AuditCallTimeout: time.Second, AuditTotalBudget: 2 * time.Second,
		AuditRetries: 0, AuditCacheTTL: time.Second, UpstreamTimeout: time.Second,
		BillingMode: "metered", PriceSyncPeriod: time.Hour,
	}
}

func TestLoadDoesNotReuseDefaultHashForCustomPriceSource(t *testing.T) {
	t.Setenv("SUBAI_MASTER_KEY", strings.Repeat("0", 64))
	t.Setenv("SUBAI_PRICE_SOURCE_URL", "https://prices.example.test/catalog.json")
	t.Setenv("SUBAI_PRICE_HASH_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PriceHashURL != "" {
		t.Fatalf("custom source inherited unrelated hash URL %q", cfg.PriceHashURL)
	}
}

func TestValidateRejectsInvalidRetryAndTimeoutConfiguration(t *testing.T) {
	c := validConfig()
	c.AuditRetries = -1
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() accepted negative retries")
	}
	c = validConfig()
	c.AuditCallTimeout = 3 * time.Second
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() accepted call timeout above total budget")
	}
}
