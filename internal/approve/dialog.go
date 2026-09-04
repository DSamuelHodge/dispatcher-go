package approve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
)

// DefaultPromptTimeout is the confirm dialog block limit (spec §7).
const DefaultPromptTimeout = 120 * time.Second

// Result of an approval prompt.
type Result struct {
	Approved bool
	By       string // "user" | "policy"
	Raw      string
	Err      error
	TimedOut bool
}

// Prompter asks the device owner to confirm a verb.
type Prompter interface {
	Confirm(ctx context.Context, title, body string, timeout time.Duration) Result
}

// DialogPrompter runs termux-dialog confirm (MVP backend).
type DialogPrompter struct{}

// Confirm implements Prompter.
func (DialogPrompter) Confirm(ctx context.Context, title, body string, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = DefaultPromptTimeout
	}
	// termux-dialog confirm -t TITLE  (text via -i input hint optional)
	// Use -t for title; put redacted summary as extra arg text when supported.
	argv := []string{"termux-dialog", "confirm", "-t", title}
	if body != "" {
		// -i sets input; for confirm, some builds accept description via leftover — use -i for hint
		argv = append(argv, "-i", body)
	}
	res := execx.Run(ctx, argv, "", timeout)
	out := Result{Raw: res.Stdout, By: "user"}
	if res.TimedOut {
		out.TimedOut = true
		out.Err = res.Err
		return out
	}
	if res.Err != nil && res.ExitCode != 0 {
		out.Err = res.Err
		// still try parse
	}
	ok, perr := parseConfirmJSON(res.Stdout)
	if perr != nil {
		// Keep the underlying exec failure: an empty/unparseable stdout
		// must not swallow the reason the dialog command failed.
		if res.Err != nil {
			out.Err = errors.Join(perr, res.Err)
		} else {
			out.Err = perr
		}
		return out
	}
	out.Approved = ok
	return out
}

// parseConfirmJSON understands termux-dialog confirm stdout:
// {"code":0,"text":"yes"} or similar.
func parseConfirmJSON(stdout string) (bool, error) {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return false, fmt.Errorf("empty dialog stdout")
	}
	// bare yes/no
	low := strings.ToLower(s)
	if low == "yes" || low == "y" {
		return true, nil
	}
	if low == "no" || low == "n" {
		return false, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return false, fmt.Errorf("dialog json: %w", err)
	}
	// Cancellation wins over the text field: a dismissed dialog reports
	// code -1, and {text:yes, code:-1} must deny, never approve.
	if c, ok := m["code"]; ok {
		switch v := c.(type) {
		case float64:
			if v == -1 {
				return false, nil
			}
		case int:
			if v == -1 {
				return false, nil
			}
		case int64:
			if v == -1 {
				return false, nil
			}
		case json.Number:
			if i, err := v.Int64(); err == nil && i == -1 {
				return false, nil
			}
		}
	}
	// text field
	if t, ok := m["text"]; ok {
		ts := strings.ToLower(strings.TrimSpace(fmt.Sprint(t)))
		if ts == "yes" || ts == "y" {
			return true, nil
		}
		if ts == "no" || ts == "n" || ts == "" {
			return false, nil
		}
	}
	return false, nil
}

// StaticPrompter is a test double.
type StaticPrompter struct {
	Approve bool
	Err     error
	Delay   time.Duration
}

// Confirm implements Prompter.
func (p StaticPrompter) Confirm(ctx context.Context, title, body string, timeout time.Duration) Result {
	if p.Delay > 0 {
		select {
		case <-ctx.Done():
			return Result{TimedOut: true, Err: ctx.Err(), By: "user"}
		case <-time.After(p.Delay):
		}
	}
	if p.Err != nil {
		return Result{Err: p.Err, By: "user"}
	}
	return Result{Approved: p.Approve, By: "user", Raw: fmt.Sprintf("title=%s body=%s", title, body)}
}

// AutoPrompter never prompts (always-approve path uses this as no-op).
type AutoPrompter struct{}

func (AutoPrompter) Confirm(context.Context, string, string, time.Duration) Result {
	return Result{Approved: true, By: "policy"}
}
