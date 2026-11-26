package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestJSONKeys(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo, FormatJSON).Warn("disk almost full", "free_mb", 12)

	m := decode(t, &buf)
	if m["level"] != "WARN" || m["message"] != "disk almost full" || m["free_mb"] != float64(12) {
		t.Errorf("unexpected record: %v", m)
	}
	if _, ok := m["msg"]; ok {
		t.Errorf("the msg key must be renamed to message: %v", m)
	}
	if _, ok := m["time"]; !ok {
		t.Errorf("missing time: %v", m)
	}
}

func TestOnlyTopLevelMessageKeyIsRenamed(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo, FormatJSON).Info("hello", slog.Group("http", slog.String("msg", "kept")))

	m := decode(t, &buf)
	if g, _ := m["http"].(map[string]any); g["msg"] != "kept" {
		t.Errorf("a user attribute named msg inside a group must not be renamed: %v", m)
	}
}

func TestContextAttributesAreAdded(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo, FormatJSON)

	ctx := With(context.Background(), slog.String("request_id", "abc123"))
	ctx = With(ctx, slog.String("role", "owner")) // keeps the parent's attributes
	log.InfoContext(ctx, "request done")

	m := decode(t, &buf)
	if m["request_id"] != "abc123" || m["role"] != "owner" {
		t.Errorf("context attributes are missing: %v", m)
	}
}

func TestWithDoesNotLeakIntoParent(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo, FormatJSON)

	parent := With(context.Background(), slog.String("a", "1"))
	_ = With(parent, slog.String("b", "2"))
	log.InfoContext(parent, "x")

	if m := decode(t, &buf); m["b"] != nil {
		t.Errorf("child attributes leaked into the parent context: %v", m)
	}
}

func TestLevelFiltersRecords(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelWarn, FormatJSON).Info("noise")

	if buf.Len() != 0 {
		t.Errorf("info must be dropped at warn level, got %s", buf.String())
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo, FormatText).Info("hello", "k", "v")

	if got := buf.String(); !strings.Contains(got, "msg=hello") || !strings.Contains(got, "k=v") {
		t.Errorf("unexpected text output: %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "Error": slog.LevelError,
	} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("expected an error for an unknown level")
	}
}
