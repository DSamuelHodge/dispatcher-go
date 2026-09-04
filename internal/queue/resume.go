package queue

import (
	"fmt"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
)

// ResumeStats summarizes boot crash-resume work.
type ResumeStats struct {
	ExecutingToPending int `json:"executing_to_pending"`
	PendingKept        int `json:"pending_kept"`
	RetryDue           int `json:"retry_due"`
	RetryFuture        int `json:"retry_future"`
}

// Resume requeues interrupted work after process start (FR-6.5).
// - executing → pending (never phantom-executed)
// - pending left as pending
// - retry_scheduled due (next_run_at <= now) left claimable; future kept
// Ordering is by created_at (claim already orders).
func Resume(store Store, now time.Time) (ResumeStats, error) {
	var st ResumeStats
	if store == nil {
		return st, fmt.Errorf("nil store")
	}
	// Memory + SQLite both support List
	for _, t := range store.List(StateExecuting) {
		err := store.Update(t.ID, func(tk *Task) {
			tk.State = StatePending
			// clear any partial outcome; do not mark executed
			tk.Error = "resumed after crash (was executing)"
		})
		if err != nil {
			return st, err
		}
		st.ExecutingToPending++
	}
	st.PendingKept = len(store.List(StatePending))
	for _, t := range store.List(StateRetryScheduled) {
		if t.NextRunAt == nil || !t.NextRunAt.After(now) {
			st.RetryDue++
		} else {
			st.RetryFuture++
		}
	}
	// accepted means created but never through the approval gate (the gate
	// runs inline right after Create). Fail closed: cancel rather than
	// making a never-approved task runnable. Audited like every terminal
	// transition (ADR-0002).
	for _, t := range store.List(StateAccepted) {
		err := store.UpdateAndAudit(t.ID, func(tk *Task) {
			tk.State = StateCanceled
			tk.Error = "canceled on restart: never passed approval gate"
		}, audit.Event{
			TaskID: t.ID, Verb: t.Verb, State: StateCanceled,
			ArgvRedacted: t.ArgvRedacted, Error: "canceled on restart: never passed approval gate",
		})
		if err != nil {
			return st, err
		}
	}
	// pending_approval at crash → denied (cannot complete dialog safely) — safer than hanging
	for _, t := range store.List(StatePendingApproval) {
		err := store.UpdateAndAudit(t.ID, func(tk *Task) {
			tk.State = StateDenied
			tk.Error = "approval interrupted by crash"
		}, audit.Event{
			TaskID: t.ID, Verb: t.Verb, State: StateDenied,
			ArgvRedacted: t.ArgvRedacted, Error: "approval interrupted by crash",
		})
		if err != nil {
			return st, err
		}
	}
	return st, nil
}
