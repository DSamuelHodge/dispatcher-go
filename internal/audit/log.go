// Package audit writes NDJSON lifecycle events (M3 file logger; M4 adds outbox).
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event is one lifecycle transition line.
type Event struct {
	TS           string   `json:"ts"`
	TaskID       string   `json:"task_id"`
	Verb         string   `json:"verb"`
	Tier         string   `json:"tier,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	Approval     string   `json:"approval,omitempty"`
	ApprovedBy   string   `json:"approved_by,omitempty"`
	State        string   `json:"state"`
	ArgvRedacted []string `json:"argv_redacted,omitempty"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	LatencyMS    int64    `json:"latency_ms,omitempty"`
	Attempt      int      `json:"attempt,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Logger appends NDJSON lines.
type Logger struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	Mem     []Event
	MemOnly bool
}

// Open creates/appends to path. Empty path ⇒ memory-only.
func Open(path string) (*Logger, error) {
	l := &Logger{path: path}
	if path == "" {
		l.MemOnly = true
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

// Close flushes the file handle.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// Log writes one event (fsync per line when file-backed).
func (l *Logger) Log(ev Event) error {
	if l == nil {
		return nil
	}
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Mem = append(l.Mem, ev)
	if l.MemOnly || l.f == nil {
		return nil
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		return err
	}
	return l.f.Sync()
}

// Contains reports whether any in-memory event JSON contains substr.
func (l *Logger) Contains(substr string) bool {
	if l == nil || substr == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range l.Mem {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), substr) {
			return true
		}
	}
	return false
}
