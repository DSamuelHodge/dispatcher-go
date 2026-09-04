//go:build linux || android || darwin

package execx

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so the
// whole tree can be signalled via kill(-pid, ...). Linux, Android and
// macOS all support Setpgid and negative-pid kills.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the child's process group. Falls back to
// killing the direct child if the group id can't be resolved.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
