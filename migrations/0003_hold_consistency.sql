-- Preserve existing rows and accept the reason names used by all writers.
ALTER TABLE account_holds DROP CONSTRAINT IF EXISTS account_holds_reason_check;
ALTER TABLE account_holds ADD CONSTRAINT account_holds_reason_check
 CHECK (reason IN ('over_reserve','unknown_pending','admin_action','admin_pause','reauth_required','legacy_review'));

-- A migrated recovery_hold was not necessarily caused by unknown requests.
INSERT INTO account_holds(account_id,reason,details)
SELECT account_id,'legacy_review',details FROM account_holds
WHERE details->>'migrated_from'='recovery_hold'
ON CONFLICT(account_id,reason) DO NOTHING;

INSERT INTO account_holds(account_id,reason,details)
SELECT a.id,'legacy_review','{"source":"unclassified_existing_hold"}'::jsonb
FROM accounts a WHERE a.state='recovery_hold'
AND NOT EXISTS(SELECT 1 FROM account_holds h WHERE h.account_id=a.id)
ON CONFLICT(account_id,reason) DO NOTHING;
