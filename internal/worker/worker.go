// Package worker claims pending tasks, executes them, and schedules retries.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
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
	Circuits    *circuit.Registry
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
	defer w.drainAudit()
	v, ok := w.Catalog.Get(task.Verb)
	if !ok {
		// Unknown verb is a catalog skew, not an attempt outcome: land in
		// the spec §9.1 terminal state (never phantom failed/timeout rows).
		_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
			t.State = queue.StateExhausted
			t.Error = "unknown verb in catalog"
			t.LastAttemptOutcome = "failed"
		}, audit.Event{
			TaskID: task.ID, Verb: task.Verb, State: queue.StateExhausted,
			ArgvRedacted: task.ArgvRedacted, Error: "unknown verb in catalog",
		})
		return
	}
	trip := 0
	if v.CircuitBreakerThreshold != nil {
		trip = *v.CircuitBreakerThreshold
	}
	var br *circuit.Breaker
	if w.Circuits != nil {
		br = w.Circuits.For(task.Verb, trip)
		if !br.Allow() {
			// requeue shortly — circuit open (exhaustion notify still bypasses via direct path)
			next := time.Now().UTC().Add(time.Second)
			_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
				t.State = queue.StateRetryScheduled
				t.NextRunAt = &next
				t.Error = "circuit_open"
			}, audit.Event{
				TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
				State: "circuit_open", ArgvRedacted: task.ArgvRedacted, Error: "circuit_open",
			})
			return
		}
	}
	timeout := time.Duration(w.Catalog.Daemon.TaskTimeoutS) * time.Second
	if v.TimeoutS > 0 {
		timeout = time.Duration(v.TimeoutS) * time.Second
	}
	n := task.Attempt
	start := time.Now().UTC()
	_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
		t.State = queue.StateExecuting
	}, audit.Event{
		TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
		State: queue.StateExecuting, ArgvRedacted: task.ArgvRedacted, Attempt: n,
	})
	res := execx.Run(ctx, task.Argv(), task.StdinBlob, timeout)
	end := time.Now().UTC()
	lat := end.Sub(start).Milliseconds()
	// Stdin bodies are never persisted second-hand: a child that echoes
	// its input has its output withheld.
	stdout := res.Stdout
	if task.StdinBlob != "" && approve.ContainsSecret(stdout, task.StdinBlob) {
		stdout = approve.RedactedMarker
	}
	stderr := res.Stderr
	if task.StdinBlob != "" && approve.ContainsSecret(stderr, task.StdinBlob) {
		stderr = approve.RedactedMarker
	}

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
		_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
			t.State = queue.StateExecuted
			t.LastAttemptOutcome = "ok"
			t.ExitCode = &ec
			t.Stdout = stdout
			t.Stderr = stderr
			t.Result = result
			if result != nil {
				b, _ := json.Marshal(result)
				t.ResultJSON = string(b)
			}
			t.Error = ""
			t.NextRunAt = nil
		}, audit.Event{
			TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
			State: queue.StateExecuted, ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: n,
		})
		if br != nil {
			br.Success()
		}
		return
	}

	// failed / timeout
	if br != nil {
		br.Failure()
	}
	if retry.ShouldExhaust(n, maxRetries) {
		_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
			t.State = queue.StateExhausted
			t.LastAttemptOutcome = outcome
			t.ExitCode = &ec
			t.Stdout = stdout
			t.Stderr = stderr
			t.Error = errMsg
			t.NextRunAt = nil
		}, audit.Event{
			TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
			State: queue.StateExhausted, ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: n, Error: errMsg,
		})
		_ = w.Notifier.Exhausted(ctx, task.Verb, task.ID, n+1)
		return
	}

	delay := retry.DelayAfterFailure(w.BackoffBase, n, w.MaxJitter)
	next := time.Now().UTC().Add(delay)
	_ = w.Store.UpdateAndAudit(task.ID, func(t *queue.Task) {
		t.State = queue.StateRetryScheduled
		t.LastAttemptOutcome = outcome
		t.ExitCode = &ec
		t.Stdout = stdout
		t.Stderr = stderr
		t.Error = errMsg
		t.Attempt = n + 1
		t.NextRunAt = &next
	}, audit.Event{
		TaskID: task.ID, Verb: task.Verb, Tier: string(v.Tier), Risk: string(v.Risk),
		State: "will-retry", ArgvRedacted: task.ArgvRedacted, ExitCode: &ec,
		LatencyMS: lat, Attempt: n, Error: errMsg,
	})
}

func (w *Worker) drainAudit() {
	// Flush outbox rows appended by UpdateAndAudit: callers (and tests)
	// may read the audit log synchronously after execute returns, and
	// the next tick's drain may be a poll-interval away.
	_, _ = w.Store.DrainOutbox(w.AuditLog, 50)
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
