// Package worker claims pending tasks, executes them, and schedules retries.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
	"github.com/DSamuelHodge/dispatcher-go/internal/notify"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/retry"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Worker runs the durable execution loop.
type Worker struct {
	Store       queue.Store
	Catalog     *verbs.Catalog
	AuditLog    *audit.Logger
	Notifier    notify.Notifier
	BackoffBase time.Duration
	MaxJitter   time.Duration
	PollEvery   time.Duration
}

// Run polls until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if w.PollEvery <= 0 {
		w.PollEvery = 100 * time.Millisecond
	}
	if w.BackoffBase <= 0 {
		w.BackoffBase = time.Second
	}
	if w.MaxJitter <= 0 {
		w.MaxJitter = 250 * time.Millisecond
	}
	if w.Notifier == nil {
		w.Notifier = notify.Termux{}
	}
	t := time.NewTicker(w.PollEvery)
	defer t.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	_, _ = w.Store.DrainOutbox(w.AuditLog, 50)
	task, err := w.Store.ClaimDue(time.Now().UTC())
	if err != nil || task == nil {
		return
	}
	w.execute(ctx, task)
}

func (w *Worker) execute(ctx context.Context, task *queue.Task) {
	v, ok := w.Catalog.Get(task.Verb)
	if !ok {
		_ = w.Store.Update(task.ID, func(t *queue.Task) {
			t.State = queue.StateFailed
			t.Error = "unknown verb in catalog"
			t.LastAttemptOutcome = "failed"
		})
		return
	}
	timeout := time.Duration(w.Catalog.Daemon.TaskTimeoutS) * time.Second
	if v.TimeoutS > 0 {
		timeout = time.Duration(v.TimeoutS) * time.Second
	}
	n := task.Attempt
	start := time.Now().UTC()
	_ = w.audit(audit.Event{
		TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
		State: queue.StateExecuting, ArgvRedacted: task.ArgvRedacted, Attempt: n,
	})
	res := execx.Run(ctx, task.Argv(), task.StdinBlob, timeout)
	end := time.Now().UTC()
	lat := end.Sub(start).Milliseconds()

	outcome := "ok"
	errMsg := ""
	ec := res.ExitCode
	if res.TimedOut {
		outcome = "timeout"
		errMsg = res.Err.Error()
	} else if res.Err != nil || res.ExitCode != 0 {
		outcome = "failed"
		if res.Err != nil {
			errMsg = res.Err.Error()
		} else {
			errMsg = fmt.Sprintf("exit %d", res.ExitCode)
		}
	}
	_ = w.Store.RecordAttempt(task.ID, n, start, end, &ec, outcome, errMsg)

	maxRetries := task.MaxRetries
	if maxRetries == 0 {
		maxRetries = w.Catalog.Daemon.MaxRetries
	}

	if outcome == "ok" {
		var result any
		if v.Parser == verbs.ParserJSON || v.Parser == "" {
			result, _ = execx.ParseJSON(res.Stdout)
		}
		_ = w.Store.Update(task.ID, func(t *queue.Task) {
			t.State = queue.StateExecuted
			t.LastAttemptOutcome = "ok"
			t.ExitCode = &ec
			t.Stdout = res.Stdout
			t.Stderr = res.Stderr
			t.Result = result
			if result != nil {
				b, _ := json.Marshal(result)
				t.ResultJSON = string(b)
			}
			t.Error = ""
			t.NextRunAt = nil
		})
		_ = w.audit(audit.Event{
			TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
			State: queue.StateExecuted, ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: n,
		})
		return
	}

	// failed / timeout
	if retry.ShouldExhaust(n, maxRetries) {
		_ = w.Store.Update(task.ID, func(t *queue.Task) {
			t.State = queue.StateExhausted
			t.LastAttemptOutcome = outcome
			t.ExitCode = &ec
			t.Stdout = res.Stdout
			t.Stderr = res.Stderr
			t.Error = errMsg
			t.NextRunAt = nil
		})
		_ = w.audit(audit.Event{
			TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
			State: queue.StateExhausted, ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: n, Error: errMsg,
		})
		_ = w.Notifier.Exhausted(ctx, task.Verb, task.ID, n+1)
		return
	}

	delay := retry.DelayAfterFailure(w.BackoffBase, n, w.MaxJitter)
	next := time.Now().UTC().Add(delay)
	_ = w.Store.Update(task.ID, func(t *queue.Task) {
		t.State = queue.StateRetryScheduled
		t.LastAttemptOutcome = outcome
		t.ExitCode = &ec
		t.Stdout = res.Stdout
		t.Stderr = res.Stderr
		t.Error = errMsg
		t.Attempt = n + 1
		t.NextRunAt = &next
	})
	_ = w.audit(audit.Event{
		TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
		State: "will-retry", ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
		LatencyMS: lat, Attempt: n, Error: errMsg,
	})
}

func (w *Worker) audit(ev audit.Event) error {
	if err := w.Store.AppendAudit(ev); err != nil {
		return err
	}
	// opportunistic drain
	_, _ = w.Store.DrainOutbox(w.AuditLog, 20)
	return nil
}

// RunOnce claims and executes at most one due task (tests).
func (w *Worker) RunOnce(ctx context.Context) bool {
	if w.BackoffBase <= 0 {
		w.BackoffBase = time.Second
	}
	if w.MaxJitter <= 0 {
		w.MaxJitter = 250 * time.Millisecond
	}
	if w.Notifier == nil {
		w.Notifier = notify.Termux{}
	}
	_, _ = w.Store.DrainOutbox(w.AuditLog, 50)
	task, err := w.Store.ClaimDue(time.Now().UTC())
	if err != nil || task == nil {
		return false
	}
	w.execute(ctx, task)
	return true
}
