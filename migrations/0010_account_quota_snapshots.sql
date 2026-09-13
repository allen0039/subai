-- Latest official ChatGPT/Codex quota snapshot for each upstream account.
-- Failed refreshes keep the last successful snapshot and only replace the
-- attempt metadata, so operators never lose the last known reset times.
CREATE TABLE account_quota_snapshots (
    account_id       UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    snapshot         JSONB,
    fetched_at       TIMESTAMPTZ,
    last_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    fetch_error      TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

