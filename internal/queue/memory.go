package queue

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
)

// Memory is a process-local Store (tests / fallback).
type Memory struct {
	mu     sync.Mutex
	tasks  map[string]*Task
	audit  []audit.Event
	idem   map[string]IdempotencyRecord
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
	if m.tasks == nil {
		m.tasks = make(map[string]*Task)
	}
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
	// Deterministic created_at order, matching SQLite ClaimDue/List.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
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
		due := t.State == StatePending ||
			(t.State == StateRetryScheduled && (t.NextRunAt == nil || !t.NextRunAt.After(now)))
		if !due {
			continue
		}
		if pick == nil || t.CreatedAt.Before(pick.CreatedAt) ||
			(t.CreatedAt.Equal(pick.CreatedAt) && t.ID < pick.ID) {
			pick = t
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

// CreateAndAudit inserts a task and records its audit event under one lock.
func (m *Memory) CreateAndAudit(in CreateInput, ev audit.Event) (*Task, error) {
	t, err := m.Create(in)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if ev.TaskID == "" {
		ev.TaskID = t.ID
	}
	m.audit = append(m.audit, ev)
	m.mu.Unlock()
	if m.logger != nil {
		if err := m.logger.Log(ev); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// UpdateAndAudit applies fn and records the audit event under one lock.
func (m *Memory) UpdateAndAudit(id string, fn func(*Task), ev audit.Event) error {
	m.mu.Lock()
	if m.tasks == nil {
		m.mu.Unlock()
		return fmt.Errorf("task %q not found", id)
	}
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("task %q not found", id)
	}
	fn(t)
	t.UpdatedAt = nowUTC()
	if ev.TaskID == "" {
		ev.TaskID = id
	}
	m.audit = append(m.audit, ev)
	m.mu.Unlock()
	if m.logger != nil {
		return m.logger.Log(ev)
	}
	return nil
}

// FindIdempotency looks up a client idempotency key.
func (m *Memory) FindIdempotency(key string) (IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.idem[key]
	return rec, ok, nil
}

// SaveIdempotency records a key binding; duplicate keys are an error.
func (m *Memory) SaveIdempotency(rec IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idem == nil {
		m.idem = make(map[string]IdempotencyRecord)
	}
	if _, dup := m.idem[rec.Key]; dup {
		return fmt.Errorf("idempotency key %q already claimed", rec.Key)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	m.idem[rec.Key] = rec
	return nil
}

func (m *Memory) Close() error { return nil }
