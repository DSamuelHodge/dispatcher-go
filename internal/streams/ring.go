package streams

import "sync"

// Event is one ring-buffer entry.
type Event struct {
	Seq  uint64 `json:"seq"`
	TS   string `json:"ts"`
	Line string `json:"line"`
}

// Ring is a fixed-capacity sequence ring with O(1) eviction.
type Ring struct {
	mu   sync.RWMutex
	buf  []Event
	cap  int
	next uint64 // next seq to assign (starts at 1)
	// head is index of oldest element when full
	start int
	len   int
}

// NewRing creates a ring of capacity cap (default 128).
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 128
	}
	return &Ring{buf: make([]Event, capacity), cap: capacity}
}

// Push appends a line, assigning the next sequence number.
func (r *Ring) Push(ts, line string) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	ev := Event{Seq: r.next, TS: ts, Line: line}
	if r.len < r.cap {
		r.buf[(r.start+r.len)%r.cap] = ev
		r.len++
	} else {
		r.buf[r.start] = ev
		r.start = (r.start + 1) % r.cap
	}
	return ev
}

// Since returns events with seq > since, in order.
func (r *Ring) Since(since uint64) []Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Event, 0, r.len)
	for i := 0; i < r.len; i++ {
		ev := r.buf[(r.start+i)%r.cap]
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

// Len returns current buffered count.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.len
}
