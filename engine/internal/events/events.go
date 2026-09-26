// Package events is an in-memory publish/subscribe bus for cluster events
// (writes, repairs, corruption, membership). It keeps a bounded history.
package events

import (
	"sync"
	"time"
)

// Severity levels.
const (
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

// Event is a notable occurrence on a node.
type Event struct {
	ID       uint64    `json:"id"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Severity string    `json:"severity"`
	Node     string    `json:"node"`
	Bucket   string    `json:"bucket,omitempty"`
	Key      string    `json:"key,omitempty"`
	Message  string    `json:"message"`
}

// Bus fans events out to subscribers without ever blocking publishers.
type Bus struct {
	mu      sync.Mutex
	node    string
	next    uint64
	history []Event
	limit   int
	subs    map[chan Event]struct{}
}

// NewBus returns a bus that keeps the last limit events.
func NewBus(node string, limit int) *Bus {
	return &Bus{node: node, limit: limit, subs: map[chan Event]struct{}{}}
}

// Publish records and broadcasts an event. Slow subscribers drop events.
func (b *Bus) Publish(typ, severity, bucket, key, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	e := Event{ID: b.next, Time: time.Now().UTC(), Type: typ, Severity: severity, Node: b.node, Bucket: bucket, Key: key, Message: msg}
	b.history = append(b.history, e)
	if len(b.history) > b.limit {
		b.history = b.history[len(b.history)-b.limit:]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Recent returns up to n recent events, oldest first.
func (b *Bus) Recent(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.history) {
		n = len(b.history)
	}
	return append([]Event(nil), b.history[len(b.history)-n:]...)
}

// Subscribe returns a channel of new events and a cancel function.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}
