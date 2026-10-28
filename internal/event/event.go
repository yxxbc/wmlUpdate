// Package event defines the stable event stream shared by the CLI JSON mode
// and the local HTTP API. Command line integrations should treat this as a
// public protocol; see docs/integration.md.
package event

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Type identifies an event's payload.
type Type string

const (
	TypeProgress Type = "progress"
	TypeLog      Type = "log"
	TypeWarning  Type = "warning"
	TypeResult   Type = "result"
	TypeError    Type = "error"
)

// Event is emitted as one JSON object per line in JSON mode.
type Event struct {
	Type    Type      `json:"type"`
	Time    time.Time `json:"time"`
	Phase   string    `json:"phase,omitempty"`
	Message string    `json:"message,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	Total   int64     `json:"total,omitempty"`
	Percent float64   `json:"percent,omitempty"`
	Data    any       `json:"data,omitempty"`
	Error   *Error    `json:"error,omitempty"`
}

// Error is a machine-readable failure description.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// Sink receives events.
type Sink interface {
	Emit(Event)
}

// FuncSink adapts a function to Sink.
type FuncSink func(Event)

func (f FuncSink) Emit(e Event) { f(e) }

// NopSink discards events.
type NopSink struct{}

func (NopSink) Emit(Event) {}

// Buffer is a concurrency-safe bounded event buffer. It is used by API jobs so
// progress can be polled without unbounded memory growth.
type Buffer struct {
	mu     sync.Mutex
	events []Event
	max    int
}

func NewBuffer(max int) *Buffer {
	if max <= 0 {
		max = 256
	}
	return &Buffer{max: max}
}

func (b *Buffer) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) == b.max {
		copy(b.events, b.events[1:])
		b.events[len(b.events)-1] = e
		return
	}
	b.events = append(b.events, e)
}

// Events returns a snapshot of buffered events.
func (b *Buffer) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.events))
	copy(out, b.events)
	return out
}

// Multi fans out events in order.
func Multi(sinks ...Sink) Sink {
	filtered := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return FuncSink(func(e Event) {
		for _, s := range filtered {
			s.Emit(e)
		}
	})
}

// JSONWriter emits one JSON line per event.
type JSONWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSONWriter(w io.Writer) *JSONWriter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &JSONWriter{enc: enc}
}

func (w *JSONWriter) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(e)
}
