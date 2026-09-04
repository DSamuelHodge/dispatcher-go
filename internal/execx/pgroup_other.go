//go:build !linux && !android && !darwin

package execx

import (
	"os/exec"
)

// configureProcessGroup is a no-op where process groups are unsupported;
// the timeout path falls back to killing the direct child.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing the direct child only.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
