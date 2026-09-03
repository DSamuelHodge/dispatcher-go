package queue_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
)

func TestResumeExecutingToPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := queue.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, _ := st.Create(queue.CreateInput{Verb: "a", ArgsJSON: "{}", Argv: []string{"true"}, ArgvRedacted: []string{"true"}, MaxRetries: 5})
	b, _ := st.Create(queue.CreateInput{Verb: "b", ArgsJSON: "{}", Argv: []string{"true"}, ArgvRedacted: []string{"true"}, MaxRetries: 5})
	_ = st.Update(a.ID, func(tk *queue.Task) { tk.State = queue.StateExecuting })
	_ = st.Update(b.ID, func(tk *queue.Task) { tk.State = queue.StatePending })

	stats, err := queue.Resume(st, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ExecutingToPending < 1 {
		t.Fatalf("%+v", stats)
	}
	ga, _ := st.Get(a.ID)
	if ga.State != queue.StatePending {
		t.Fatalf("a state=%s", ga.State)
	}
	// never executed
	if ga.LastAttemptOutcome == "ok" || ga.State == queue.StateExecuted {
		t.Fatal("phantom executed")
	}
	gb, _ := st.Get(b.ID)
	if gb.State != queue.StatePending {
		t.Fatalf("b=%s", gb.State)
	}
}
