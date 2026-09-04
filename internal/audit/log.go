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
	// Unattended marks approvals granted by the -unattended force_ask
	// override (remote-agent full autonomy). Only set on approved events.
	Unattended bool `json:"unattended,omitempty"`
}

// maxMemEvents bounds the in-memory ring used by Mem/Contains (test and
// debug aid — the file is the durable record). Only the newest entries are
// kept so long-running daemons cannot grow memory without bound.
const maxMemEvents = 1000

// Logger appends NDJSON lines.
type Logger struct {
	// mu serializes file writes + fsync so lines never interleave.
	mu sync.Mutex
	// memMu guards Mem only, so Contains readers never block on fsync.
	memMu   sync.RWMutex
	path    string
	f       *os.File // guarded by mu
	Mem     []Event
	MemOnly bool // immutable after Open; safe to read lock-free
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
	// In-memory record first (matches prior order: Mem holds the event even
	// if marshaling or the file write below fails).
	l.appendMem(ev)
	if l.MemOnly {
		return nil
	}
	// Marshal outside the file lock: ev is a local copy, so this is safe and
	// keeps slow fsync lines from holding the lock during encoding.
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if _, err := l.f.Write(b); err != nil {
		return err
	}
	return l.f.Sync()
}

// appendMem records ev, keeping only the newest maxMemEvents entries.
func (l *Logger) appendMem(ev Event) {
	l.memMu.Lock()
	defer l.memMu.Unlock()
	l.Mem = append(l.Mem, ev)
	if len(l.Mem) > maxMemEvents {
		// Drop oldest; copy down so the backing array stays bounded instead
		// of growing with every subsequent append.
		copy(l.Mem, l.Mem[len(l.Mem)-maxMemEvents:])
		l.Mem = l.Mem[:maxMemEvents]
	}
}

// Contains reports whether any in-memory event JSON contains substr.
func (l *Logger) Contains(substr string) bool {
	if l == nil || substr == "" {
		return false
	}
	// Copy under a read lock, then marshal/search without holding it, so
	// readers never block writers (or fsync) longer than a slice copy.
	l.memMu.RLock()
	cp := make([]Event, len(l.Mem))
	copy(cp, l.Mem)
	l.memMu.RUnlock()
	for _, ev := range cp {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), substr) {
			return true
		}
	}
	return false
}
