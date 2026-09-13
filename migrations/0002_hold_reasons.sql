-- Add persistent hold reasons to accounts (review R3-01, R3-02).
-- Allows tracking multiple independent isolation causes: over_reserve,
-- unknown_pending, admin_pause, etc. Account is only restored to 'active'
-- when ALL hold reasons are cleared.

CREATE TABLE account_holds (
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL CHECK (reason IN ('over_reserve','unknown_pending','admin_action')),
    details    JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, reason)
);

CREATE INDEX idx_account_holds_account ON account_holds(account_id);
CREATE INDEX idx_account_holds_reason ON account_holds(reason);

COMMENT ON TABLE account_holds IS 'Tracks why an account is in recovery_hold: over_reserve, unknown_pending, or admin_action';
COMMENT ON COLUMN account_holds.reason IS 'Isolation trigger: over_reserve (actual > reserved), unknown_pending (unresolved dispatch), admin_action (manual)';
COMMENT ON COLUMN account_holds.details IS 'Structured context: request_id for unknown_pending, settled_at for over_reserve, operator for admin_action';

-- Migrate existing recovery_hold accounts to the new hold table.
-- Any account in recovery_hold gets an 'unknown_pending' reason as the safe default.
-- Operators must manually review and adjust if the actual cause was over_reserve.
INSERT INTO account_holds (account_id, reason, details)
SELECT id, 'unknown_pending', '{"migrated_from":"recovery_hold","note":"manual review required"}'::jsonb
FROM accounts
WHERE state = 'recovery_hold'
ON CONFLICT (account_id, reason) DO NOTHING;
