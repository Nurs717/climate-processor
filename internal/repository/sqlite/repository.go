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

// readerConns is the size of the read-only connection pool.
const readerConns = 4

// Repository is a SQLite-backed store of readings. It is safe for concurrent use.
//
// Writes and reads use separate connection pools. SQLite allows a single writer, so the writer pool has
// one connection (no "database is locked"); in WAL mode readers do not block it, so a slow history query
// from the API can never delay saving a reading from the sensor.
type Repository struct {
	writer *sql.DB // one connection: Save and migrations
	reader *sql.DB // read-only connections: Range, Aggregate, Summary
}

// New opens (and creates, if needed) the database at path and applies pending migrations.
func New(ctx context.Context, path string) (*Repository, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("sqlite: invalid database path %q", path)
	}
	// WAL: readers do not block the writer (the mode is stored in the file, so readers need not set it).
	// NORMAL sync is safe with WAL (a power loss can lose the last transaction, never corrupt the file).
	// busy_timeout: wait for a lock instead of failing. query_only: the reader pool cannot modify data.
	const common = "?_pragma=busy_timeout(5000)"
	writer, err := openPool(ctx, "file:"+path+common+"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", 1)
	if err != nil {
		return nil, fmt.Errorf("sqlite: writer: %w", err)
	}
	if err := migrate(ctx, writer); err != nil {
		_ = writer.Close()
		return nil, err
	}
	reader, err := openPool(ctx, "file:"+path+common+"&_pragma=query_only(1)", readerConns)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("sqlite: reader: %w", err)
	}
	return &Repository{writer: writer, reader: reader}, nil
}

// openPool opens a pool of at most maxConns connections and checks that the database is reachable.
func openPool(ctx context.Context, dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns) // keep connections: reopening a SQLite file for every query is wasteful
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes both pools. Closing the last connection checkpoints the WAL file.
func (r *Repository) Close() error {
	return errors.Join(r.reader.Close(), r.writer.Close())
}

// Save stores a reading. A second reading of the same device within the same second replaces the first.
func (r *Repository) Save(ctx context.Context, d domain.ClimateData) error {
	if d.DeviceID == "" {
		return errors.New("sqlite: empty device id")
	}
	_, err := r.writer.ExecContext(ctx, `
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
	rows, err := r.reader.QueryContext(ctx, `
		SELECT ts, temperature, humidity, pressure FROM readings
		WHERE device_id = ? AND ts >= ? AND ts < ?
		ORDER BY ts`,
		deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("sqlite: query range: %w", err)
	}
	defer func() { _ = rows.Close() }()

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

// Aggregate groups the readings of a device with from <= time < to into windows of step (whole seconds,
// at least one) and returns the average, minimum and maximum of every quantity per window, oldest first.
// Windows are aligned to the Unix epoch, so the first one may start before from. Windows without readings
// are omitted: a gap in the result is a gap in the data.
func (r *Repository) Aggregate(ctx context.Context, deviceID string, from, to time.Time, step time.Duration) ([]domain.Bucket, error) {
	seconds := int64(step / time.Second)
	if seconds < 1 || step%time.Second != 0 {
		return nil, fmt.Errorf("sqlite: step must be a whole number of seconds, at least 1s, got %s", step)
	}
	// ts / ? * ? is integer division: it rounds ts down to the start of its window.
	rows, err := r.reader.QueryContext(ctx, `
		SELECT ts / ? * ? AS bucket, COUNT(*),
		       AVG(temperature), MIN(temperature), MAX(temperature),
		       AVG(humidity),    MIN(humidity),    MAX(humidity),
		       AVG(pressure),    MIN(pressure),    MAX(pressure)
		FROM readings
		WHERE device_id = ? AND ts >= ? AND ts < ?
		GROUP BY bucket
		ORDER BY bucket`,
		seconds, seconds, deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("sqlite: aggregate: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Bucket
	for rows.Next() {
		var (
			start int64
			b     domain.Bucket
		)
		if err := rows.Scan(&start, &b.Count,
			&b.Temperature.Avg, &b.Temperature.Min, &b.Temperature.Max,
			&b.Humidity.Avg, &b.Humidity.Min, &b.Humidity.Max,
			&b.Pressure.Avg, &b.Pressure.Min, &b.Pressure.Max); err != nil {
			return nil, fmt.Errorf("sqlite: scan aggregate: %w", err)
		}
		b.Start = time.Unix(start, 0).UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate aggregate: %w", err)
	}
	return out, nil
}

// Summary aggregates all readings of a device with from <= time < to. Without readings Count is 0.
func (r *Repository) Summary(ctx context.Context, deviceID string, from, to time.Time) (domain.Summary, error) {
	// An aggregate query always returns one row; AVG/MIN/MAX are NULL when nothing matched, hence COALESCE.
	var s domain.Summary
	err := r.reader.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(AVG(temperature), 0), COALESCE(MIN(temperature), 0), COALESCE(MAX(temperature), 0),
		       COALESCE(AVG(humidity), 0),    COALESCE(MIN(humidity), 0),    COALESCE(MAX(humidity), 0),
		       COALESCE(AVG(pressure), 0),    COALESCE(MIN(pressure), 0),    COALESCE(MAX(pressure), 0)
		FROM readings
		WHERE device_id = ? AND ts >= ? AND ts < ?`,
		deviceID, from.Unix(), to.Unix()).Scan(&s.Count,
		&s.Temperature.Avg, &s.Temperature.Min, &s.Temperature.Max,
		&s.Humidity.Avg, &s.Humidity.Min, &s.Humidity.Max,
		&s.Pressure.Avg, &s.Pressure.Min, &s.Pressure.Max)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("sqlite: summary: %w", err)
	}
	return s, nil
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
