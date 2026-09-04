package audit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
)

func TestLogMemOnlyAndContains(t *testing.T) {
	l, err := audit.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Log(audit.Event{TaskID: "task-123", State: "done"}); err != nil {
		t.Fatal(err)
	}
	if !l.Contains("task-123") {
		t.Fatal("expected Contains to find logged task id")
	}
	if l.Contains("no-such-task") {
		t.Fatal("unexpected Contains hit")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMemBounded(t *testing.T) {
	l, err := audit.Open("")
	if err != nil {
		t.Fatal(err)
	}
	const total = 2500
	for i := 0; i < total; i++ {
		if err := l.Log(audit.Event{TaskID: fmt.Sprintf("task-%04d", i), State: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(l.Mem); n > 1000 {
		t.Fatalf("Mem unbounded: len=%d, want <=1000", n)
	}
	if l.Contains("task-0000") {
		t.Fatal("oldest event should have been evicted")
	}
	if !l.Contains(fmt.Sprintf("task-%04d", total-1)) {
		t.Fatal("newest event must survive bounding")
	}
}

func TestLogFileBackedFsyncPerLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Log(audit.Event{TaskID: "file-task-1", State: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(audit.Event{TaskID: "file-task-2", State: "error"}); err != nil {
		t.Fatal(err)
	}
	// Sync-per-line: lines must be durable before Close.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines != 2 {
		t.Fatalf("want 2 ndjson lines, got %d: %q", lines, data)
	}
	if !l.Contains("file-task-2") {
		t.Fatal("file-backed logger must also record Mem")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
