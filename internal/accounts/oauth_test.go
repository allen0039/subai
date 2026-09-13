// Package accounts_test provides concurrency and race condition tests for OAuth
// flow. Tests target P1-01 (state collision) and P1-02 (session race).
package accounts_test

import (
	"context"
	"sync"
	"testing"

	"subai/internal/accounts"
	"subai/internal/storage"
)

// TestConcurrentStartSession_RaceCondition tests P1-02: two concurrent
// StartSession calls for the same account should not both succeed.
// Expected: one succeeds, one fails with "already has a pending reauthorization".
func TestConcurrentStartSession_RaceCondition(t *testing.T) {
	t.Skip("Requires database setup - to be implemented after P1-02 fix")

	db := setupTestDB(t)
	defer db.Close()

	mgr := accounts.NewManager(db, "https://auth.example.com", "https://token.example.com", "client-id", "https://callback.example.com")

	// Create a test account
	accountID := createTestAccount(t, db)

	var wg sync.WaitGroup
	results := make(chan error, 2)

	// Launch 2 concurrent StartSession calls for the same account
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			_, _, _, _, err := mgr.StartSession(ctx, &accountID)
			results <- err
		}()
	}

	wg.Wait()
	close(results)

	// Collect results
	var successCount, failCount int
	for err := range results {
		if err == nil {
			successCount++
		} else {
			failCount++
		}
	}

	// Expect exactly 1 success and 1 failure
	if successCount != 1 || failCount != 1 {
		t.Errorf("Expected 1 success and 1 failure, got %d successes and %d failures", successCount, failCount)
	}
}

// TestConcurrentCompleteCallback_StateCollision tests P1-01: two sessions
// completing at the same time should not both set state=” (unique violation).
// Expected: both complete successfully with different final state values.
func TestConcurrentCompleteCallback_StateCollision(t *testing.T) {
	t.Skip("Requires database setup and mock OAuth server - to be implemented after P1-01 fix")

	db := setupTestDB(t)
	defer db.Close()

	mgr := accounts.NewManager(db, "https://auth.example.com", "https://token.example.com", "client-id", "https://callback.example.com")

	// Create 2 test sessions
	state1, code1 := createTestOAuthSession(t, db, nil)
	state2, code2 := createTestOAuthSession(t, db, nil)

	var wg sync.WaitGroup
	results := make(chan error, 2)

	// Launch 2 concurrent CompleteCallback calls
	for _, stateCode := range []struct{ state, code string }{{state1, code1}, {state2, code2}} {
		wg.Add(1)
		go func(s, c string) {
			defer wg.Done()
			ctx := context.Background()
			_, err := mgr.CompleteCallback(ctx, s, c)
			results <- err
		}(stateCode.state, stateCode.code)
	}

	wg.Wait()
	close(results)

	// Both should succeed (no unique constraint violation)
	for err := range results {
		if err != nil {
			t.Errorf("CompleteCallback failed: %v", err)
		}
	}

	// Verify: no two sessions should have the same state value in the database
	var duplicates int
	err := db.Pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM (
			SELECT state, COUNT(*) as cnt FROM oauth_sessions
			WHERE state != '' GROUP BY state HAVING COUNT(*) > 1
		) duplicates`).Scan(&duplicates)
	if err != nil {
		t.Fatalf("Failed to check for duplicate states: %v", err)
	}
	if duplicates > 0 {
		t.Errorf("Found %d duplicate state values (P1-01 violation)", duplicates)
	}
}

// TestConcurrentRefresh_Coalescing tests that concurrent refresh attempts
// for the same account coalesce properly (§8 requirement).
// Expected: first caller performs refresh, subsequent callers fail fast.
func TestConcurrentRefresh_Coalescing(t *testing.T) {
	t.Skip("Requires database setup and mock token endpoint - to be implemented")

	db := setupTestDB(t)
	defer db.Close()

	mgr := accounts.NewManager(db, "https://auth.example.com", "https://token.example.com", "client-id", "https://callback.example.com")

	accountID := createTestAccountWithRefreshToken(t, db)

	var wg sync.WaitGroup
	results := make([]error, 5)

	// Launch 5 concurrent Refresh calls
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := context.Background()
			results[idx] = mgr.Refresh(ctx, accountID)
		}(i)
	}

	wg.Wait()

	// Exactly 1 should succeed (the first one), others should fail with "refresh already in progress"
	var successCount int
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}

	if successCount != 1 {
		t.Errorf("Expected exactly 1 successful refresh, got %d", successCount)
	}
}

// TestCredentialVersionRace tests that concurrent updates don't clobber
// newer credentials (optimistic locking via credential_version).
func TestCredentialVersionRace(t *testing.T) {
	t.Skip("Requires database setup - to be implemented")

	db := setupTestDB(t)
	defer db.Close()

	_ = createTestAccount(t, db)

	// Simulate: one process refreshes, another tries to update with stale version
	// Expected: stale update should be rejected ("credential changed concurrently")
}

// ── Test helpers ──────────────────────────────────────────────────────────────

func setupTestDB(t *testing.T) *storage.DB {
	// TODO: Connect to test database with migrations applied
	t.Fatal("setupTestDB not implemented - requires test database configuration")
	return nil
}

func createTestAccount(t *testing.T, db *storage.DB) string {
	// TODO: Insert test account with valid credentials
	t.Fatal("createTestAccount not implemented")
	return ""
}

func createTestAccountWithRefreshToken(t *testing.T, db *storage.DB) string {
	// TODO: Insert test account with refresh_token in credentials
	t.Fatal("createTestAccountWithRefreshToken not implemented")
	return ""
}

func createTestOAuthSession(t *testing.T, db *storage.DB, accountID *string) (state, code string) {
	// TODO: Insert test oauth_session with pending status
	t.Fatal("createTestOAuthSession not implemented")
	return "", ""
}
