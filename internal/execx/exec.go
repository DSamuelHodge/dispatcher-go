// Package execx runs termux-* argv templates via os/exec with no shell.
package execx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MaxCapture is the stdout/stderr retention cap for task records (4 KiB).
const MaxCapture = 4 * 1024

// Result is the outcome of one process run.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Parsed   any // set when parser=json and stdout parses
	Err      error
	TimedOut bool
}

// Run executes argv[0] with argv[1:] directly (no shell). stdin may be nil.
// lookPath is optional; empty uses exec.LookPath.
func Run(ctx context.Context, argv []string, stdin string, timeout time.Duration) Result {
	if len(argv) == 0 {
		return Result{ExitCode: -1, Err: fmt.Errorf("empty argv")}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Keep inheriting the full environment: Termux binaries rely on
	// os.Environ() (PATH and friends) to resolve termux-* helpers.
	cmd.Env = os.Environ()
	// Run the child in its own process group so a timeout can kill the
	// whole process tree, not just the direct child. No-op on platforms
	// without process-group support (see pgroup_*.go).
	configureProcessGroup(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// If the context expires (one-shot timeout or caller cancel),
	// exec.CommandContext kills only the direct child. Kill the whole
	// process group promptly so orphaned grandchildren don't survive.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-stopWatch:
		}
	}()
	err := cmd.Run()
	res := Result{
		Stdout: truncate(stdout.String(), MaxCapture),
		Stderr: truncate(stderr.String(), MaxCapture),
	}
	if ctx.Err() == context.DeadlineExceeded {
		// Belt-and-suspenders: the watcher may have raced with Run
		// returning, so ensure the group is dead before reporting.
		killProcessGroup(cmd)
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = fmt.Errorf("timeout after %s", timeout)
		return res
	}
	if err != nil {
		res.Err = err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		return res
	}
	res.ExitCode = 0
	return res
}

// ParseJSON tries to decode stdout as JSON into a generic value.
func ParseJSON(stdout string) (any, error) {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return nil, fmt.Errorf("empty stdout")
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// ensure no trailing junk
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return v, fmt.Errorf("trailing data after JSON")
	}
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
