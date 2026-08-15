package ui

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Handler is a slog.Handler that publishes every record to the Bus and
// forwards to an inner handler (typically stderr). The bus receives all
// levels including Debug, so the UI can show heartbeat ticks that would be
// noise in the log file.
type Handler struct {
	inner slog.Handler
	bus   *Bus
	attrs []slog.Attr
}

func NewHandler(inner slog.Handler, bus *Bus) *Handler {
	return &Handler{inner: inner, bus: bus}
}

func (h *Handler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]string{}
	for _, a := range h.attrs {
		attrs[a.Key] = fmt.Sprint(a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	h.bus.Publish(Event{
		Time:  r.Time.UTC().Format(time.RFC3339),
		Level: r.Level.String(),
		Msg:   r.Message,
		Attrs: attrs,
	})
	if h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		inner: h.inner.WithAttrs(attrs),
		bus:   h.bus,
		attrs: append(append([]slog.Attr{}, h.attrs...), attrs...),
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	// The bridge does not use groups; flatten them for the UI.
	return &Handler{inner: h.inner.WithGroup(name), bus: h.bus, attrs: h.attrs}
}
