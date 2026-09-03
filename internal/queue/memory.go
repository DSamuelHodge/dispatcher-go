package queue

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Task states (spec §9.1 subset used in M2/M3).
const (
	StateAccepted        = "accepted"
	StatePendingApproval = "pending_approval"
	StatePending         = "pending"
	StateExecuting       = "executing"
	StateExecuted        = "executed"
	StateFailed          = "failed"
	StateTimeout         = "timeout"
	StateDenied          = "denied"
	StateCanceled        = "canceled"
)

// Task is an in-memory task record (SQLite lands in M4).
type Task struct {
	ID                 string    `json:"id"`
	Verb               string    `json:"verb"`
	ArgsJSON           string    `json:"args_json,omitempty"` // already redacted when secrets present
	ArgvRedacted       []string  `json:"argv_redacted"`
	State              string    `json:"state"`
	Attempt            int       `json:"attempt"`
	LastAttemptOutcome string    `json:"last_attempt_outcome,omitempty"`
	ApprovalMode       string    `json:"approval_mode,omitempty"`
	ApprovedBy         string    `json:"approved_by,omitempty"`
	ExitCode           *int      `json:"exit_code,omitempty"`
	Stdout             string    `json:"stdout,omitempty"`
	Stderr             string    `json:"stderr,omitempty"`
	Result             any       `json:"result,omitempty"`
	Error              string    `json:"error,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Memory is a process-local task store for M2/M3.
type Memory struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{tasks: make(map[string]*Task)}
}

// Create inserts a new task.
func (m *Memory) Create(verb string, argvRedacted []string, argsJSON string) *Task {
	now := time.Now().UTC()
	t := &Task{
		ID:           newID(),
		Verb:         verb,
		ArgsJSON:     argsJSON,
		ArgvRedacted: append([]string(nil), argvRedacted...),
		State:        StateAccepted,
		Attempt:      0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.mu.Lock()
	m.tasks[t.ID] = t
	m.mu.Unlock()
	return clone(t)
}

// Get returns a task copy or false.
func (m *Memory) Get(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	return clone(t), true
}

// List filters by state (empty = all).
func (m *Memory) List(state string) []*Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if state == "" || t.State == state {
			out = append(out, clone(t))
		}
	}
	return out
}

// Update applies fn under lock.
func (m *Memory) Update(id string, fn func(*Task)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	fn(t)
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Depth returns number of tasks.
func (m *Memory) Depth() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func clone(t *Task) *Task {
	c := *t
	c.ArgvRedacted = append([]string(nil), t.ArgvRedacted...)
	return &c
}
