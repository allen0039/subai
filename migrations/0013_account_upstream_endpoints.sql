-- A pool member can use an already-running OpenAI-compatible relay (such as
-- a private CPA or Sub2API deployment), not only direct Codex OAuth.
-- Credentials remain in the existing encrypted envelope; this URL is not a secret.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS upstream_base_url TEXT;

COMMENT ON COLUMN accounts.upstream_base_url IS
    'Per-account OpenAI-compatible base URL; /responses is appended when absent.';
