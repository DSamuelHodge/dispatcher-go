package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
	"github.com/DSamuelHodge/dispatcher-go/internal/notify"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
	"github.com/DSamuelHodge/dispatcher-go/internal/worker"
)

func TestWorkerCircuitOpens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "termux-toast"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cat, err := verbs.Parse([]byte(`
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 2
  max_retries: 20
  backoff_base_s: 0.001
  cb_trip_threshold: 3
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
	st, err := queue.OpenSQLite(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := circuit.NewRegistry(3, time.Minute)
	w := &worker.Worker{
		Store: st, Catalog: cat, Notifier: &notify.LogNotifier{}, Circuits: reg,
		BackoffBase: time.Millisecond, MaxJitter: 0,
	}
	// feed 3 failing tasks
	for i := 0; i < 3; i++ {
		task, _ := st.Create(queue.CreateInput{
			Verb: "always.fail", ArgsJSON: "{}", Argv: []string{"termux-toast", "x"},
			ArgvRedacted: []string{"termux-toast", "x"}, MaxRetries: 20,
		})
		_ = st.Update(task.ID, func(tk *queue.Task) { tk.State = queue.StatePending })
		if !w.RunOnce(context.Background()) {
			// may be retry scheduled from previous — force due
			past := time.Now().UTC().Add(-time.Second)
			for _, tk := range st.List(queue.StateRetryScheduled) {
				_ = st.Update(tk.ID, func(x *queue.Task) { x.NextRunAt = &past; x.State = queue.StatePending })
			}
			_ = w.RunOnce(context.Background())
		}
	}
	sn := reg.For("always.fail", 0).Snapshot()
	if sn.State != circuit.Open {
		// run more until open
		for i := 0; i < 5 && sn.State != circuit.Open; i++ {
			past := time.Now().UTC().Add(-time.Second)
			for _, tk := range st.List("") {
				if tk.State == queue.StateRetryScheduled || tk.State == queue.StatePending {
					_ = st.Update(tk.ID, func(x *queue.Task) {
						x.State = queue.StatePending
						x.NextRunAt = &past
					})
				}
			}
			w.RunOnce(context.Background())
			sn = reg.For("always.fail", 0).Snapshot()
		}
	}
	if sn.State != circuit.Open {
		t.Fatalf("want open got %+v", sn)
	}
}
