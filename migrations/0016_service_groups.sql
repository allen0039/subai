-- Service groups are the customer-facing routing boundary.  They replace the
-- previous product meaning of "account pool" without changing its physical
-- identity, so existing accounts, plans, keys and usage remain addressable.
ALTER TABLE account_groups
    ADD COLUMN IF NOT EXISTS platform TEXT NOT NULL DEFAULT 'codex'
        CHECK (platform IN ('codex','openai_compatible','anthropic','gemini','composite')),
    ADD COLUMN IF NOT EXISTS subscription_type TEXT NOT NULL DEFAULT 'standard'
        CHECK (subscription_type IN ('standard','subscription')),
    ADD COLUMN IF NOT EXISTS rate_multiplier NUMERIC(30,12) NOT NULL DEFAULT 1
        CHECK (rate_multiplier >= 0),
    ADD COLUMN IF NOT EXISTS daily_limit_usd NUMERIC(30,12) CHECK (daily_limit_usd IS NULL OR daily_limit_usd >= 0),
    ADD COLUMN IF NOT EXISTS weekly_limit_usd NUMERIC(30,12) CHECK (weekly_limit_usd IS NULL OR weekly_limit_usd >= 0),
    ADD COLUMN IF NOT EXISTS monthly_limit_usd NUMERIC(30,12) CHECK (monthly_limit_usd IS NULL OR monthly_limit_usd >= 0),
    ADD COLUMN IF NOT EXISTS default_validity_days INT NOT NULL DEFAULT 30 CHECK (default_validity_days > 0),
    ADD COLUMN IF NOT EXISTS model_allowlist_enabled BOOLEAN NOT NULL DEFAULT false;

-- Preserve the intent of already-configured model rules: once a group had
-- rules it was already operating as an explicit allow-list.
UPDATE account_groups g
SET model_allowlist_enabled=true
WHERE EXISTS (SELECT 1 FROM account_group_model_rules r WHERE r.group_id=g.id);

-- A group used by an existing subscription plan is a subscription service.
UPDATE account_groups g
SET subscription_type='subscription'
WHERE EXISTS (
    SELECT 1 FROM plan_pool_bindings b
    JOIN plan_versions v ON v.id=b.plan_version_id
    WHERE b.pool_id=g.id
);

-- A legacy pool made entirely of compatible upstreams should retain that
-- platform identity. Mixed legacy pools remain Codex-compatible until an
-- operator splits them into distinct service groups.
UPDATE account_groups g
SET platform='openai_compatible'
WHERE EXISTS (SELECT 1 FROM account_group_members m JOIN accounts a ON a.id=m.account_id WHERE m.group_id=g.id)
  AND NOT EXISTS (SELECT 1 FROM account_group_members m JOIN accounts a ON a.id=m.account_id WHERE m.group_id=g.id AND a.provider='codex');

CREATE TABLE service_group_model_routes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id            UUID NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
    public_model        TEXT NOT NULL,
    upstream_model      TEXT NOT NULL DEFAULT '',
    target_group_id     UUID REFERENCES account_groups(id) ON DELETE CASCADE,
    priority            INT NOT NULL DEFAULT 100,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    version             INT NOT NULL DEFAULT 1,
    UNIQUE(group_id, public_model),
    CHECK (length(trim(public_model)) > 0)
);
CREATE INDEX service_group_model_routes_active_idx
    ON service_group_model_routes(group_id, public_model, priority) WHERE status='active';

-- New plan versions select exactly one subscription service group. Existing
-- versions are migrated only when their former pool binding is unambiguous;
-- multi-pool versions deliberately keep legacy routing until split by admin.
ALTER TABLE plan_versions ADD COLUMN IF NOT EXISTS service_group_id UUID REFERENCES account_groups(id);
WITH one_group AS (
    SELECT plan_version_id, min(pool_id::text)::uuid AS group_id
    FROM plan_pool_bindings
    GROUP BY plan_version_id
    HAVING count(*)=1
)
UPDATE plan_versions v SET service_group_id=o.group_id
FROM one_group o WHERE v.id=o.plan_version_id AND v.service_group_id IS NULL;
CREATE INDEX plan_versions_service_group_idx ON plan_versions(service_group_id);

ALTER TABLE user_subscriptions ADD COLUMN IF NOT EXISTS service_group_id UUID REFERENCES account_groups(id);
UPDATE user_subscriptions s
SET service_group_id=v.service_group_id
FROM plan_versions v
WHERE s.plan_version_id=v.id AND s.service_group_id IS NULL AND v.service_group_id IS NOT NULL;
CREATE INDEX user_subscriptions_service_group_active_idx
    ON user_subscriptions(member_id, service_group_id, status, expires_at);
