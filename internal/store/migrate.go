package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "identity_and_operation_ledger",
		sql: `
CREATE TABLE device_identities (
    alias         TEXT PRIMARY KEY NOT NULL CHECK (alias <> ''),
    origin        TEXT NOT NULL UNIQUE CHECK (origin <> ''),
    device_id     TEXT NOT NULL UNIQUE CHECK (device_id <> ''),
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT;

CREATE TABLE operations (
    operation_id           TEXT PRIMARY KEY NOT NULL CHECK (operation_id <> ''),
    request_digest         BLOB NOT NULL CHECK (length(request_digest) = 32),
    device_id              TEXT NOT NULL CHECK (device_id <> ''),
    control_generation     INTEGER NOT NULL CHECK (control_generation >= 0),
    effect                 TEXT NOT NULL CHECK (effect IN ('input', 'power', 'media', 'admin')),
    action                 TEXT NOT NULL CHECK (action <> ''),
    policy_revision        TEXT NOT NULL CHECK (policy_revision <> ''),
    stage                  TEXT NOT NULL CHECK (stage IN (
        'not_sent', 'send_started', 'transport_accepted', 'observation_started',
        'state_observed', 'completed', 'failed', 'ambiguous', 'cancelled'
    )),
    delivery               TEXT NOT NULL CHECK (delivery IN ('not_sent', 'possibly_sent', 'transport_accepted')),
    verification_status    TEXT NOT NULL CHECK (verification_status IN ('not_requested', 'pending', 'observed', 'not_observed')),
    observation_id         TEXT,
    terminal_claim         TEXT NOT NULL,
    retry_safe             INTEGER NOT NULL CHECK (retry_safe IN (0, 1)),
    error_kind             TEXT,
    created_at_ms          INTEGER NOT NULL,
    updated_at_ms          INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    send_started_at_ms     INTEGER,
    terminal_at_ms         INTEGER,
    CHECK (stage NOT IN (
        'send_started', 'transport_accepted', 'observation_started',
        'state_observed', 'completed', 'ambiguous'
    ) OR send_started_at_ms IS NOT NULL),
    CHECK (stage <> 'not_sent' OR send_started_at_ms IS NULL),
    CHECK ((stage IN ('completed', 'failed', 'ambiguous', 'cancelled')) = (terminal_at_ms IS NOT NULL))
) STRICT;

CREATE INDEX operations_device_updated_idx ON operations(device_id, updated_at_ms DESC);
CREATE INDEX operations_terminal_retention_idx ON operations(terminal_at_ms) WHERE terminal_at_ms IS NOT NULL;

CREATE TABLE operation_signals (
    operation_id TEXT NOT NULL REFERENCES operations(operation_id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL CHECK (ordinal >= 0),
    signal       TEXT NOT NULL CHECK (signal <> ''),
    PRIMARY KEY (operation_id, ordinal)
) STRICT;

CREATE TABLE operation_warnings (
    operation_id TEXT NOT NULL REFERENCES operations(operation_id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL CHECK (ordinal >= 0),
    warning      TEXT NOT NULL CHECK (warning <> ''),
    PRIMARY KEY (operation_id, ordinal)
) STRICT;`,
	},
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY NOT NULL,
			name       TEXT NOT NULL,
			checksum   BLOB NOT NULL CHECK (length(checksum) = 32),
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		) STRICT`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	var newestVersion sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&newestVersion); err != nil {
		return fmt.Errorf("read newest schema migration: %w", err)
	}
	if newestVersion.Valid && newestVersion.Int64 > int64(migrations[len(migrations)-1].version) {
		return fmt.Errorf("database schema version %d is newer than this binary supports", newestVersion.Int64)
	}

	for _, migration := range migrations {
		var existingName string
		var existingChecksum []byte
		expectedChecksum := sha256.Sum256([]byte(migration.sql))
		err := tx.QueryRowContext(ctx,
			"SELECT name, checksum FROM schema_migrations WHERE version = ?", migration.version).
			Scan(&existingName, &existingChecksum)
		switch {
		case err == nil:
			if existingName != migration.name {
				return fmt.Errorf("migration %d name mismatch: database=%q binary=%q", migration.version, existingName, migration.name)
			}
			if len(existingChecksum) != sha256.Size || !bytes.Equal(existingChecksum, expectedChecksum[:]) {
				return fmt.Errorf("migration %d checksum mismatch", migration.version)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read migration %d: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)",
			migration.version, migration.name, expectedChecksum[:]); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
