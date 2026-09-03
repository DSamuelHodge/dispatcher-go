package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	if err := writeTaskTx(tx, t); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Depth() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE state IN (?,?,?,?,?)`,
		StatePending, StateRetryScheduled, StatePendingApproval, StateExecuting, StateAccepted).Scan(&n)
	return n
}

func (s *SQLite) ClaimDue(now time.Time) (*Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRow(`
SELECT `+taskCols+` FROM tasks
WHERE state=? OR (state=? AND (next_run_at IS NULL OR next_run_at<=?))
ORDER BY created_at LIMIT 1`, StatePending, StateRetryScheduled, fmtTime(now))
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.State = StateExecuting
	t.UpdatedAt = nowUTC()
	if err := writeTaskTx(tx, t); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return cloneTask(t), nil
}

func (s *SQLite) RecordAttempt(taskID string, n int, started, ended time.Time, exitCode *int, outcome, errMsg string) error {
	var ec any
	if exitCode != nil {
		ec = *exitCode
	}
	_, err := s.db.Exec(`
INSERT OR REPLACE INTO attempts(task_id, n, started_at, ended_at, exit_code, outcome, error)
VALUES(?,?,?,?,?,?,?)`, taskID, n, fmtTime(started), fmtTime(ended), ec, outcome, errMsg)
	return err
}

func (s *SQLite) AppendAudit(ev audit.Event) error {
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	id := newID()
	_, err = s.db.Exec(`INSERT INTO audit_outbox(id, ts, payload_json, written_at) VALUES(?,?,?,NULL)`,
		id, ev.TS, string(payload))
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
	if apprMode.Valid {
		t.ApprovalMode = apprMode.String
	}
	if apprBy.Valid {
		t.ApprovedBy = apprBy.String
	}
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

func writeTaskTx(tx *sql.Tx, t *Task) error {
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
	_, err := tx.Exec(`
UPDATE tasks SET verb=?, args_json=?, argv_json=?, argv_redacted=?, stdin_blob=?, stdin_present=?,
 state=?, attempt=?, max_retries=?, next_run_at=?, last_attempt_outcome=?, approval_mode=?, approved_by=?,
 exit_code=?, stdout=?, stderr=?, result_json=?, error=?, updated_at=?
WHERE id=?`,
		t.Verb, t.ArgsJSON, t.ArgvJSON, string(argvR), t.StdinBlob, boolInt(t.StdinPresent),
		t.State, t.Attempt, t.MaxRetries, next, nullStr(t.LastAttemptOutcome), nullStr(t.ApprovalMode), nullStr(t.ApprovedBy),
		ec, t.Stdout, t.Stderr, nullStr(resJSON), nullStr(t.Error), fmtTime(t.UpdatedAt), t.ID,
	)
	return err
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
