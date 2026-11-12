package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/robdevops/dalibot-gallery/internal/telegram"
)

// safeError is an error's text for the log. Fetcher errors are already
// redacted; the redacting log handler is the second line of defence.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// RedactingHandler wraps a slog.Handler so that no secret can reach the log:
// every occurrence of a secret (plain or URL-escaped) in the message or in any
// attribute value is replaced with "[redacted]". Wrap the process's handler
// with it and pass cfg.Secrets().
func RedactingHandler(h slog.Handler, secrets ...string) slog.Handler {
	var kept []string
	for _, s := range secrets {
		if s != "" {
			kept = append(kept, s)
		}
	}
	return &redactor{inner: h, secrets: kept}
}

type redactor struct {
	inner   slog.Handler
	secrets []string
}

func (r *redactor) Enabled(ctx context.Context, level slog.Level) bool {
	return r.inner.Enabled(ctx, level)
}

func (r *redactor) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, r.text(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(r.attr(a))
		return true
	})
	return r.inner.Handle(ctx, out)
}

func (r *redactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = r.attr(a)
	}
	return &redactor{inner: r.inner.WithAttrs(clean), secrets: r.secrets}
}

func (r *redactor) WithGroup(name string) slog.Handler {
	return &redactor{inner: r.inner.WithGroup(r.text(name)), secrets: r.secrets}
}

func (r *redactor) text(s string) string { return telegram.Redact(s, r.secrets...) }

// attr redacts one attribute. Numbers, booleans, times and durations cannot
// hold a secret; everything else is turned into text and cleaned.
func (r *redactor) attr(a slog.Attr) slog.Attr {
	a.Key = r.text(a.Key)
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(r.text(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = r.attr(g)
		}
		a.Value = slog.GroupValue(clean...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			a.Value = slog.StringValue(r.text(x.Error()))
		case fmt.Stringer:
			a.Value = slog.StringValue(r.text(x.String()))
		default:
			a.Value = slog.StringValue(r.text(fmt.Sprintf("%+v", x)))
		}
	default:
		a.Value = v
	}
	return a
}
