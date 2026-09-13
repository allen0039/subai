-- Migration 0007: Fix OAuth concurrency issues
-- Date: 2026-09-13
-- Fixes: P1-01 (state collision) and P1-02 (session race)

-- ══════════════════════════════════════════════════════════════════════════════
-- P1-02: Prevent concurrent pending sessions per account
-- ══════════════════════════════════════════════════════════════════════════════
-- Problem: Two concurrent StartSession calls can both pass the SELECT EXISTS check
-- and both INSERT pending sessions, violating the business rule "one pending reauth
-- per account at a time."
--
-- Solution: Partial unique index enforces the constraint at database level.
-- The index only includes rows WHERE status='pending', so completed/expired/cancelled
-- sessions don't conflict. This allows historical audit trail while preventing races.
--
-- CONCURRENTLY ensures zero downtime - no table locks during index build.

-- Note: CONCURRENTLY removed because it cannot run inside transaction block
-- Since this is a new system with no data, regular CREATE INDEX is safe
CREATE UNIQUE INDEX idx_oauth_sessions_one_pending_per_account
ON oauth_sessions(account_id)
WHERE status='pending';

-- ══════════════════════════════════════════════════════════════════════════════
-- P1-01: State collision fix (code-only, no schema changes)
-- ══════════════════════════════════════════════════════════════════════════════
-- Problem: CompleteCallback was setting state='' for all completed sessions,
-- causing unique constraint violations when 2+ sessions completed concurrently.
--
-- Solution: Code change only (internal/accounts/oauth.go:148)
-- Remove state='' from the UPDATE statement. The state column will retain its
-- original random value after completion, which is safe because:
--   1. status='completed' already prevents state reuse (checked before allowing completion)
--   2. State tokens contain no sensitive data (just random CSRF-protection strings)
--   3. Zero schema changes = zero migration complexity
--
-- This migration file documents the fix but requires no SQL execution.
-- The actual change is in the Go code.

-- ══════════════════════════════════════════════════════════════════════════════
-- Verification queries
-- ══════════════════════════════════════════════════════════════════════════════

-- After deployment, verify the constraint is working:
-- SELECT account_id, COUNT(*) as pending_count
-- FROM oauth_sessions
-- WHERE status='pending'
-- GROUP BY account_id
-- HAVING COUNT(*) > 1;
-- Expected: 0 rows (no account should have multiple pending sessions)

-- Check for duplicate state values (should be none):
-- SELECT state, COUNT(*) as occurrences
-- FROM oauth_sessions
-- WHERE state != '' AND state IS NOT NULL
-- GROUP BY state
-- HAVING COUNT(*) > 1;
-- Expected: 0 rows (all states should be unique)
