-- Review round 6: preserve a confirmed upstream failure separately from a
-- successful completion, and persist the exact audit-input HMAC needed for
-- narrowly scoped review exceptions.

ALTER TABLE requests DROP CONSTRAINT IF EXISTS requests_state_check;
ALTER TABLE requests ADD CONSTRAINT requests_state_check CHECK (state IN
    ('received','validated','local_checked','audit_queued','auditing','audit_passed',
     'reserved','dispatching','streaming','settling','completed','failed_after_dispatch',
     'rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch','unknown'));

ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS content_hmac BYTEA;
CREATE INDEX IF NOT EXISTS idx_audit_events_content_hmac ON audit_events(api_key_id, content_hmac)
WHERE content_hmac IS NOT NULL;
