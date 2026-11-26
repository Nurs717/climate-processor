// Package sqlite stores climate readings in a SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"climate-processor/internal/domain"

	_ "modernc.org/sqlite" // pure-Go driver: works with CGO_ENABLED=0
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Repository is a SQLite-backed store of readings. It is safe for concurrent use.
type Repository struct {
	db *sql.DB
}

// New opens (and creates, if needed) the database at path and applies pending migrations.
func New(ctx context.Context, path string) (*Repository, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("sqlite: invalid database path %q", path)
	}
	// WAL: readers do not block the writer. NORMAL sync is safe with WAL (a power loss can lose the
	// last transaction, never corrupt the file). busy_timeout: wait for a lock instead of failing.
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite allows one writer; a single connection removes "database is locked"

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Repository{db: db}, nil
}

// Close closes the database.
func (r *Repository) Close() error { return r.db.Close() }

// Save stores a reading. A second reading of the same device within the same second replaces the first.
func (r *Repository) Save(ctx context.Context, d domain.ClimateData) error {
	if d.DeviceID == "" {
		return errors.New("sqlite: empty device id")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO readings (device_id, ts, temperature, humidity, pressure)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (device_id, ts) DO UPDATE SET
			temperature = excluded.temperature,
			humidity    = excluded.humidity,
			pressure    = excluded.pressure`,
		d.DeviceID, d.Timestamp.Unix(), d.Temperature, d.Humidity, d.Pressure)
	if err != nil {
		return fmt.Errorf("sqlite: save reading: %w", err)
	}
	return nil
}

// Range returns the readings of a device with from <= time < to, oldest first. Times are in UTC.
func (r *Repository) Range(ctx context.Context, deviceID string, from, to time.Time) ([]domain.ClimateData, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ts, temperature, humidity, pressure FROM readings
		WHERE device_id = ? AND ts >= ? AND ts < ?
		ORDER BY ts`,
		deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("sqlite: query range: %w", err)
	}
	defer rows.Close()

	var out []domain.ClimateData
	for rows.Next() {
		var ts int64
		d := domain.ClimateData{DeviceID: deviceID}
		if err := rows.Scan(&ts, &d.Temperature, &d.Humidity, &d.Pressure); err != nil {
			return nil, fmt.Errorf("sqlite: scan: %w", err)
		}
		d.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate: %w", err)
	}
	return out, nil
}

// migrate applies the embedded migrations newer than the database's PRAGMA user_version.
// File names look like 0001_init.sql; the number before the first "_" is the schema version.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("sqlite: read schema version: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations") // sorted by file name
	if err != nil {
		return fmt.Errorf("sqlite: list migrations: %w", err)
	}
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("sqlite: bad migration name %q: %w", e.Name(), err)
		}
		if version <= current {
			continue
		}
		script, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("sqlite: read migration %s: %w", e.Name(), err)
		}
		if err := applyMigration(ctx, db, version, string(script)); err != nil {
			return fmt.Errorf("sqlite: migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// applyMigration runs the script and bumps user_version in one transaction.
func applyMigration(ctx context.Context, db *sql.DB, version int, script string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}
