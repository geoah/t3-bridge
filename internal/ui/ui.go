// Package ui serves a minimal monitoring page for the daemon: an embedded
// HTML page that tails the daemon's log events over SSE. Every slog record
// becomes an event via the Handler in log.go.
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
)

// Event is one log line, shown as one row in the UI.
type Event struct {
	Seq   int64             `json:"seq"`
	Time  string            `json:"t"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

const bufferSize = 500

// Bus fans events out to SSE subscribers and keeps a replay ring buffer.
type Bus struct {
	mu     sync.Mutex
	seq    int64
	buf    []Event
	subs   map[int64]chan Event
	nextID int64
}

func NewBus() *Bus {
	return &Bus{subs: map[int64]chan Event{}}
}

func (b *Bus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ev.Seq = b.seq
	b.buf = append(b.buf, ev)
	if len(b.buf) > bufferSize {
		b.buf = b.buf[len(b.buf)-bufferSize:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // slow subscriber; drop rather than block logging
		}
	}
}

// subscribe returns events after seq from the buffer plus a live channel.
func (b *Bus) subscribe(afterSeq int64) (replay []Event, ch chan Event, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ev := range b.buf {
		if ev.Seq > afterSeq {
			replay = append(replay, ev)
		}
	}
	ch = make(chan Event, 64)
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	return replay, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, id)
	}
}

// Server is the HTTP UI. Serve() blocks.
type Server struct {
	Bus    *Bus
	Listen string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML)
	})
	mux.HandleFunc("GET /events", s.events)
	return mux
}

func (s *Server) Serve() error {
	return (&http.Server{Addr: s.Listen, Handler: s.Handler()}).ListenAndServe()
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	// EventSource reconnects send Last-Event-ID; replay only newer events.
	afterSeq, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	replay, ch, cancel := s.Bus.subscribe(afterSeq)
	defer cancel()

	write := func(ev Event) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data); err != nil {
			return false
		}
		return true
	}
	for _, ev := range replay {
		if !write(ev) {
			return
		}
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !write(ev) {
				return
			}
			flusher.Flush()
		}
	}
}
