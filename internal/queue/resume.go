package queue

import (
	"fmt"
	"time"
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
	// accepted left over mid-request should become pending if approved already;
	// keep accepted as-is (will be rare); treat accepted as reclaimable pending for safety
	for _, t := range store.List(StateAccepted) {
		_ = store.Update(t.ID, func(tk *Task) {
			tk.State = StatePending
			tk.Error = "resumed after crash (was accepted)"
		})
		st.ExecutingToPending++ // count as recovered
	}
	// pending_approval at crash → denied (cannot complete dialog safely) — safer than hanging
	for _, t := range store.List(StatePendingApproval) {
		_ = store.Update(t.ID, func(tk *Task) {
			tk.State = StateDenied
			tk.Error = "approval interrupted by crash"
		})
	}
	return st, nil
}
