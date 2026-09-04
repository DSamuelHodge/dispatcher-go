// Package queue provides durable and in-memory task storage.
package queue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
)

// Task states (spec §9.1).
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
	StateRetryScheduled  = "retry_scheduled"
	StateExhausted       = "exhausted"
)

// Task is a durable task record.
type Task struct {
	ID                 string     `json:"id"`
	Verb               string     `json:"verb"`
	ArgsJSON           string     `json:"args_json,omitempty"`
	ArgvJSON           string     `json:"-"` // full argv for re-exec (may omit secrets; secrets in StdinBlob)
	ArgvRedacted       []string   `json:"argv_redacted"`
	StdinPresent       bool       `json:"stdin_present,omitempty"`
	StdinBlob          string     `json:"-"` // never serialized to clients
	State              string     `json:"state"`
	Attempt            int        `json:"attempt"`
	MaxRetries         int        `json:"max_retries,omitempty"`
	NextRunAt          *time.Time `json:"next_run_at,omitempty"`
	LastAttemptOutcome string     `json:"last_attempt_outcome,omitempty"`
	ApprovalMode       string     `json:"approval_mode,omitempty"`
	ApprovedBy         string     `json:"approved_by,omitempty"`
	ExitCode           *int       `json:"exit_code,omitempty"`
	Stdout             string     `json:"stdout,omitempty"`
	Stderr             string     `json:"stderr,omitempty"`
	ResultJSON         string     `json:"-"`
	Result             any        `json:"result,omitempty"`
	Error              string     `json:"error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// IdempotencyRecord binds a client key to the task created for it (ADR-0003).
// RequestHash covers verb + canonical args + stdin so different payloads
// under the same key are conflicts, not replays.
type IdempotencyRecord struct {
	Key         string
	Verb        string
	RequestHash string
	TaskID      string
	CreatedAt   time.Time
}

// CreateInput is the payload for inserting a new task.
type CreateInput struct {
	Verb         string
	ArgsJSON     string
	Argv         []string // full argv for execution
	ArgvRedacted []string
	Stdin        string
	MaxRetries   int
}

// Store is the task persistence surface used by API and worker.
type Store interface {
	Create(in CreateInput) (*Task, error)
	Get(id string) (*Task, bool)
	List(state string) []*Task
	Update(id string, fn func(*Task)) error
	Depth() int
	// ClaimDue marks one due pending/retry_scheduled task as executing and returns it.
	ClaimDue(now time.Time) (*Task, error)
	// RecordAttempt inserts an attempts row (best-effort on Memory).
	RecordAttempt(taskID string, n int, started, ended time.Time, exitCode *int, outcome, errMsg string) error
	// AppendAudit enqueues an audit event (outbox on SQLite; immediate on Memory+Logger).
	AppendAudit(ev audit.Event) error
	// CreateAndAudit inserts a task and its audit event atomically (ADR-0002).
	CreateAndAudit(in CreateInput, ev audit.Event) (*Task, error)
	// UpdateAndAudit applies fn and appends the audit event atomically (ADR-0002).
	UpdateAndAudit(id string, fn func(*Task), ev audit.Event) error
	// FindIdempotency looks up a client idempotency key (ADR-0003).
	FindIdempotency(key string) (IdempotencyRecord, bool, error)
	// SaveIdempotency records a key binding with plain INSERT (duplicate = error).
	SaveIdempotency(rec IdempotencyRecord) error
	// DrainOutbox writes pending outbox rows to the audit file logger.
	DrainOutbox(log *audit.Logger, limit int) (int, error)
	Close() error
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func cloneTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	c := *t
	c.ArgvRedacted = append([]string(nil), t.ArgvRedacted...)
	if t.NextRunAt != nil {
		n := *t.NextRunAt
		c.NextRunAt = &n
	}
	if t.ExitCode != nil {
		e := *t.ExitCode
		c.ExitCode = &e
	}
	if t.ResultJSON != "" && t.Result == nil {
		var v any
		if json.Unmarshal([]byte(t.ResultJSON), &v) == nil {
			c.Result = v
		}
	}
	return &c
}

func encodeArgv(argv []string) string {
	b, _ := json.Marshal(argv)
	return string(b)
}

func decodeArgv(s string) []string {
	if s == "" {
		return nil
	}
	var a []string
	_ = json.Unmarshal([]byte(s), &a)
	return a
}

func nowUTC() time.Time { return time.Now().UTC() }
