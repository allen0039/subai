-- P2-03: Add checksum column to detect historical migration tampering.
-- Existing migrations get a placeholder checksum; new migrations will store actual SHA256.

ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT NOT NULL DEFAULT 'legacy';
COMMENT ON COLUMN schema_migrations.checksum IS 'SHA256 of migration content; legacy for pre-checksum migrations';
