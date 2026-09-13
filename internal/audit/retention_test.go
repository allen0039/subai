package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetentionCleanup(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; integration test SKIPPED")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Clean up test tables
	for _, table := range []string{"audit_events", "audit_policies", "audit_rules"} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
			t.Fatalf("clean table %s: %v", table, err)
		}
	}

	// Create minimal schema for audit tables
	schema := `
		CREATE TABLE audit_rules (
			rule_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			description TEXT,
			triggers JSONB NOT NULL,
			action TEXT NOT NULL,
			action_params JSONB,
			enabled BOOLEAN NOT NULL DEFAULT true,
			metadata JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);

		CREATE TABLE audit_policies (
			policy_id SERIAL PRIMARY KEY,
			account_id INTEGER NOT NULL,
			api_key_id INTEGER,
			rule_id TEXT NOT NULL REFERENCES audit_rules(rule_id),
			enabled BOOLEAN NOT NULL DEFAULT true,
			action_override TEXT,
			action_params JSONB,
			priority INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);

		CREATE TABLE audit_events (
			event_id BIGSERIAL PRIMARY KEY,
			request_id TEXT NOT NULL,
			account_id INTEGER NOT NULL,
			api_key_id INTEGER,
			triggered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			rule_id TEXT NOT NULL,
			action TEXT NOT NULL,
			action_params JSONB,
			request_snapshot JSONB,
			decision_metadata JSONB
		);

		CREATE INDEX idx_audit_events_triggered_at ON audit_events(triggered_at);
		CREATE INDEX idx_audit_events_account ON audit_events(account_id, triggered_at);
	`

	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Insert a test rule
	_, err = pool.Exec(ctx, `
		INSERT INTO audit_rules (rule_id, display_name, description, triggers, action)
		VALUES ($1, $2, $3, $4, $5)
	`, "test-rule", "Test Rule", "Test rule for retention", `{"pre_upstream": true}`, "log")
	if err != nil {
		t.Fatalf("insert rule: %v", err)
	}

	// Insert test events with different timestamps
	now := time.Now()
	testCases := []struct {
		daysAgo int
		count   int
	}{
		{5, 3},   // Recent events (should be kept)
		{95, 5},  // Old events (should be deleted with 90-day retention)
		{200, 2}, // Very old events (should be deleted)
	}

	totalInserted := 0
	for _, tc := range testCases {
		triggeredAt := now.Add(-time.Duration(tc.daysAgo) * 24 * time.Hour)
		for i := 0; i < tc.count; i++ {
			_, err = pool.Exec(ctx, `
				INSERT INTO audit_events (request_id, account_id, triggered_at, rule_id, action)
				VALUES ($1, $2, $3, $4, $5)
			`, "req-"+time.Now().Format("20060102150405.000000"), 1, triggeredAt, "test-rule", "log")
			if err != nil {
				t.Fatalf("insert event: %v", err)
			}
			totalInserted++
		}
	}

	t.Logf("Inserted %d total events", totalInserted)

	// Verify initial count
	var initialCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events").Scan(&initialCount)
	if err != nil {
		t.Fatalf("count initial events: %v", err)
	}
	if initialCount != totalInserted {
		t.Fatalf("expected %d events, got %d", totalInserted, initialCount)
	}

	// Run cleanup with 90-day retention
	retentionDays := 90
	deleted, err := CleanupOldEvents(ctx, pool, retentionDays)
	if err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	t.Logf("Deleted %d events", deleted)

	// Verify deletion: only events from last 5 days should remain
	var remainingCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM audit_events").Scan(&remainingCount)
	if err != nil {
		t.Fatalf("count remaining events: %v", err)
	}

	expectedRemaining := 3 // Only the 5-day-old events should remain
	if remainingCount != expectedRemaining {
		t.Fatalf("expected %d remaining events, got %d", expectedRemaining, remainingCount)
	}

	expectedDeleted := 7 // 5 + 2 old events should be deleted
	if deleted != int64(expectedDeleted) {
		t.Fatalf("expected to delete %d events, deleted %d", expectedDeleted, deleted)
	}

	// Verify the remaining events are all recent
	var oldestEvent time.Time
	err = pool.QueryRow(ctx, "SELECT MIN(triggered_at) FROM audit_events").Scan(&oldestEvent)
	if err != nil {
		t.Fatalf("query oldest event: %v", err)
	}

	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	if oldestEvent.Before(cutoff) {
		t.Fatalf("found event older than retention period: %v (cutoff: %v)", oldestEvent, cutoff)
	}

	t.Logf("Cleanup successful: %d events deleted, %d remaining", deleted, remainingCount)
}
