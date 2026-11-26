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

// saveAll stores the readings of a device, one per entry of temps, offsets[i] after t0.
func saveAll(t *testing.T, r *Repository, device string, t0 time.Time, offsets []time.Duration, temps []float64) {
	t.Helper()
	for i, off := range offsets {
		if err := r.Save(context.Background(), reading(device, t0.Add(off), temps[i])); err != nil {
			t.Fatal(err)
		}
	}
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
	defer func() { _ = r.Close() }()

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
	if err := r.writer.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(entries) {
		t.Errorf("user_version = %d, want %d (one per migration file)", version, len(entries))
	}
}

func TestAggregateGroupsReadingsIntoWindows(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

	// 10:00:00 and 10:00:30 share a window; 10:01:00 starts the next one; 10:02 has no data; 10:03:00 follows.
	saveAll(t, r, "room", t0,
		[]time.Duration{0, 30 * time.Second, time.Minute, 3 * time.Minute},
		[]float64{20, 22, 30, 10})
	// Another device in the same interval must not leak into the result.
	saveAll(t, r, "garage", t0, []time.Duration{0}, []float64{-5})

	got, err := r.Aggregate(ctx, "room", t0, t0.Add(4*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d windows, want 3 (the empty minute is omitted): %+v", len(got), got)
	}

	first := got[0]
	if !first.Start.Equal(t0) || first.Start.Location() != time.UTC || first.Count != 2 {
		t.Errorf("first window = %+v", first)
	}
	if first.Temperature != (domain.Stat{Avg: 21, Min: 20, Max: 22}) {
		t.Errorf("first window temperature = %+v, want avg 21, min 20, max 22", first.Temperature)
	}
	if first.Humidity != (domain.Stat{Avg: 45, Min: 45, Max: 45}) || first.Pressure != (domain.Stat{Avg: 101325, Min: 101325, Max: 101325}) {
		t.Errorf("first window humidity/pressure = %+v / %+v", first.Humidity, first.Pressure)
	}
	if !got[1].Start.Equal(t0.Add(time.Minute)) || got[1].Count != 1 || got[1].Temperature.Avg != 30 {
		t.Errorf("second window = %+v", got[1])
	}
	if !got[2].Start.Equal(t0.Add(3*time.Minute)) || got[2].Temperature.Avg != 10 {
		t.Errorf("third window = %+v", got[2])
	}
}

func TestAggregateIntervalIsHalfOpen(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	saveAll(t, r, "room", t0, []time.Duration{0, time.Minute}, []float64{20, 30})

	// to is exclusive: the reading at exactly t0+1m belongs to the next interval.
	got, err := r.Aggregate(ctx, "room", t0, t0.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 1 || got[0].Temperature.Avg != 20 {
		t.Errorf("got %+v, want only the reading at t0", got)
	}
}

func TestAggregateWindowsAreAlignedToTheEpoch(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	saveAll(t, r, "room", t0, []time.Duration{30 * time.Second, 70 * time.Second}, []float64{20, 30})

	// from falls in the middle of the first window: it starts at 10:00:00, before from.
	got, err := r.Aggregate(ctx, "room", t0.Add(30*time.Second), t0.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Start.Equal(t0) || !got[1].Start.Equal(t0.Add(time.Minute)) {
		t.Errorf("got %+v, want windows starting at 10:00:00 and 10:01:00", got)
	}
}

func TestAggregateOfEmptyIntervalIsEmpty(t *testing.T) {
	r := newTestRepository(t)
	now := time.Now()

	got, err := r.Aggregate(context.Background(), "room", now.Add(-time.Hour), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no windows, got %+v", got)
	}
}

func TestAggregateRejectsInvalidStep(t *testing.T) {
	r := newTestRepository(t)
	now := time.Now()

	for _, step := range []time.Duration{0, -time.Second, 500 * time.Millisecond, 1500 * time.Millisecond} {
		if _, err := r.Aggregate(context.Background(), "room", now.Add(-time.Hour), now, step); err == nil {
			t.Errorf("step %s: expected an error", step)
		}
	}
}

func TestSummary(t *testing.T) {
	ctx := context.Background()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	saveAll(t, r, "room", t0, []time.Duration{0, time.Minute, 2 * time.Minute, 3 * time.Minute}, []float64{20, 22, 30, 99})
	saveAll(t, r, "garage", t0, []time.Duration{0}, []float64{-5})

	got, err := r.Summary(ctx, "room", t0, t0.Add(3*time.Minute)) // the last reading is outside
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 || got.Temperature != (domain.Stat{Avg: 24, Min: 20, Max: 30}) {
		t.Errorf("summary = %+v, want 3 readings, temperature avg 24, min 20, max 30", got)
	}
}

func TestSummaryOfEmptyIntervalIsZero(t *testing.T) {
	r := newTestRepository(t)
	now := time.Now()

	got, err := r.Summary(context.Background(), "room", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if got != (domain.Summary{}) {
		t.Errorf("expected a zero summary, got %+v", got)
	}
}

// A history query that is still reading must not stop the sensor's readings from being saved.
// With one shared connection Save would wait for the open query until its context expires.
func TestSaveDoesNotWaitForAnOpenReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := newTestRepository(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	saveAll(t, r, "room", t0, []time.Duration{0, time.Second}, []float64{20, 21})

	rows, err := r.reader.QueryContext(ctx, "SELECT ts FROM readings")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() { // the reader now holds its connection in the middle of the result
		t.Fatal("expected a row")
	}

	if err := r.Save(ctx, reading("room", t0.Add(time.Minute), 22)); err != nil {
		t.Fatalf("Save was blocked by an open reader: %v", err)
	}
}

func TestReaderPoolIsReadOnly(t *testing.T) {
	r := newTestRepository(t)

	if _, err := r.reader.ExecContext(context.Background(), "DELETE FROM readings"); err == nil {
		t.Fatal("the reader pool must not be able to modify data")
	}
}
