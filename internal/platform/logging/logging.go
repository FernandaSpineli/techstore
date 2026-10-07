// Package logging configures the structured JSON logger and carries a
// request-scoped logger through context.
package logging

import (
	"context"
	"io"
	"log/slog"
)

// New returns a JSON logger. The message key is renamed to "event" because
// every log line names something that happened (e.g. "order.created").
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.MessageKey {
				a.Key = "event"
			}
			return a
		},
	})
	return slog.New(h)
}

type ctxKey struct{}

// WithLogger returns a copy of ctx carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger stored in ctx, or slog.Default if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
