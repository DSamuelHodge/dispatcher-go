package queue

import (
	"fmt"
	"sync"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
)

// Memory is a process-local Store (tests / fallback).
type Memory struct {
	mu     sync.Mutex
	tasks  map[string]*Task
	audit  []audit.Event
	logger *audit.Logger // optional mirror
}

// NewMemory returns an empty memory store.
func NewMemory() *Memory {
	return &Memory{tasks: make(map[string]*Task)}
}

// SetLogger mirrors AppendAudit to an in-process logger (tests).
func (m *Memory) SetLogger(l *audit.Logger) { m.logger = l }

func (m *Memory) Create(in CreateInput) (*Task, error) {
	now := nowUTC()
	t := &Task{
		ID:           newID(),
		Verb:         in.Verb,
		ArgsJSON:     in.ArgsJSON,
		ArgvJSON:     encodeArgv(in.Argv),
		ArgvRedacted: append([]string(nil), in.ArgvRedacted...),
		StdinPresent: in.Stdin != "",
		StdinBlob:    in.Stdin,
		State:        StateAccepted,
		Attempt:      0,
		MaxRetries:   in.MaxRetries,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.mu.Lock()
	m.tasks[t.ID] = t
	m.mu.Unlock()
	return cloneTask(t), nil
}

func (m *Memory) Get(id string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneTask(t), true
}

func (m *Memory) List(state string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if state == "" || t.State == state {
			out = append(out, cloneTask(t))
		}
	}
	return out
}

func (m *Memory) Update(id string, fn func(*Task)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	fn(t)
	t.UpdatedAt = nowUTC()
	return nil
}

func (m *Memory) Depth() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tasks {
		switch t.State {
		case StatePending, StateRetryScheduled, StatePendingApproval, StateExecuting, StateAccepted:
			n++
		}
	}
	return n
}

func (m *Memory) ClaimDue(now time.Time) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pick *Task
	for _, t := range m.tasks {
		if t.State == StatePending {
			pick = t
			break
		}
		if t.State == StateRetryScheduled && t.NextRunAt != nil && !t.NextRunAt.After(now) {
			pick = t
			break
		}
	}
	if pick == nil {
		return nil, nil
	}
	pick.State = StateExecuting
	pick.UpdatedAt = nowUTC()
	return cloneTask(pick), nil
}

func (m *Memory) RecordAttempt(taskID string, n int, started, ended time.Time, exitCode *int, outcome, errMsg string) error {
	return nil
}

func (m *Memory) AppendAudit(ev audit.Event) error {
	m.mu.Lock()
	m.audit = append(m.audit, ev)
	m.mu.Unlock()
	if m.logger != nil {
		return m.logger.Log(ev)
	}
	return nil
}

func (m *Memory) DrainOutbox(log *audit.Logger, limit int) (int, error) {
	return 0, nil
}

func (m *Memory) Close() error { return nil }
