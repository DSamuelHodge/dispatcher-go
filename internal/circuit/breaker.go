// Package circuit implements per-verb-template circuit breakers (FR-6.4).
package circuit

import (
	"sync"
	"time"
)

// State of a breaker.
type State string

const (
	Closed   State = "closed"
	Open     State = "open"
	HalfOpen State = "half_open"
)

// Breaker is one verb-template breaker.
type Breaker struct {
	mu            sync.Mutex
	state         State
	failures      int
	openedAt      time.Time
	halfOpenTrial bool // true while a half-open probe is in flight
	TripThreshold int
	OpenFor       time.Duration
	now           func() time.Time
}

// New returns a closed breaker.
func New(trip int, openFor time.Duration) *Breaker {
	if trip <= 0 {
		trip = 5
	}
	if openFor <= 0 {
		openFor = 60 * time.Second
	}
	return &Breaker{
		state:         Closed,
		TripThreshold: trip,
		OpenFor:       openFor,
		now:           time.Now,
	}
}

// Snapshot is a read-only view for /v1/health.
type Snapshot struct {
	State    State `json:"state"`
	Failures int   `json:"failures"`
}

// Snapshot returns current state.
func (b *Breaker) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeTransitionLocked()
	return Snapshot{State: b.state, Failures: b.failures}
}

// Allow reports whether a new attempt may start.
// Open → false until OpenFor elapses, then one half-open probe is allowed.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeTransitionLocked()
	switch b.state {
	case Closed:
		return true
	case Open:
		return false
	case HalfOpen:
		if b.halfOpenTrial {
			return false // only one probe
		}
		b.halfOpenTrial = true
		return true
	default:
		return true
	}
}

// Success records a successful execution.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.halfOpenTrial = false
	b.state = Closed
}

// Failure records timeout/failed outcome.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == HalfOpen {
		b.state = Open
		b.openedAt = b.now()
		b.halfOpenTrial = false
		b.failures = b.TripThreshold
		return
	}
	b.failures++
	if b.failures >= b.TripThreshold {
		b.state = Open
		b.openedAt = b.now()
		b.halfOpenTrial = false
	}
}

func (b *Breaker) maybeTransitionLocked() {
	if b.state == Open && b.now().Sub(b.openedAt) >= b.OpenFor {
		b.state = HalfOpen
		b.halfOpenTrial = false
	}
}

// Registry maps verb name (template key) → breaker.
type Registry struct {
	mu       sync.Mutex
	breakers map[string]*Breaker
	trip     int
	openFor  time.Duration
}

// NewRegistry creates an empty registry with defaults.
func NewRegistry(trip int, openFor time.Duration) *Registry {
	if trip <= 0 {
		trip = 5
	}
	if openFor <= 0 {
		openFor = 60 * time.Second
	}
	return &Registry{
		breakers: make(map[string]*Breaker),
		trip:     trip,
		openFor:  openFor,
	}
}

// For returns the breaker for verb, creating it if needed.
// perVerbTrip overrides default when > 0.
func (r *Registry) For(verb string, perVerbTrip int) *Breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.breakers[verb]; ok {
		return b
	}
	trip := r.trip
	if perVerbTrip > 0 {
		trip = perVerbTrip
	}
	b := New(trip, r.openFor)
	r.breakers[verb] = b
	return b
}

// Snapshots returns all breaker states.
func (r *Registry) Snapshots() map[string]Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Snapshot, len(r.breakers))
	for k, b := range r.breakers {
		out[k] = b.Snapshot()
	}
	return out
}
