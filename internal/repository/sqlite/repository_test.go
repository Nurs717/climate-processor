package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"climate-processor/internal/domain"
)

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	r, err := New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func reading(device string, ts time.Time, temp float64) domain.ClimateData {
	return domain.ClimateData{DeviceID: device, Temperature: temp, Humidity: 45, Pressure: 101325, Timestamp: ts}
}

func TestSaveAndRange(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

	for i, temp := range []float64{20, 21, 22} {
		if err := r.Save(ctx, reading("room", t0.Add(time.Duration(i)*time.Minute), temp)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Save(ctx, reading("garage", t0, 5)); err != nil {
		t.Fatal(err)
	}

	// Half-open range: the first minute is included, the third one is not.
	got, err := r.Range(ctx, "room", t0, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d readings, want 2: %+v", len(got), got)
	}
	if got[0].Temperature != 20 || got[1].Temperature != 21 {
		t.Errorf("readings are not ordered by time: %+v", got)
	}
	if got[0].DeviceID != "room" || !got[0].Timestamp.Equal(t0) || got[0].Timestamp.Location() != time.UTC {
		t.Errorf("unexpected first reading: %+v", got[0])
	}
}

func TestRangeOfUnknownDeviceIsEmpty(t *testing.T) {
	r := newTestRepository(t)
	now := time.Now()

	got, err := r.Range(context.Background(), "nobody", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no readings, got %+v", got)
	}
}

func TestSaveSameSecondOverwrites(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	ts := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

	if err := r.Save(ctx, reading("room", ts, 20)); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, reading("room", ts, 25)); err != nil {
		t.Fatal(err)
	}

	got, err := r.Range(ctx, "room", ts, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Temperature != 25 {
		t.Errorf("expected one reading with the latest value, got %+v", got)
	}
}

func TestSaveRejectsEmptyDeviceID(t *testing.T) {
	r := newTestRepository(t)

	if err := r.Save(context.Background(), reading("", time.Now(), 20)); err == nil {
		t.Fatal("expected an error for an empty device id")
	}
}

func TestDataSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	ts := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

	r, err := New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, reading("room", ts, 21)); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Opening an existing database must not re-run migrations or lose data.
	r, err = New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := r.Range(ctx, "room", ts, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Temperature != 21 {
		t.Errorf("data lost after reopen: %+v", got)
	}
}

func TestSchemaVersionMatchesMigrations(t *testing.T) {
	r := newTestRepository(t)

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := r.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(entries) {
		t.Errorf("user_version = %d, want %d (one per migration file)", version, len(entries))
	}
}
