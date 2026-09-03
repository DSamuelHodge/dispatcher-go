package queue_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
)

func TestSQLiteCreateGetUpdateOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	st, err := queue.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	task, err := st.Create(queue.CreateInput{
		Verb:         "battery.status",
		ArgsJSON:     "{}",
		Argv:         []string{"termux-battery-status"},
		ArgvRedacted: []string{"termux-battery-status"},
		MaxRetries:   5,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st.Get(task.ID)
	if !ok || got.Verb != "battery.status" {
		t.Fatalf("%v %v", ok, got)
	}
	if err := st.Update(task.ID, func(tk *queue.Task) {
		tk.State = queue.StatePending
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(audit.Event{TaskID: task.ID, Verb: "battery.status", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	log, _ := audit.Open("")
	n, err := st.DrainOutbox(log, 10)
	if err != nil || n != 1 {
		t.Fatalf("drain n=%d err=%v", n, err)
	}
	if !log.Contains(task.ID) {
		t.Fatal("audit missing task id")
	}

	claimed, err := st.ClaimDue(time.Now().UTC())
	if err != nil || claimed == nil || claimed.State != queue.StateExecuting {
		t.Fatalf("claim %+v err=%v", claimed, err)
	}
}

func TestSQLiteRetryClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	st, err := queue.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	task, _ := st.Create(queue.CreateInput{Verb: "x", ArgsJSON: "{}", Argv: []string{"true"}, ArgvRedacted: []string{"true"}, MaxRetries: 2})
	past := time.Now().UTC().Add(-time.Second)
	_ = st.Update(task.ID, func(tk *queue.Task) {
		tk.State = queue.StateRetryScheduled
		tk.NextRunAt = &past
		tk.Attempt = 1
	})
	c, err := st.ClaimDue(time.Now().UTC())
	if err != nil || c == nil || c.Attempt != 1 {
		t.Fatalf("%+v %v", c, err)
	}
}
