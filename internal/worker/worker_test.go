package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/notify"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
	"github.com/DSamuelHodge/dispatcher-go/internal/worker"
)

func TestWorkerExhaustion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "termux-fail")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cat, err := verbs.Parse([]byte(`
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 2
  max_retries: 2
  backoff_base_s: 0.01
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 100
  stream_buffer_default: 128
verbs:
  - name: always.fail
    tier: B
    argv: ["termux-toast", "x"]
    parser: exit
    watch: false
`))
	if err != nil {
		t.Fatal(err)
	}
	// replace toast with failing shim named termux-toast
	_ = os.WriteFile(filepath.Join(dir, "termux-toast"), []byte("#!/bin/sh\nexit 1\n"), 0o755)

	db := filepath.Join(dir, "t.db")
	st, err := queue.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, err := st.Create(queue.CreateInput{
		Verb: "always.fail", ArgsJSON: "{}", Argv: []string{"termux-toast", "x"},
		ArgvRedacted: []string{"termux-toast", "x"}, MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Update(task.ID, func(tk *queue.Task) { tk.State = queue.StatePending })

	log, _ := audit.Open("")
	nlog := &notify.LogNotifier{}
	w := &worker.Worker{
		Store: st, Catalog: cat, AuditLog: log, Notifier: nlog,
		BackoffBase: time.Millisecond, MaxJitter: 0, PollEvery: time.Millisecond,
	}
	ctx := context.Background()
	// attempts 0,1,2 → exhaust on n=2
	for i := 0; i < 10; i++ {
		_ = w.RunOnce(ctx)
		got, _ := st.Get(task.ID)
		if got.State == queue.StateExhausted {
			break
		}
		// advance due time for retry_scheduled
		if got.State == queue.StateRetryScheduled {
			past := time.Now().UTC().Add(-time.Second)
			_ = st.Update(task.ID, func(tk *queue.Task) { tk.NextRunAt = &past })
		}
	}
	got, _ := st.Get(task.ID)
	if got.State != queue.StateExhausted {
		t.Fatalf("state=%s attempt=%d", got.State, got.Attempt)
	}
	if len(nlog.Calls) != 1 {
		t.Fatalf("notify calls=%v", nlog.Calls)
	}
	// outbox drained into log
	if !log.Contains("exhausted") {
		t.Fatal("audit missing exhausted")
	}
}

func TestWorkerSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "termux-battery-status"), []byte("#!/bin/sh\necho '{\"ok\":true}'\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cat, err := verbs.Parse([]byte(`
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 5
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 100
  stream_buffer_default: 128
verbs:
  - name: battery.status
    tier: A
    argv: ["termux-battery-status"]
    parser: json
    watch: false
`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := queue.OpenSQLite(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, _ := st.Create(queue.CreateInput{
		Verb: "battery.status", ArgsJSON: "{}", Argv: []string{"termux-battery-status"},
		ArgvRedacted: []string{"termux-battery-status"}, MaxRetries: 5,
	})
	_ = st.Update(task.ID, func(tk *queue.Task) { tk.State = queue.StatePending })
	w := &worker.Worker{Store: st, Catalog: cat, AuditLog: nil, Notifier: &notify.LogNotifier{}, BackoffBase: time.Millisecond}
	if !w.RunOnce(context.Background()) {
		t.Fatal("expected claim")
	}
	got, _ := st.Get(task.ID)
	if got.State != queue.StateExecuted {
		t.Fatalf("%+v", got)
	}
}
