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
	for _, t := range store.List(StateExecuting) {
		err := store.Update(t.ID, func(tk *Task) {
			tk.State = StatePending
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
	// accepted means created but pipeline never finished (crash mid-request).
	// Fail closed: cancel rather than making a half-created task runnable.
	for _, t := range store.List(StateAccepted) {
		err := store.UpdateAndAudit(t.ID, func(tk *Task) {
			tk.State = StateCanceled
			tk.Error = "canceled on restart: accept incomplete"
		}, audit.Event{
			TaskID: t.ID, Verb: t.Verb, State: StateCanceled,
			ArgvRedacted: t.ArgvRedacted, Error: "canceled on restart: accept incomplete",
		})
		if err != nil {
			return st, err
		}
	}
	// Legacy pending_approval rows (pre-autonomy builds) → canceled.
	for _, t := range store.List(StatePendingApproval) {
		err := store.UpdateAndAudit(t.ID, func(tk *Task) {
			tk.State = StateCanceled
			tk.Error = "canceled on restart: legacy pending_approval"
		}, audit.Event{
			TaskID: t.ID, Verb: t.Verb, State: StateCanceled,
			ArgvRedacted: t.ArgvRedacted, Error: "canceled on restart: legacy pending_approval",
		})
		if err != nil {
			return st, err
		}
	}
	return st, nil
}
