package audit

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionConfig defines how long audit data should be kept.
type RetentionConfig struct {
	EventRetention  time.Duration // how long to keep audit_events
	ReviewRetention time.Duration // how long to keep audit_reviews (must be >= EventRetention)
	CleanupInterval time.Duration // how often to run cleanup
}

// DefaultRetentionConfig returns sensible defaults: 90 days for events, 1 year for reviews.
func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		EventRetention:  90 * 24 * time.Hour,  // 90 days
		ReviewRetention: 365 * 24 * time.Hour, // 1 year
		CleanupInterval: 6 * time.Hour,        // every 6 hours
	}
}

// StartRetentionWorker launches a background goroutine that periodically cleans up
// old audit data according to the retention policy (P2-02).
//
// The worker respects foreign key constraints: audit_reviews are cleaned first,
// then audit_events. Active exceptions (expires_at > now()) are never deleted
// regardless of their created_at timestamp.
//
// Returns a channel that will be closed when ctx is cancelled and cleanup stops.
func StartRetentionWorker(ctx context.Context, pool *pgxpool.Pool, cfg RetentionConfig) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.CleanupInterval)
		defer ticker.Stop()

		// Run once immediately on startup
		if err := cleanupAuditData(ctx, pool, cfg); err != nil {
			log.Printf("audit retention: initial cleanup failed: %v", err)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cleanupAuditData(ctx, pool, cfg); err != nil {
					log.Printf("audit retention: cleanup failed: %v", err)
				}
			}
		}
	}()
	return done
}

// cleanupAuditData removes audit records older than the configured retention periods.
// P2-02: Prevents unbounded growth of audit_events and audit_reviews tables.
func cleanupAuditData(ctx context.Context, pool *pgxpool.Pool, cfg RetentionConfig) error {
	now := time.Now()
	eventCutoff := now.Add(-cfg.EventRetention)
	reviewCutoff := now.Add(-cfg.ReviewRetention)

	// Step 1: Delete old reviews first (to respect FK constraint audit_reviews.audit_event_id)
	// BUT: preserve any review with an active exception (expires_at > now())
	reviewResult, err := pool.Exec(ctx, `
		DELETE FROM audit_reviews
		WHERE created_at < $1
		  AND (expires_at IS NULL OR expires_at <= $2)
	`, reviewCutoff, now)
	if err != nil {
		return err
	}

	// Step 2: Delete orphaned events (events with no remaining reviews older than retention)
	eventResult, err := pool.Exec(ctx, `
		DELETE FROM audit_events
		WHERE created_at < $1
		  AND NOT EXISTS (
		    SELECT 1 FROM audit_reviews r WHERE r.audit_event_id = audit_events.id
		  )
	`, eventCutoff)
	if err != nil {
		return err
	}

	reviewsDeleted := reviewResult.RowsAffected()
	eventsDeleted := eventResult.RowsAffected()

	if reviewsDeleted > 0 || eventsDeleted > 0 {
		log.Printf("audit retention: deleted %d reviews (older than %s), %d events (older than %s)",
			reviewsDeleted, reviewCutoff.Format(time.RFC3339),
			eventsDeleted, eventCutoff.Format(time.RFC3339))
	}

	return nil
}

// CleanupOldEvents removes audit_events older than the specified number of days.
// This is a simplified cleanup function for the current schema which only has
// audit_events, audit_rules, and audit_policies tables.
func CleanupOldEvents(ctx context.Context, pool *pgxpool.Pool, retentionDays int) (int64, error) {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	result, err := pool.Exec(ctx, `
		DELETE FROM audit_events
		WHERE triggered_at < $1
	`, cutoff)
	if err != nil {
		return 0, err
	}

	deleted := result.RowsAffected()
	if deleted > 0 {
		log.Printf("audit retention: deleted %d events older than %s", deleted, cutoff.Format(time.RFC3339))
	}

	return deleted, nil
}
