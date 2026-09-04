package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	_ "modernc.org/sqlite"
)

// SQLite is the durable Store (ADR-0001/0002).
type SQLite struct {
	db *sql.DB
}

// OpenSQLite opens/creates path with WAL + FULL synchronous.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialize writers for simplicity
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate() error {
	pragmas := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA synchronous=FULL;`,
		`PRAGMA foreign_keys=ON;`,
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("pragma: %w", err)
		}
	}
	// P0: PRAGMA Exec succeeds even when the mode silently falls back
	// (e.g. :memory:, read-only dir). Fail closed on the effective values.
	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode;`).Scan(&journalMode); err != nil {
		return fmt.Errorf("pragma journal_mode verify: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("pragma journal_mode=%q, need wal", journalMode)
	}
	var synchronous int
	if err := s.db.QueryRow(`PRAGMA synchronous;`).Scan(&synchronous); err != nil {
		return fmt.Errorf("pragma synchronous verify: %w", err)
	}
	if synchronous != 2 { // 2 == FULL
		return fmt.Errorf("pragma synchronous=%d, need FULL(2)", synchronous)
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tasks(
  id TEXT PRIMARY KEY,
  verb TEXT NOT NULL,
  args_json TEXT NOT NULL,
  argv_json TEXT NOT NULL DEFAULT '[]',
  argv_redacted TEXT NOT NULL,
  stdin_blob TEXT NOT NULL DEFAULT '',
  stdin_present INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 0,
  max_retries INTEGER NOT NULL DEFAULT 5,
  next_run_at TEXT,
  last_attempt_outcome TEXT,
  approval_mode TEXT,
  approved_by TEXT,
  exit_code INTEGER,
  stdout TEXT,
  stderr TEXT,
  result_json TEXT,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attempts(
  task_id TEXT NOT NULL,
  n INTEGER NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER,
  outcome TEXT,
  error TEXT,
  PRIMARY KEY(task_id, n)
);
CREATE TABLE IF NOT EXISTS streams(
  id TEXT PRIMARY KEY,
  verb TEXT NOT NULL,
  pid INTEGER,
  buf_path TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_outbox(
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  written_at TEXT
);
CREATE TABLE IF NOT EXISTS idempotency_keys(
  key TEXT PRIMARY KEY,
  verb TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  task_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_state_next ON tasks(state, next_run_at);
CREATE INDEX IF NOT EXISTS idx_outbox_unwritten ON audit_outbox(written_at);
`)
	return err
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Create(in CreateInput) (*Task, error) {
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
	argvR, _ := json.Marshal(t.ArgvRedacted)
	_, err := s.db.Exec(`
INSERT INTO tasks(id, verb, args_json, argv_json, argv_redacted, stdin_blob, stdin_present,
  state, attempt, max_retries, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Verb, t.ArgsJSON, t.ArgvJSON, string(argvR), t.StdinBlob, boolInt(t.StdinPresent),
		t.State, t.Attempt, t.MaxRetries, fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt),
	)
	if err != nil {
		return nil, err
	}
	return cloneTask(t), nil
}

func (s *SQLite) Get(id string) (*Task, bool) {
	t, err := s.scanOne(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
	if err != nil {
		return nil, false
	}
	return t, true
}

func (s *SQLite) List(state string) []*Task {
	var rows *sql.Rows
	var err error
	if state == "" {
		rows, err = s.db.Query(`SELECT ` + taskCols + ` FROM tasks ORDER BY created_at`)
	} else {
		rows, err = s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE state=? ORDER BY created_at`, state)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (s *SQLite) Update(id string, fn func(*Task)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := scanOneTx(tx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("task %q not found", id)
	}
	fn(t)
	t.UpdatedAt = nowUTC()
	n, err := writeTaskTx(tx, t)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task %q not found (raced delete)", id)
	}
	return tx.Commit()
}

// CreateAndAudit inserts a task and its audit event in one transaction
// (ADR-0002: never lose a transition between state write and outbox row).
func (s *SQLite) CreateAndAudit(in CreateInput, ev audit.Event) (*Task, error) {
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
	argvR, _ := json.Marshal(t.ArgvRedacted)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
INSERT INTO tasks(id, verb, args_json, argv_json, argv_redacted, stdin_blob, stdin_present,
  state, attempt, max_retries, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Verb, t.ArgsJSON, t.ArgvJSON, string(argvR), t.StdinBlob, boolInt(t.StdinPresent),
		t.State, t.Attempt, t.MaxRetries, fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt),
	); err != nil {
		return nil, err
	}
	if _, _, _, err := insertAuditTx(tx, withTaskDefaults(ev, t.ID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cloneTask(t), nil
}

// CreateAndAuditLimited is CreateAndAudit with the capacity check inside the
// same transaction: COUNT of active-state rows, abort with ErrQueueFull when
// already at maxDepth, else insert task + outbox row atomically.
func (s *SQLite) CreateAndAuditLimited(in CreateInput, ev audit.Event, maxDepth int) (*Task, error) {
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
	argvR, _ := json.Marshal(t.ArgvRedacted)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Capacity check inside the write transaction. With MaxOpenConns(1) and
	// BEGIN IMMEDIATE semantics from modernc sqlite, concurrent writers
	// serialize here: the count reflects all committed inserts.
	var depth int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE state IN (?,?,?,?,?)`,
		StatePending, StateRetryScheduled, StatePendingApproval, StateExecuting, StateAccepted,
	).Scan(&depth); err != nil {
		return nil, err
	}
	if depth >= maxDepth {
		return nil, ErrQueueFull
	}
	if _, err := tx.Exec(`
INSERT INTO tasks(id, verb, args_json, argv_json, argv_redacted, stdin_blob, stdin_present,
  state, attempt, max_retries, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Verb, t.ArgsJSON, t.ArgvJSON, string(argvR), t.StdinBlob, boolInt(t.StdinPresent),
		t.State, t.Attempt, t.MaxRetries, fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt),
	); err != nil {
		return nil, err
	}
	if _, _, _, err := insertAuditTx(tx, withTaskDefaults(ev, t.ID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cloneTask(t), nil
}

// UpdateAndAudit applies fn and appends the audit event in one transaction.
func (s *SQLite) UpdateAndAudit(id string, fn func(*Task), ev audit.Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := scanOneTx(tx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("task %q not found", id)
	}
	fn(t)
	t.UpdatedAt = nowUTC()
	n, err := writeTaskTx(tx, t)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("task %q not found (raced delete)", id)
	}
	if _, _, _, err := insertAuditTx(tx, withTaskDefaults(ev, id)); err != nil {
		return err
	}
	return tx.Commit()
}

// FindIdempotency looks up a client idempotency key.
func (s *SQLite) FindIdempotency(key string) (IdempotencyRecord, bool, error) {
	var rec IdempotencyRecord
	var created string
	err := s.db.QueryRow(`SELECT key, verb, request_hash, task_id, created_at FROM idempotency_keys WHERE key=?`, key).
		Scan(&rec.Key, &rec.Verb, &rec.RequestHash, &rec.TaskID, &created)
	if err == sql.ErrNoRows {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if tm, e := time.Parse(time.RFC3339Nano, created); e == nil {
		rec.CreatedAt = tm
	}
	return rec, true, nil
}

// SaveIdempotency records a key binding with plain INSERT: a duplicate key
// is an error and the caller must re-Find to replay the winner.
func (s *SQLite) SaveIdempotency(rec IdempotencyRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	_, err := s.db.Exec(`INSERT INTO idempotency_keys(key, verb, request_hash, task_id, created_at) VALUES(?,?,?,?,?)`,
		rec.Key, rec.Verb, rec.RequestHash, rec.TaskID, fmtTime(rec.CreatedAt))
	return err
}

// withTaskDefaults fills TaskID when the caller left it empty.
func withTaskDefaults(ev audit.Event, taskID string) audit.Event {
	if ev.TaskID == "" {
		ev.TaskID = taskID
	}
	return ev
}

// auditRow marshals one outbox row.
func auditRow(ev audit.Event) (id, ts, payload string, err error) {
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", "", "", err
	}
	return newID(), ev.TS, string(b), nil
}

func insertAuditTx(tx *sql.Tx, ev audit.Event) (string, string, string, error) {
	id, ts, payload, err := auditRow(ev)
	if err != nil {
		return "", "", "", err
	}
	_, err = tx.Exec(`INSERT INTO audit_outbox(id, ts, payload_json, written_at) VALUES(?,?,?,NULL)`,
		id, ts, payload)
	return id, ts, payload, err
}

func (s *SQLite) Depth() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE state IN (?,?,?,?,?)`,
		StatePending, StateRetryScheduled, StatePendingApproval, StateExecuting, StateAccepted).Scan(&n)
	return n
}

func (s *SQLite) ClaimDue(now time.Time) (*Task, error) {
	// P0 double-claim fix: single-statement conditional UPDATE is atomic.
	// The loser's UPDATE matches 0 rows, so two writers can never both
	// mark the same task executing. Retry a few times in case our
	// candidate was just taken by someone else.
	for i := 0; i < 5; i++ {
		var id string
		err := s.db.QueryRow(`
SELECT id FROM tasks
WHERE state=? OR (state=? AND (next_run_at IS NULL OR next_run_at<=?))
ORDER BY created_at LIMIT 1`, StatePending, StateRetryScheduled, fmtTime(now)).Scan(&id)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		res, err := s.db.Exec(`
UPDATE tasks SET state=?, updated_at=?
WHERE id=? AND (state=? OR (state=? AND (next_run_at IS NULL OR next_run_at<=?)))`,
			StateExecuting, fmtTime(nowUTC()), id,
			StatePending, StateRetryScheduled, fmtTime(now))
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 1 {
			t, err := s.scanOne(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
			if err != nil {
				return nil, err
			}
			return cloneTask(t), nil
		}
		// Lost the race on this candidate; look for the next one.
	}
	return nil, nil
}

func (s *SQLite) RecordAttempt(taskID string, n int, started, ended time.Time, exitCode *int, outcome, errMsg string) error {
	var ec any
	if exitCode != nil {
		ec = *exitCode
	}
	// Plain INSERT: re-recording an attempt number is a constraint
	// violation, not a silent overwrite (masks double-execution).
	_, err := s.db.Exec(`
INSERT INTO attempts(task_id, n, started_at, ended_at, exit_code, outcome, error)
VALUES(?,?,?,?,?,?,?)`, taskID, n, fmtTime(started), fmtTime(ended), ec, outcome, errMsg)
	return err
}

func (s *SQLite) AppendAudit(ev audit.Event) error {
	id, ts, payload, err := auditRow(ev)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO audit_outbox(id, ts, payload_json, written_at) VALUES(?,?,?,NULL)`,
		id, ts, payload)
	return err
}

func (s *SQLite) DrainOutbox(log *audit.Logger, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, payload_json FROM audit_outbox WHERE written_at IS NULL ORDER BY ts LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id, payload string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			return 0, err
		}
		batch = append(batch, r)
	}
	n := 0
	for _, r := range batch {
		var ev audit.Event
		if err := json.Unmarshal([]byte(r.payload), &ev); err != nil {
			// Poison pill: mark written so one corrupt row can't wedge
			// the drain (or starve rows behind it under a limit) forever.
			_, _ = s.db.Exec(`UPDATE audit_outbox SET written_at=? WHERE id=?`, fmtTime(nowUTC()), r.id)
			continue
		}
		if log != nil {
			if err := log.Log(ev); err != nil {
				return n, err
			}
		}
		_, err := s.db.Exec(`UPDATE audit_outbox SET written_at=? WHERE id=?`, fmtTime(nowUTC()), r.id)
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

const taskCols = `id, verb, args_json, argv_json, argv_redacted, stdin_blob, stdin_present, state, attempt, max_retries, next_run_at, last_attempt_outcome, approval_mode, approved_by, exit_code, stdout, stderr, result_json, error, created_at, updated_at`

type scannable interface {
	Scan(dest ...any) error
}

func (s *SQLite) scanOne(q string, args ...any) (*Task, error) {
	return scanTask(s.db.QueryRow(q, args...))
}

func scanOneTx(tx *sql.Tx, q string, args ...any) (*Task, error) {
	return scanTask(tx.QueryRow(q, args...))
}

func scanTaskRow(rows *sql.Rows) (*Task, error) {
	return scanTask(rows)
}

func scanTask(row scannable) (*Task, error) {
	var t Task
	var argsJSON, argvJSON, argvR, stdinBlob, nextRun, lastOut, apprMode, apprBy sql.NullString
	var stdout, stderr, resJSON, errMsg, created, updated sql.NullString
	var stdinPres int
	var exitCode sql.NullInt64
	err := row.Scan(
		&t.ID, &t.Verb, &argsJSON, &argvJSON, &argvR, &stdinBlob, &stdinPres,
		&t.State, &t.Attempt, &t.MaxRetries, &nextRun, &lastOut, &apprMode, &apprBy,
		&exitCode, &stdout, &stderr, &resJSON, &errMsg, &created, &updated,
	)
	if err != nil {
		return nil, err
	}
	if argsJSON.Valid {
		t.ArgsJSON = argsJSON.String
	}
	if argvJSON.Valid {
		t.ArgvJSON = argvJSON.String
	}
	if argvR.Valid {
		_ = json.Unmarshal([]byte(argvR.String), &t.ArgvRedacted)
	}
	if stdinBlob.Valid {
		t.StdinBlob = stdinBlob.String
	}
	if stdout.Valid {
		t.Stdout = stdout.String
	}
	if stderr.Valid {
		t.Stderr = stderr.String
	}
	t.StdinPresent = stdinPres != 0
	if nextRun.Valid && nextRun.String != "" {
		if tm, e := time.Parse(time.RFC3339Nano, nextRun.String); e == nil {
			t.NextRunAt = &tm
		}
	}
	if lastOut.Valid {
		t.LastAttemptOutcome = lastOut.String
	}
	// approval_mode/approved_by columns retained for schema compat; ignored.
	_ = apprMode
	_ = apprBy
	if exitCode.Valid {
		v := int(exitCode.Int64)
		t.ExitCode = &v
	}
	if resJSON.Valid {
		t.ResultJSON = resJSON.String
		var v any
		if json.Unmarshal([]byte(resJSON.String), &v) == nil {
			t.Result = v
		}
	}
	if errMsg.Valid {
		t.Error = errMsg.String
	}
	if created.Valid {
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	}
	if updated.Valid {
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	}
	return &t, nil
}

func writeTaskTx(tx *sql.Tx, t *Task) (int64, error) {
	argvR, _ := json.Marshal(t.ArgvRedacted)
	var next any
	if t.NextRunAt != nil {
		next = fmtTime(*t.NextRunAt)
	}
	var ec any
	if t.ExitCode != nil {
		ec = *t.ExitCode
	}
	resJSON := t.ResultJSON
	if resJSON == "" && t.Result != nil {
		b, _ := json.Marshal(t.Result)
		resJSON = string(b)
		t.ResultJSON = resJSON
	}
	res, err := tx.Exec(`
UPDATE tasks SET verb=?, args_json=?, argv_json=?, argv_redacted=?, stdin_blob=?, stdin_present=?,
 state=?, attempt=?, max_retries=?, next_run_at=?, last_attempt_outcome=?, approval_mode=?, approved_by=?,
 exit_code=?, stdout=?, stderr=?, result_json=?, error=?, updated_at=?
WHERE id=?`,
		t.Verb, t.ArgsJSON, t.ArgvJSON, string(argvR), t.StdinBlob, boolInt(t.StdinPresent),
		t.State, t.Attempt, t.MaxRetries, next, nullStr(t.LastAttemptOutcome), nil, nil,
		ec, t.Stdout, t.Stderr, nullStr(resJSON), nullStr(t.Error), fmtTime(t.UpdatedAt), t.ID,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Argv returns decoded execution argv.
func (t *Task) Argv() []string { return decodeArgv(t.ArgvJSON) }
