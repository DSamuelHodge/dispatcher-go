// Package notify sends operator alerts (exhaustion).
package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
)

// Notifier delivers a user-visible alert.
type Notifier interface {
	Exhausted(ctx context.Context, verb, taskID string, attempts int) error
}

// Termux sends termux-notification (allowlisted; may bypass circuit later).
type Termux struct{}

func (Termux) Exhausted(ctx context.Context, verb, taskID string, attempts int) error {
	title := fmt.Sprintf("dispatcher: %s exhausted", verb)
	content := fmt.Sprintf("task %s after %d attempts", taskID, attempts)
	argv := []string{"termux-notification", "--title", title, "-c", content}
	res := execx.Run(ctx, argv, "", 15*time.Second)
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// LogNotifier records calls for tests.
type LogNotifier struct {
	Calls []string
}

func (n *LogNotifier) Exhausted(ctx context.Context, verb, taskID string, attempts int) error {
	n.Calls = append(n.Calls, fmt.Sprintf("%s|%s|%d", verb, taskID, attempts))
	return nil
}
