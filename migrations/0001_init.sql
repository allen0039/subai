-- SubAI schema v1. All timestamps are timestamptz (UTC). Money is NUMERIC(30,12) USD.
-- Every mutable config entity carries created_at/updated_at/version for optimistic locking (plan §16.2).
-- (schema_migrations is created by the migration runner itself.)

CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Principals (§16.1) ────────────────────────────────────────────────────────

CREATE TABLE members (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL UNIQUE,
    role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member')),
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    password_hash TEXT, -- admin login only; nullable
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    version       INT NOT NULL DEFAULT 1
);

CREATE TABLE clients (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id   UUID NOT NULL REFERENCES members(id),
    name        TEXT NOT NULL,
    type        TEXT NOT NULL CHECK (type IN ('computer','hermes','cli','other')),
    notes       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    version     INT NOT NULL DEFAULT 1,
    UNIQUE (member_id, name)
);

CREATE TABLE api_keys (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id         UUID NOT NULL REFERENCES members(id),
    client_id         UUID REFERENCES clients(id),
    name              TEXT NOT NULL,
    public_prefix     TEXT NOT NULL,            -- shown in lists; secret shown once at creation
    key_hash          TEXT NOT NULL UNIQUE,     -- sha256 hex of full key
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','revoked')),
    expires_at        TIMESTAMPTZ,
    concurrency_limit INT NOT NULL DEFAULT 1,
    allowed_models    TEXT[],                   -- NULL = all models permitted by routes/prices
    audit_policy_id   UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    version           INT NOT NULL DEFAULT 1
);

-- ── Egress (§8, §22) ──────────────────────────────────────────────────────────

CREATE TABLE proxy_profiles (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL UNIQUE,
    kind                    TEXT NOT NULL CHECK (kind IN ('direct','http','socks5')),
    endpoint                TEXT,               -- NULL/absent for kind=direct
    credentials_ciphertext  BYTEA,              -- AES-GCM(user:pass), NULL when none
    status                  TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    version                 INT NOT NULL DEFAULT 1,
    CHECK (kind <> 'direct' OR endpoint IS NULL) -- direct must not carry a proxy address
);

CREATE TABLE egress_policies (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                       TEXT NOT NULL UNIQUE,
    primary_proxy_id           UUID NOT NULL REFERENCES proxy_profiles(id),
    failure_mode               TEXT NOT NULL DEFAULT 'stop' CHECK (failure_mode IN ('stop','fallback')),
    ordered_fallback_proxy_ids UUID[] NOT NULL DEFAULT '{}',
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    version                    INT NOT NULL DEFAULT 1
);

-- ── Upstream accounts & groups ────────────────────────────────────────────────

CREATE TABLE accounts (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider               TEXT NOT NULL DEFAULT 'codex',
    label                  TEXT NOT NULL UNIQUE,
    credentials_ciphertext BYTEA NOT NULL,      -- AES-GCM JSON {access_token,refresh_token,account_id,...}
    credential_version     INT NOT NULL DEFAULT 1,
    expires_at             TIMESTAMPTZ,
    state                  TEXT NOT NULL DEFAULT 'active' CHECK (state IN
                           ('active','paused','refreshing','reauth_required','quota_exhausted','proxy_unavailable','recovery_hold')),
    concurrency_limit      INT NOT NULL DEFAULT 1,
    priority               INT NOT NULL DEFAULT 100,
    egress_policy_id       UUID REFERENCES egress_policies(id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    version                INT NOT NULL DEFAULT 1
);

CREATE TABLE account_groups (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    version    INT NOT NULL DEFAULT 1
);

CREATE TABLE account_group_members (
    group_id   UUID NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    weight     INT NOT NULL DEFAULT 1,
    PRIMARY KEY (group_id, account_id)
);

CREATE TABLE key_routes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key_id  UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    target_type TEXT NOT NULL CHECK (target_type IN ('account','group')),
    target_id   UUID NOT NULL,
    priority    INT NOT NULL DEFAULT 100,
    UNIQUE (api_key_id, target_type, target_id)
);

-- ── Budget (§18) ──────────────────────────────────────────────────────────────

CREATE TABLE budget_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_type      TEXT NOT NULL CHECK (owner_type IN ('member','key','account','group','key_account','key_group')),
    owner_member_id UUID REFERENCES members(id),
    owner_key_id    UUID REFERENCES api_keys(id),
    owner_account_id UUID REFERENCES accounts(id),
    owner_group_id  UUID REFERENCES account_groups(id),
    period          TEXT NOT NULL CHECK (period IN ('day','week')),
    timezone        TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    mode            TEXT NOT NULL CHECK (mode IN ('fixed','percent')),
    amount          NUMERIC(30,12) CHECK (mode <> 'fixed' OR amount IS NOT NULL),
    percent_bps     INT CHECK (mode <> 'percent' OR (percent_bps BETWEEN 0 AND 10000)),
    base_policy_id  UUID REFERENCES budget_policies(id), -- required for percent; must not cycle
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    version         INT NOT NULL DEFAULT 1
);

CREATE TABLE budget_periods (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id      UUID NOT NULL REFERENCES budget_policies(id),
    period_start   DATE NOT NULL,
    period_end     DATE NOT NULL,
    timezone       TEXT NOT NULL,
    limit_snapshot NUMERIC(30,12) NOT NULL CHECK (limit_snapshot >= 0),
    spent          NUMERIC(30,12) NOT NULL DEFAULT 0,
    reserved       NUMERIC(30,12) NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (policy_id, period_start)
);

CREATE TABLE reservations (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id       TEXT NOT NULL,
    budget_period_id UUID NOT NULL REFERENCES budget_periods(id),
    amount           NUMERIC(30,12) NOT NULL CHECK (amount >= 0),
    state            TEXT NOT NULL DEFAULT 'held' CHECK (state IN ('held','settled','released','unknown')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at       TIMESTAMPTZ,
    UNIQUE (request_id, budget_period_id)
);

-- ── Requests, ledger, prices ──────────────────────────────────────────────────

CREATE TABLE requests (
    id                 TEXT PRIMARY KEY,          -- request_id, gateway-generated opaque id
    api_key_id         UUID NOT NULL REFERENCES api_keys(id),
    client_request_id  TEXT,
    payload_hmac       BYTEA,
    model              TEXT NOT NULL,
    state              TEXT NOT NULL DEFAULT 'received' CHECK (state IN
                       ('received','validated','local_checked','audit_queued','auditing','audit_passed',
                        'reserved','dispatching','streaming','settling','completed',
                        'rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch','unknown')),
    coverage           TEXT NOT NULL DEFAULT '',
    account_id         UUID REFERENCES accounts(id),
    group_id           UUID REFERENCES account_groups(id),
    price_version_id   UUID,
    error_code         TEXT,
    attempt_id         INT NOT NULL DEFAULT 0,
    state_history      JSONB NOT NULL DEFAULT '[]',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- full request bodies are never persisted (§16.2)

CREATE TABLE usage_ledger (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id          TEXT NOT NULL REFERENCES requests(id),
    attempt_id          INT NOT NULL DEFAULT 0,
    api_key_id          UUID NOT NULL REFERENCES api_keys(id),
    account_id          UUID REFERENCES accounts(id),
    budget_period_id    UUID REFERENCES budget_periods(id),
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    cached_input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cost                NUMERIC(30,12) NOT NULL CHECK (cost >= 0),
    price_version_id    UUID,
    entry_type          TEXT NOT NULL DEFAULT 'charge' CHECK (entry_type IN ('charge','adjustment')),
    details             JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (request_id, attempt_id, entry_type) -- idempotent settlement (§18.3); corrections are appended rows
);

CREATE TABLE price_versions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_url   TEXT NOT NULL DEFAULT '',
    source_hash  TEXT NOT NULL DEFAULT '',
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    origin       TEXT NOT NULL CHECK (origin IN ('synthetic','official','manual')),
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','superseded')),
    notes        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE model_prices (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    price_version_id       UUID NOT NULL REFERENCES price_versions(id) ON DELETE CASCADE,
    model                  TEXT NOT NULL,
    input_per_mtok         NUMERIC(30,12) NOT NULL CHECK (input_per_mtok >= 0),
    cached_input_per_mtok  NUMERIC(30,12) NOT NULL CHECK (cached_input_per_mtok >= 0),
    output_per_mtok        NUMERIC(30,12) NOT NULL CHECK (output_per_mtok >= 0),
    fixed_fees             JSONB NOT NULL DEFAULT '{}',
    tiers                  JSONB NOT NULL DEFAULT '[]',
    UNIQUE (price_version_id, model)
);

-- ── Audit (§4, §5, §9) ────────────────────────────────────────────────────────

CREATE TABLE audit_rules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id      TEXT NOT NULL,
    version      INT NOT NULL DEFAULT 1,
    category     TEXT NOT NULL CHECK (category IN ('secret','content','injection','exfil','format')),
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    scope        TEXT[] NOT NULL DEFAULT '{}',
    matcher      JSONB NOT NULL,
    action       TEXT NOT NULL CHECK (action IN ('flag','review','block','reject','unsupported')),
    severity     TEXT NOT NULL DEFAULT 'medium' CHECK (severity IN ('low','medium','high')),
    message      TEXT NOT NULL DEFAULT '',
    fixture_set  TEXT NOT NULL DEFAULT '',
    enabled      BOOLEAN NOT NULL DEFAULT true,
    source       TEXT NOT NULL DEFAULT 'custom' CHECK (source IN ('builtin','custom')),
    modified_by  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (rule_id, version)
);

CREATE TABLE audit_policies (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version     INT NOT NULL,
    content     JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','superseded')),
    modified_by TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (version)
);

CREATE TABLE audit_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id     TEXT NOT NULL,
    api_key_id     UUID NOT NULL REFERENCES api_keys(id),
    decision       TEXT NOT NULL CHECK (decision IN ('allow','block','review','unavailable','unsupported','flag')),
    coverage       TEXT NOT NULL DEFAULT '',
    hits           JSONB NOT NULL DEFAULT '[]',   -- [{rule_id, action, severity, ranges, masked_sample}]
    categories     JSONB NOT NULL DEFAULT '{}',   -- official moderation categories+scores (masked)
    summary        TEXT NOT NULL DEFAULT '',      -- sanitized digest only
    policy_version INT NOT NULL DEFAULT 0,
    model_version  TEXT NOT NULL DEFAULT '',
    duration_ms    INT NOT NULL DEFAULT 0,
    cache_state    TEXT NOT NULL DEFAULT 'miss' CHECK (cache_state IN ('miss','hit')),
    error_type     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_reviews (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    audit_event_id UUID NOT NULL REFERENCES audit_events(id),
    reviewer       TEXT NOT NULL,
    outcome        TEXT NOT NULL CHECK (outcome IN ('confirmed_violation','false_positive','exception_created')),
    note           TEXT NOT NULL DEFAULT '',
    exception      JSONB,             -- {scope:{rule_id|category}, expires_at} exact-scoped, never global
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE admin_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor       TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    changes     JSONB NOT NULL DEFAULT '{}',  -- sanitized before/after; secrets never written (§16.2)
    request_id  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── OAuth sessions (§22) ──────────────────────────────────────────────────────

CREATE TABLE oauth_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID REFERENCES accounts(id),
    state        TEXT NOT NULL UNIQUE,
    verifier     TEXT NOT NULL,               -- PKCE, consumed server-side only
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','completed','expired','cancelled')),
    redirect_uri TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE TABLE admin_sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id  UUID NOT NULL REFERENCES members(id),
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_keys_member ON api_keys(member_id);
CREATE INDEX idx_requests_key ON requests(api_key_id, created_at);
CREATE INDEX idx_requests_state ON requests(state) WHERE state NOT IN ('completed','rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch');
CREATE INDEX idx_audit_events_request ON audit_events(request_id);
CREATE INDEX idx_audit_events_created ON audit_events(created_at);
CREATE INDEX idx_ledger_request ON usage_ledger(request_id);
CREATE INDEX idx_reservations_period ON reservations(budget_period_id) WHERE state IN ('held','unknown');
CREATE INDEX idx_admin_sessions_expiry ON admin_sessions(expires_at);
