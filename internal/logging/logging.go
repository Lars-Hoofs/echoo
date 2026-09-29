// Package logging makes every log line of a request carry its request ID and keeps email
// addresses out of log output, whatever the caller passed in.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
)

type ctxKey struct{}

// WithRequestID stores the request ID for handlers that log with the request's context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the ID stored by WithRequestID, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// address matches what looks like an email address. Errors from mail servers routinely quote
// the recipient ("550 5.1.1 <jan@example.com> user unknown"), and nothing at the call site can
// know that, so the handler removes them.
var address = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

// Scrub replaces email addresses in s.
func Scrub(s string) string { return address.ReplaceAllString(s, "[address]") }

type handler struct{ inner slog.Handler }

// NewHandler wraps inner: request_id is added to records logged with a request context (unless
// the caller already passed one), and email addresses in the message and in string, error,
// fmt.Stringer and []string values are replaced.
func NewHandler(inner slog.Handler) slog.Handler { return handler{inner} }

func (h handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Scrub(r.Message), r.PC)
	hasID := false
	r.Attrs(func(a slog.Attr) bool {
		hasID = hasID || a.Key == "request_id"
		out.AddAttrs(scrubAttr(a))
		return true
	})
	if id := RequestID(ctx); id != "" && !hasID {
		out.AddAttrs(slog.String("request_id", id))
	}
	return h.inner.Handle(ctx, out)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = scrubAttr(a)
	}
	return handler{h.inner.WithAttrs(scrubbed)}
}

func (h handler) WithGroup(name string) slog.Handler { return handler{h.inner.WithGroup(name)} }

func scrubAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Scrub(v.String()))
	case slog.KindGroup:
		group := v.Group()
		scrubbed := make([]any, len(group))
		for i, g := range group {
			scrubbed[i] = scrubAttr(g)
		}
		return slog.Group(a.Key, scrubbed...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, Scrub(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, Scrub(x.String()))
		case []string:
			out := make([]string, len(x))
			for i, s := range x {
				out[i] = Scrub(s)
			}
			return slog.Any(a.Key, out)
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
