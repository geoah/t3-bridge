// Package ui serves a minimal monitoring page for the daemon: an embedded
// HTML page that tails the daemon's log events over SSE. Every slog record
// becomes an event via the Handler in log.go. Tick status is broadcast as a
// transient named SSE event and shown in the page header, not in the log.
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Event is one log line, shown as one row in the UI.
type Event struct {
	Seq   int64             `json:"seq"`
	Time  string            `json:"t"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// TickStatus is shown in the page header.
type TickStatus struct {
	Last string `json:"last"`
	Next string `json:"next"`
}

// sseMsg is one wire frame: an optional named event with a payload, plus an
// id for log events so reconnects can resume via Last-Event-ID.
type sseMsg struct {
	id    int64
	event string
	data  []byte
}

const bufferSize = 500

// Bus fans events out to SSE subscribers and keeps a replay ring buffer.
type Bus struct {
	mu     sync.Mutex
	seq    int64
	buf    []Event
	subs   map[int64]chan sseMsg
	nextID int64
	tick   *TickStatus
}

func NewBus() *Bus {
	return &Bus{subs: map[int64]chan sseMsg{}}
}

func (b *Bus) broadcastLocked(msg sseMsg) {
	for _, ch := range b.subs {
		select {
		case ch <- msg:
		default: // slow subscriber; drop rather than block logging
		}
	}
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
	b.broadcastLocked(logMsg(ev))
}

// SetTick records the last tick time and the expected next one, and pushes
// the update to connected pages. Not stored in the replay buffer.
func (b *Bus) SetTick(last, next time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick = &TickStatus{
		Last: last.UTC().Format(time.RFC3339),
		Next: next.UTC().Format(time.RFC3339),
	}
	b.broadcastLocked(tickMsg(*b.tick))
}

func logMsg(ev Event) sseMsg {
	data, _ := json.Marshal(ev)
	return sseMsg{id: ev.Seq, data: data}
}

func tickMsg(t TickStatus) sseMsg {
	data, _ := json.Marshal(t)
	return sseMsg{event: "tick", data: data}
}

// subscribe returns log events after seq plus the current tick status, and a
// live channel.
func (b *Bus) subscribe(afterSeq int64) (replay []sseMsg, ch chan sseMsg, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ev := range b.buf {
		if ev.Seq > afterSeq {
			replay = append(replay, logMsg(ev))
		}
	}
	if b.tick != nil {
		replay = append(replay, tickMsg(*b.tick))
	}
	ch = make(chan sseMsg, 64)
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

	write := func(msg sseMsg) bool {
		if msg.event != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", msg.event); err != nil {
				return false
			}
		}
		if msg.id != 0 {
			if _, err := fmt.Fprintf(w, "id: %d\n", msg.id); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", msg.data); err != nil {
			return false
		}
		return true
	}
	for _, msg := range replay {
		if !write(msg) {
			return
		}
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if !write(msg) {
				return
			}
			flusher.Flush()
		}
	}
}
