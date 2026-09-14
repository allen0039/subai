-- A zero key limit means unlimited. New plans default to unlimited while
-- existing configured limits keep their current values.
ALTER TABLE plan_versions
    DROP CONSTRAINT IF EXISTS plan_versions_max_keys_check;

ALTER TABLE plan_versions
    ALTER COLUMN max_keys SET DEFAULT 0,
    ADD CONSTRAINT plan_versions_max_keys_check CHECK (max_keys >= 0);
