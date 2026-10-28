package wmlupdate

import "time"

// EventType identifies a callback event.
type EventType string

const (
	EventLog      EventType = "log"
	EventWarning  EventType = "warning"
	EventProgress EventType = "progress"
)

// Event is delivered to Options.Events. It is a read-only progress/log signal;
// the terminal result is returned by Check and Apply.
type Event struct {
	Type    EventType `json:"type"`
	Time    time.Time `json:"time"`
	Phase   string    `json:"phase,omitempty"`
	Message string    `json:"message,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	Total   int64     `json:"total,omitempty"`
	Percent float64   `json:"percent,omitempty"`
}

// EventFunc receives progress and log callbacks. It is called synchronously.
type EventFunc func(Event)
