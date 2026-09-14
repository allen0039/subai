-- Model capability, mapping and plan-version pricing rules.  All additions
-- are additive so existing accounts remain legacy-unrestricted until an
-- operator explicitly configures capabilities.
CREATE TABLE account_model_capabilities (
    account_id     UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    public_model   TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    priority       INT NOT NULL DEFAULT 100,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    version        INT NOT NULL DEFAULT 1,
    PRIMARY KEY(account_id, public_model),
    CHECK (length(trim(public_model)) > 0 AND length(trim(upstream_model)) > 0)
);
CREATE INDEX account_model_capabilities_active_idx ON account_model_capabilities(public_model, account_id) WHERE status='active';

CREATE TABLE account_group_model_rules (
    group_id       UUID NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
    public_model   TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    version        INT NOT NULL DEFAULT 1,
    PRIMARY KEY(group_id, public_model),
    CHECK (length(trim(public_model)) > 0)
);
CREATE INDEX account_group_model_rules_active_idx ON account_group_model_rules(public_model, group_id) WHERE status='active';

CREATE TABLE plan_model_pricing (
    plan_version_id          UUID NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
    model                    TEXT NOT NULL,
    input_per_mtok           NUMERIC(30,12) CHECK (input_per_mtok IS NULL OR input_per_mtok >= 0),
    cached_input_per_mtok    NUMERIC(30,12) CHECK (cached_input_per_mtok IS NULL OR cached_input_per_mtok >= 0),
    output_per_mtok          NUMERIC(30,12) CHECK (output_per_mtok IS NULL OR output_per_mtok >= 0),
    rate_multiplier          NUMERIC(30,12) CHECK (rate_multiplier IS NULL OR rate_multiplier >= 0),
    notes                    TEXT NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(plan_version_id, model),
    CHECK (length(trim(model)) > 0),
    CHECK (input_per_mtok IS NOT NULL OR cached_input_per_mtok IS NOT NULL OR output_per_mtok IS NOT NULL OR rate_multiplier IS NOT NULL)
);

ALTER TABLE requests ADD COLUMN IF NOT EXISTS upstream_model TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS plan_version_id UUID REFERENCES plan_versions(id);
ALTER TABLE requests ADD COLUMN IF NOT EXISTS model_pricing_source TEXT NOT NULL DEFAULT 'catalog';
ALTER TABLE usage_ledger ADD COLUMN IF NOT EXISTS pricing_base_cost NUMERIC(30,12) NOT NULL DEFAULT 0 CHECK (pricing_base_cost >= 0);
UPDATE usage_ledger SET pricing_base_cost=base_cost WHERE pricing_base_cost=0 AND base_cost>0;
