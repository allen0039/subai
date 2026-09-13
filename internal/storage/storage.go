// Package storage provides the PostgreSQL connection, numbered migrations
// (D-005) and the secret box used to encrypt credentials at rest (§22).
package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
	box  cipher.AEAD
}

func Open(ctx context.Context, databaseURL, masterKeyHex string) (*DB, error) {
	key, err := hex.DecodeString(strings.TrimSpace(masterKeyHex))
	if err != nil || len(key) != 32 {
		return nil, errors.New("SUBAI_MASTER_KEY must be 64 hex chars (32 bytes)")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	box, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &DB{Pool: pool, box: box}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// Encrypt seals a secret value; Decrypt opens it. Plaintext never leaves the
// process boundary unencrypted and is never logged (§22, review item 5).
func (d *DB) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, d.box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return d.box.Seal(nonce, nonce, plain, nil), nil
}

func (d *DB) Decrypt(sealed []byte) ([]byte, error) {
	if len(sealed) < d.box.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	plain, err := d.box.Open(nil, sealed[:d.box.NonceSize()], sealed[d.box.NonceSize():], nil)
	if err != nil {
		return nil, err
	}
	// P2-05: Return a copy and zero the GCM output slice to limit heap exposure.
	// The caller receives the copy and must zero it when done (credentials, tokens).
	result := make([]byte, len(plain))
	copy(result, plain)
	for i := range plain {
		plain[i] = 0
	}
	return result, nil
}

func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Migrate applies migrations/NNNN_*.sql in order inside one transaction each.
// There is no automatic downgrade; restore from backup instead (D-005).
// P2-03: validate entire directory structure before executing any SQL, and
// store/verify migration checksums to detect historical tampering.
func (d *DB) Migrate(ctx context.Context, dir string) error {
	if _, err := d.Pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(
			version INT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	// P0-02 Bootstrap: Ensure checksum column exists before any SELECT checksum queries.
	// Old databases (pre-0006) won't have it; new installations already do.
	// This runs BEFORE Phase 1 (loading migrations) and critically BEFORE Phase 2
	// (verifying checksums at line 155-156), preventing "column does not exist" errors.
	var hasChecksumColumn bool
	err := d.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_name='schema_migrations' AND column_name='checksum'
		)`).Scan(&hasChecksumColumn)
	if err != nil {
		return fmt.Errorf("check checksum column existence: %w", err)
	}
	if !hasChecksumColumn {
		// Add checksum column with 'legacy' default for existing migration rows
		if _, err := d.Pool.Exec(ctx, `
			ALTER TABLE schema_migrations
			ADD COLUMN checksum TEXT NOT NULL DEFAULT 'legacy'`); err != nil {
			return fmt.Errorf("bootstrap checksum column: %w", err)
		}
		// Record the bootstrap event for audit trail
		if _, err := d.Pool.Exec(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES(-1, 'checksum_bootstrap', 'bootstrap', now())
			ON CONFLICT (version) DO NOTHING`); err != nil {
			return fmt.Errorf("record checksum bootstrap: %w", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") && !strings.HasSuffix(e.Name(), ".down.sql") {
			files = append(files, e.Name())
		}
	}
	// Sort numerically by version, not lexicographically
	sort.Slice(files, func(i, j int) bool {
		var vi, vj int
		fmt.Sscanf(files[i], "%d_", &vi)
		fmt.Sscanf(files[j], "%d_", &vj)
		return vi < vj
	})

	// P2-03: Phase 1 - validate entire directory before executing anything
	type migration struct {
		version  int
		name     string
		filename string
		checksum string
		content  []byte
	}
	var migrations []migration
	seenVersions := make(map[int]bool)
	for _, f := range files {
		var version int
		var name string
		if _, err := fmt.Sscanf(f, "%d_%s", &version, &name); err != nil {
			return fmt.Errorf("migration file %q must be NNNN_name.sql: %w", f, err)
		}
		name = strings.TrimSuffix(name, ".sql")

		// Reject duplicate version numbers upfront
		if seenVersions[version] {
			return fmt.Errorf("duplicate migration version %d in file %q", version, f)
		}
		seenVersions[version] = true

		sqlBytes, err := fs.ReadFile(os.DirFS(dir), f)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(sqlBytes))
		migrations = append(migrations, migration{
			version:  version,
			name:     name,
			filename: f,
			checksum: checksum,
			content:  sqlBytes,
		})
	}

	// P2-03: Phase 2 - verify historical migrations haven't been tampered with
	for _, m := range migrations {
		var appliedChecksum string
		err := d.Pool.QueryRow(ctx, `
			SELECT checksum FROM schema_migrations WHERE version=$1`, m.version).Scan(&appliedChecksum)
		if err != nil && err.Error() != "no rows in result set" {
			return err
		}
		// Skip verification for legacy migrations (upgraded databases) and bootstrap marker
		if appliedChecksum == "legacy" || appliedChecksum == "bootstrap" {
			continue
		}
		if appliedChecksum != "" && appliedChecksum != m.checksum {
			return fmt.Errorf("migration %s checksum mismatch: applied=%s current=%s (historical tampering detected)",
				m.filename, appliedChecksum, m.checksum)
		}
	}

	// Phase 3 - execute unapplied migrations
	for _, m := range migrations {
		var applied bool
		err := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, m.version).Scan(&applied)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(m.content)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", m.filename, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`,
			m.version, m.name, m.checksum); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
