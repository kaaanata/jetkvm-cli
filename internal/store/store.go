// Package store implements the single SQLite persistence authority.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	_ "modernc.org/sqlite"
)

var (
	ErrIdentityNotFound = errors.New("device identity pin was not found")
	ErrIdentityConflict = errors.New("device identity pin conflicts with the existing binding")
)

const sqliteDriver = "sqlite"

// Store owns the SQLite connection and all durable state transactions.
type Store struct {
	db *sql.DB
}

// Open opens and migrates a SQLite store. A single connection makes connection-
// local SQLite pragmas authoritative for this first-release, single-process store.
func Open(ctx context.Context, dataSourceName string) (*Store, error) {
	db, err := sql.Open(sqliteDriver, dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("open SQLite store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.initialize(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initialize(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite (%s): %w", statement, err)
		}
	}
	if err := migrate(ctx, s.db); err != nil {
		return fmt.Errorf("migrate SQLite store: %w", err)
	}
	if _, err := s.RecoverInterrupted(ctx, time.Now()); err != nil {
		return fmt.Errorf("recover interrupted operations: %w", err)
	}
	return nil
}

// IdentityPin binds a configured alias and origin to one stable hardware identity.
type IdentityPin struct {
	Alias     string
	Origin    string
	DeviceID  domain.DeviceID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PinIdentity creates an immutable alias/origin/device binding. Repeating the
// exact binding is idempotent; changing any member is a conflict.
func (s *Store) PinIdentity(ctx context.Context, pin IdentityPin, now time.Time) (IdentityPin, bool, error) {
	if pin.Alias == "" || pin.Origin == "" || pin.DeviceID == "" {
		return IdentityPin{}, false, fmt.Errorf("%w: alias, origin, and device ID are required", ErrIdentityConflict)
	}
	now = normalizeTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IdentityPin{}, false, fmt.Errorf("begin identity transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO device_identities(alias, origin, device_id, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, pin.Alias, pin.Origin, pin.DeviceID, toMillis(now), toMillis(now))
	if err != nil {
		return IdentityPin{}, false, fmt.Errorf("insert device identity: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return IdentityPin{}, false, fmt.Errorf("read device identity insert result: %w", err)
	}

	stored, err := getIdentityTx(ctx, tx, pin.Alias)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			return IdentityPin{}, false, fmt.Errorf("%w: origin or device ID is already pinned", ErrIdentityConflict)
		}
		return IdentityPin{}, false, err
	}
	if stored.Origin != pin.Origin || stored.DeviceID != pin.DeviceID {
		return IdentityPin{}, false, ErrIdentityConflict
	}
	if err := tx.Commit(); err != nil {
		return IdentityPin{}, false, fmt.Errorf("commit identity transaction: %w", err)
	}
	return stored, rows == 0, nil
}

// VerifyIdentity checks the configured route and observed hardware ID against
// the durable pin. It never changes the pin on mismatch.
func (s *Store) VerifyIdentity(ctx context.Context, alias, origin string, observed domain.DeviceID) (IdentityPin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT alias, origin, device_id, created_at_ms, updated_at_ms
		FROM device_identities WHERE alias = ?`, alias)
	pin, err := scanIdentity(row)
	if err != nil {
		return IdentityPin{}, err
	}
	if pin.Origin != origin || pin.DeviceID != observed {
		return IdentityPin{}, domain.ErrDeviceIdentityMismatch
	}
	return pin, nil
}

func getIdentityTx(ctx context.Context, tx *sql.Tx, alias string) (IdentityPin, error) {
	return scanIdentity(tx.QueryRowContext(ctx, `
		SELECT alias, origin, device_id, created_at_ms, updated_at_ms
		FROM device_identities WHERE alias = ?`, alias))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIdentity(row rowScanner) (IdentityPin, error) {
	var pin IdentityPin
	var created, updated int64
	if err := row.Scan(&pin.Alias, &pin.Origin, &pin.DeviceID, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdentityPin{}, ErrIdentityNotFound
		}
		return IdentityPin{}, fmt.Errorf("scan device identity: %w", err)
	}
	pin.CreatedAt = fromMillis(created)
	pin.UpdatedAt = fromMillis(updated)
	return pin, nil
}

// Begin atomically registers a request or returns the exact existing receipt.
func (s *Store) Begin(ctx context.Context, request operation.Request, now time.Time) (operation.Receipt, bool, error) {
	if err := request.Validate(); err != nil {
		return operation.Receipt{}, false, err
	}
	now = normalizeTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.Receipt{}, false, fmt.Errorf("begin operation transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO operations(
			operation_id, request_digest, device_id, control_generation, effect, action,
			policy_revision, stage, delivery, verification_status, terminal_claim,
			retry_safe, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(operation_id) DO NOTHING`,
		request.ID.String(), request.Digest[:], request.DeviceID, request.ControlGeneration,
		request.Effect, request.Action, request.PolicyRevision, operation.StageNotSent,
		operation.DeliveryNotSent, operation.VerificationNotRequested, operation.TerminalClaimNotProven, true,
		toMillis(now), toMillis(now))
	if err != nil {
		return operation.Receipt{}, false, fmt.Errorf("insert operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return operation.Receipt{}, false, fmt.Errorf("read operation insert result: %w", err)
	}
	receipt, err := getOperationTx(ctx, tx, request.ID)
	if err != nil {
		return operation.Receipt{}, false, err
	}
	if receipt.Digest != request.Digest {
		return operation.Receipt{}, false, operation.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return operation.Receipt{}, false, fmt.Errorf("commit operation transaction: %w", err)
	}
	return receipt, rows == 0, nil
}

// Get returns the current durable operation receipt.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (operation.Receipt, error) {
	return getOperationRow(ctx, s.db, id)
}

// Transition atomically applies one legal state transition.
func (s *Store) Transition(ctx context.Context, id uuid.UUID, to operation.Stage, patch operation.Patch, now time.Time) (operation.Receipt, error) {
	now = normalizeTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.Receipt{}, fmt.Errorf("begin operation transition: %w", err)
	}
	defer tx.Rollback()

	current, err := getOperationTx(ctx, tx, id)
	if err != nil {
		return operation.Receipt{}, err
	}
	if err := operation.ValidatePatch(current.Stage, to, patch); err != nil {
		return operation.Receipt{}, err
	}

	terminalAt := sql.NullInt64{}
	if to.IsTerminal() {
		terminalAt = sql.NullInt64{Int64: toMillis(now), Valid: true}
	}
	sendStartedAt := nullableMillis(current.SendStartedAt)
	if to == operation.StageSendStarted {
		sendStartedAt = sql.NullInt64{Int64: toMillis(now), Valid: true}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE operations SET
			stage = ?, delivery = ?, verification_status = ?, observation_id = ?,
			terminal_claim = ?, retry_safe = ?, error_kind = ?, updated_at_ms = ?,
			send_started_at_ms = ?, terminal_at_ms = ?
		WHERE operation_id = ? AND stage = ?`,
		to, patch.Delivery, patch.Verification.Status, nullableString(patch.Verification.ObservationID),
		patch.TerminalClaim, patch.RetrySafe, nullableString(patch.ErrorKind), toMillis(now),
		sendStartedAt, terminalAt, id.String(), current.Stage)
	if err != nil {
		return operation.Receipt{}, fmt.Errorf("update operation transition: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return operation.Receipt{}, fmt.Errorf("read operation transition result: %w", err)
	}
	if rows != 1 {
		return operation.Receipt{}, operation.ErrInvalidTransition
	}
	if err := replaceStrings(ctx, tx, "operation_signals", "signal", id, patch.Verification.Signals); err != nil {
		return operation.Receipt{}, err
	}
	if err := replaceStrings(ctx, tx, "operation_warnings", "warning", id, patch.Warnings); err != nil {
		return operation.Receipt{}, err
	}
	receipt, err := getOperationTx(ctx, tx, id)
	if err != nil {
		return operation.Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return operation.Receipt{}, fmt.Errorf("commit operation transition: %w", err)
	}
	return receipt, nil
}

// RecoverInterrupted terminally marks every send interrupted by a prior process crash.
func (s *Store) RecoverInterrupted(ctx context.Context, now time.Time) (int64, error) {
	now = normalizeTime(now)
	result, err := s.db.ExecContext(ctx, `
		UPDATE operations SET
			stage = ?, delivery = ?, verification_status = ?, terminal_claim = ?,
			retry_safe = 0, error_kind = ?, updated_at_ms = ?, terminal_at_ms = ?
		WHERE stage = ?`,
		operation.StageAmbiguous, operation.DeliveryPossiblySent,
		operation.VerificationNotObserved, operation.TerminalClaimNotProven, "process_interrupted_after_send_started",
		toMillis(now), toMillis(now), operation.StageSendStarted)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted operations: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read recovery result: %w", err)
	}
	return rows, nil
}

// PurgeTerminalBefore removes only terminal receipts whose terminal time is older than cutoff.
func (s *Store) PurgeTerminalBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM operations
		WHERE terminal_at_ms IS NOT NULL AND terminal_at_ms < ?`, toMillis(cutoff))
	if err != nil {
		return 0, fmt.Errorf("purge retained operation receipts: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read purge result: %w", err)
	}
	return rows, nil
}

func getOperationTx(ctx context.Context, tx *sql.Tx, id uuid.UUID) (operation.Receipt, error) {
	return getOperationRow(ctx, tx, id)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getOperationRow(ctx context.Context, queryer queryRower, id uuid.UUID) (operation.Receipt, error) {
	row := queryer.QueryRowContext(ctx, `
		SELECT operation_id, request_digest, device_id, control_generation, effect, action,
			policy_revision, stage, delivery, verification_status, observation_id,
			terminal_claim, retry_safe, error_kind, created_at_ms, updated_at_ms,
			send_started_at_ms, terminal_at_ms
		FROM operations WHERE operation_id = ?`, id.String())

	var receipt operation.Receipt
	var operationID string
	var digest []byte
	var observationID, errorKind sql.NullString
	var retrySafe bool
	var created, updated int64
	var sendStarted, terminal sql.NullInt64
	if err := row.Scan(
		&operationID, &digest, &receipt.DeviceID, &receipt.ControlGeneration,
		&receipt.Effect, &receipt.Action, &receipt.PolicyRevision, &receipt.Stage,
		&receipt.Delivery, &receipt.Verification.Status, &observationID,
		&receipt.TerminalClaim, &retrySafe, &errorKind, &created, &updated,
		&sendStarted, &terminal,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operation.Receipt{}, operation.ErrNotFound
		}
		return operation.Receipt{}, fmt.Errorf("scan operation: %w", err)
	}
	parsedID, err := uuid.Parse(operationID)
	if err != nil {
		return operation.Receipt{}, fmt.Errorf("parse stored operation ID: %w", err)
	}
	if len(digest) != sha256.Size {
		return operation.Receipt{}, fmt.Errorf("stored operation digest has length %d", len(digest))
	}
	receipt.ID = parsedID
	copy(receipt.Digest[:], digest)
	receipt.Verification.ObservationID = observationID.String
	receipt.RetrySafe = retrySafe
	receipt.ErrorKind = errorKind.String
	receipt.CreatedAt = fromMillis(created)
	receipt.UpdatedAt = fromMillis(updated)
	receipt.SendStartedAt = timeFromNullableMillis(sendStarted)
	receipt.TerminalAt = timeFromNullableMillis(terminal)

	receipt.Verification.Signals, err = readStrings(ctx, queryer, "operation_signals", "signal", id)
	if err != nil {
		return operation.Receipt{}, err
	}
	receipt.Warnings, err = readStrings(ctx, queryer, "operation_warnings", "warning", id)
	if err != nil {
		return operation.Receipt{}, err
	}
	return receipt, nil
}

func replaceStrings(ctx context.Context, tx *sql.Tx, table, column string, id uuid.UUID, values []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE operation_id = ?", id.String()); err != nil {
		return fmt.Errorf("clear %s: %w", table, err)
	}
	for index, value := range values {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO "+table+"(operation_id, ordinal, "+column+") VALUES (?, ?, ?)",
			id.String(), index, value); err != nil {
			return fmt.Errorf("insert %s: %w", table, err)
		}
	}
	return nil
}

func readStrings(ctx context.Context, queryer queryRower, table, column string, id uuid.UUID) ([]string, error) {
	rows, err := queryer.QueryContext(ctx,
		"SELECT "+column+" FROM "+table+" WHERE operation_id = ? ORDER BY ordinal", id.String())
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", table, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", table, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return values, nil
}

func normalizeTime(value time.Time) time.Time { return value.UTC().Truncate(time.Millisecond) }
func toMillis(value time.Time) int64          { return normalizeTime(value).UnixMilli() }
func fromMillis(value int64) time.Time        { return time.UnixMilli(value).UTC() }

func nullableMillis(value time.Time) sql.NullInt64 {
	if value.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toMillis(value), Valid: true}
}

func timeFromNullableMillis(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return fromMillis(value.Int64)
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}
