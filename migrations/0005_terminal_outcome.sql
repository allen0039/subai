-- P1-02: Persist terminal outcome type before settlement to survive crash window.
-- The handler now records whether the dispatch succeeded or failed in a durable field
-- before committing the settlement. Startup recovery reads this field to converge
-- correctly rather than unconditionally marking as completed.

ALTER TABLE requests ADD COLUMN terminal_outcome TEXT;
COMMENT ON COLUMN requests.terminal_outcome IS 'completed|failed - set before settlement, read by recovery';

-- Add index for audit exception query (P2-02)
-- Fixed P0-01: removed expires_at > now() from partial index (not immutable)
-- Query optimizer will use index scan + filter for expires_at condition
CREATE INDEX idx_audit_reviews_exception_lookup ON audit_reviews(
  (exception->>'api_key_id'),
  (exception->>'rule_id'),
  (exception->>'content_hmac'),
  expires_at
) WHERE outcome = 'exception_created';

COMMENT ON INDEX idx_audit_reviews_exception_lookup IS 'P2-02: accelerate exception lookup by key/rule/hmac. expires_at indexed for range scan.';
