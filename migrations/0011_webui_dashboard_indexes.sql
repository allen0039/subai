-- Bounded dashboard and member-overview aggregations. These indexes support
-- time-window queries without changing any data-plane semantics.
CREATE INDEX IF NOT EXISTS idx_requests_created_at ON requests(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_ledger_created_at ON usage_ledger(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_admin_events_created_at ON admin_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_accounts_state ON accounts(state);
