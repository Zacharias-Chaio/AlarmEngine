package logging

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// Runtime replaces the global structured logger when editable settings change.
type Runtime struct {
	mu      sync.Mutex
	handler *reloadHandler
	closer  io.Closer
}

func NewRuntime(config Config) (*Runtime, error) {
	handler, closer, err := newHandler(config)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{handler: &reloadHandler{state: &reloadHandlerState{handler: handler}}, closer: closer}
	slog.SetDefault(slog.New(runtime.handler))
	return runtime, nil
}

func (r *Runtime) Apply(config Config) error {
	handler, closer, err := newHandler(config)
	if err != nil {
		return err
	}
	r.mu.Lock()
	previous := r.closer
	r.handler.Replace(handler)
	r.closer = closer
	r.mu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	closer := r.closer
	r.closer = nil
	r.handler.Replace(slog.NewTextHandler(io.Discard, nil))
	r.mu.Unlock()
	if closer != nil {
		return closer.Close()
	}
	return nil
}

type reloadHandler struct {
	state  *reloadHandlerState
	attrs  []slog.Attr
	groups []string
}

type reloadHandlerState struct {
	mu      sync.RWMutex
	handler slog.Handler
}

func (h *reloadHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.state.mu.RLock()
	defer h.state.mu.RUnlock()
	return h.state.handler.Enabled(ctx, level)
}

func (h *reloadHandler) Handle(ctx context.Context, record slog.Record) error {
	h.state.mu.RLock()
	defer h.state.mu.RUnlock()
	handler := h.state.handler
	if len(h.attrs) > 0 {
		handler = handler.WithAttrs(h.attrs)
	}
	for _, group := range h.groups {
		handler = handler.WithGroup(group)
	}
	return handler.Handle(ctx, record)
}

func (h *reloadHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &reloadHandler{state: h.state, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...), groups: append([]string(nil), h.groups...)}
}

func (h *reloadHandler) WithGroup(name string) slog.Handler {
	return &reloadHandler{state: h.state, attrs: append([]slog.Attr(nil), h.attrs...), groups: append(append([]string(nil), h.groups...), name)}
}

func (h *reloadHandler) Replace(handler slog.Handler) {
	h.state.mu.Lock()
	h.state.handler = handler
	h.state.mu.Unlock()
}
