-- User self-service, subscription plans and account pools.
-- This migration is deliberately additive: existing clients/key_routes remain
-- readable until their data has been migrated to subscriptions.

CREATE TABLE plans (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               TEXT NOT NULL UNIQUE,
    description        TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','archived')),
    current_version_id UUID,
    created_by         UUID REFERENCES members(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    version            INT NOT NULL DEFAULT 1
);

CREATE TABLE plan_versions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id               UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    version_number        INT NOT NULL,
    daily_limit_usd       NUMERIC(30,12),
    weekly_limit_usd      NUMERIC(30,12),
    monthly_limit_usd     NUMERIC(30,12),
    concurrency_limit     INT NOT NULL DEFAULT 1 CHECK (concurrency_limit > 0),
    max_keys              INT NOT NULL DEFAULT 1 CHECK (max_keys > 0),
    allowed_models        TEXT[],
    default_validity_days INT NOT NULL DEFAULT 30 CHECK (default_validity_days > 0),
    timezone              TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    created_by            UUID REFERENCES members(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(plan_id, version_number),
    CHECK (daily_limit_usd IS NULL OR daily_limit_usd >= 0),
    CHECK (weekly_limit_usd IS NULL OR weekly_limit_usd >= 0),
    CHECK (monthly_limit_usd IS NULL OR monthly_limit_usd >= 0)
);

ALTER TABLE plans
    ADD CONSTRAINT plans_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES plan_versions(id);

-- Account groups retain their table name during the compatibility period, but
-- become account pools in the product/API vocabulary.
ALTER TABLE account_groups
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'round_robin'
        CHECK (strategy IN ('round_robin','weighted_round_robin','priority_failover'));

ALTER TABLE account_group_members
    ADD COLUMN IF NOT EXISTS priority INT NOT NULL DEFAULT 100;

CREATE TABLE plan_pool_bindings (
    plan_version_id UUID NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
    pool_id         UUID NOT NULL REFERENCES account_groups(id),
    priority        INT NOT NULL DEFAULT 100,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(plan_version_id, pool_id)
);

CREATE TABLE user_subscriptions (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id                 UUID NOT NULL REFERENCES members(id),
    plan_version_id           UUID NOT NULL REFERENCES plan_versions(id),
    status                    TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('scheduled','active','suspended','expired','revoked')),
    starts_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at                TIMESTAMPTZ NOT NULL,
    daily_limit_override      NUMERIC(30,12),
    weekly_limit_override     NUMERIC(30,12),
    monthly_limit_override    NUMERIC(30,12),
    concurrency_override      INT CHECK (concurrency_override IS NULL OR concurrency_override > 0),
    max_keys_override         INT CHECK (max_keys_override IS NULL OR max_keys_override > 0),
    allowed_models_override   TEXT[],
    assigned_by               UUID REFERENCES members(id),
    assigned_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes                     TEXT NOT NULL DEFAULT '',
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    version                   INT NOT NULL DEFAULT 1,
    CHECK (expires_at > starts_at),
    CHECK (daily_limit_override IS NULL OR daily_limit_override >= 0),
    CHECK (weekly_limit_override IS NULL OR weekly_limit_override >= 0),
    CHECK (monthly_limit_override IS NULL OR monthly_limit_override >= 0)
);

CREATE UNIQUE INDEX user_subscriptions_one_active_plan
    ON user_subscriptions(member_id, plan_version_id)
    WHERE status IN ('scheduled','active','suspended');
CREATE INDEX user_subscriptions_member_active_idx
    ON user_subscriptions(member_id, status, expires_at);

CREATE TABLE user_account_grants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id   UUID NOT NULL REFERENCES members(id),
    account_id  UUID NOT NULL REFERENCES accounts(id),
    priority    INT NOT NULL DEFAULT 50,
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','revoked')),
    starts_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ,
    assigned_by UUID REFERENCES members(id),
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    version     INT NOT NULL DEFAULT 1
);
CREATE INDEX user_account_grants_active_idx
    ON user_account_grants(member_id, status, starts_at, expires_at);

CREATE TABLE user_pool_grants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id   UUID NOT NULL REFERENCES members(id),
    pool_id     UUID NOT NULL REFERENCES account_groups(id),
    priority    INT NOT NULL DEFAULT 50,
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','revoked')),
    starts_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ,
    assigned_by UUID REFERENCES members(id),
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    version     INT NOT NULL DEFAULT 1
);
CREATE INDEX user_pool_grants_active_idx
    ON user_pool_grants(member_id, status, starts_at, expires_at);

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS user_subscription_id UUID REFERENCES user_subscriptions(id),
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;
CREATE INDEX api_keys_subscription_idx ON api_keys(user_subscription_id, status);

ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS user_subscription_id UUID REFERENCES user_subscriptions(id);
ALTER TABLE usage_ledger
    ADD COLUMN IF NOT EXISTS user_subscription_id UUID REFERENCES user_subscriptions(id);
CREATE INDEX requests_subscription_idx ON requests(user_subscription_id, created_at DESC);
CREATE INDEX usage_ledger_subscription_idx ON usage_ledger(user_subscription_id, created_at DESC);

-- Reuse the established atomic budget ledger for subscriptions and calendar
-- month windows. Existing rows remain valid under the widened checks.
ALTER TABLE budget_policies
    ADD COLUMN IF NOT EXISTS owner_subscription_id UUID REFERENCES user_subscriptions(id);
ALTER TABLE budget_policies DROP CONSTRAINT IF EXISTS budget_policies_owner_type_check;
ALTER TABLE budget_policies ADD CONSTRAINT budget_policies_owner_type_check
    CHECK (owner_type IN ('member','key','account','group','key_account','key_group','subscription'));
ALTER TABLE budget_policies DROP CONSTRAINT IF EXISTS budget_policies_period_check;
ALTER TABLE budget_policies ADD CONSTRAINT budget_policies_period_check
    CHECK (period IN ('day','week','month'));
CREATE INDEX budget_policies_subscription_idx
    ON budget_policies(owner_subscription_id, status)
    WHERE owner_subscription_id IS NOT NULL;
