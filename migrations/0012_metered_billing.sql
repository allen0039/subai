-- Sub2API-style metered billing: plan pricing multipliers and auditable
-- separation between catalog cost and the amount consumed by a subscription.

ALTER TABLE price_versions DROP CONSTRAINT IF EXISTS price_versions_origin_check;
ALTER TABLE price_versions ADD CONSTRAINT price_versions_origin_check
    CHECK (origin IN ('synthetic','official','manual','catalog'));

ALTER TABLE plan_versions
    ADD COLUMN rate_multiplier NUMERIC(30,12) NOT NULL DEFAULT 1
        CHECK (rate_multiplier >= 0);

ALTER TABLE requests
    ADD COLUMN rate_multiplier NUMERIC(30,12) NOT NULL DEFAULT 1
        CHECK (rate_multiplier >= 0);

ALTER TABLE reservations
    ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'strict_reservation'
        CHECK (billing_mode IN ('strict_reservation','metered'));

ALTER TABLE usage_ledger
    ADD COLUMN base_cost NUMERIC(30,12) NOT NULL DEFAULT 0
        CHECK (base_cost >= 0),
    ADD COLUMN rate_multiplier NUMERIC(30,12) NOT NULL DEFAULT 1
        CHECK (rate_multiplier >= 0);

ALTER TABLE model_prices
    ADD COLUMN is_manual_override BOOLEAN NOT NULL DEFAULT false;

UPDATE usage_ledger SET base_cost = cost WHERE base_cost = 0 AND cost > 0;
