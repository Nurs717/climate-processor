// Package logger builds the application's structured logger (log/slog) and carries
// request-scoped attributes (request id, ...) through the context.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// Output formats.
const (
	FormatJSON = "json" // one JSON object per line: production, parsed by log pipelines
	FormatText = "text" // key=value lines: local development
)

// New returns a logger that writes to w. Unknown formats fall back to JSON.
// Every record written with a context made by With carries that context's attributes.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	var h slog.Handler
	if strings.EqualFold(format, FormatText) {
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: renameMessage})
	}
	return slog.New(contextHandler{h})
}

// ParseLevel converts "debug", "info", "warn" or "error" (any case) into a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("unknown log level %q (use debug, info, warn or error)", s)
	}
	return l, nil
}

type ctxKey struct{}

// With returns a context whose attributes are added to every log record written with it.
// Attributes of the parent context are kept.
func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	parent, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return context.WithValue(ctx, ctxKey{}, append(slices.Clone(parent), attrs...))
}

// contextHandler adds the attributes stored by With to each record.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

// renameMessage renames the built-in "msg" key to "message", the name log pipelines expect.
// The level key keeps its slog name ("level": "INFO", "WARN", ...), and the time stays in UTC under "time".
func renameMessage(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.MessageKey {
		a.Key = "message"
	}
	return a
}
