package execx_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
)

func TestRunEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shim uses sh")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "termux-battery-status")
	script := "#!/bin/sh\necho '{\"percentage\":85,\"status\":\"CHARGING\"}'\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	res := execx.Run(context.Background(), []string{"termux-battery-status"}, "", 5*time.Second)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("run: %+v", res)
	}
	v, err := execx.ParseJSON(res.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("parsed type %T", v)
	}
	if _, ok := m["percentage"]; !ok {
		t.Fatalf("parsed=%v", m)
	}
}

func TestTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "sleepish")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	res := execx.Run(context.Background(), []string{"sleepish"}, "", 200*time.Millisecond)
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "android", "darwin":
	default:
		t.Skip("process-group kill only supported on linux/android/darwin")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	shim := filepath.Join(dir, "sleep-tree")
	// Spawn a grandchild sleep in the background, record its pid, and wait:
	// with only direct-child kill, the background sleep would survive.
	script := "#!/bin/sh\nsleep 30 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Generous timeout: cold process-spawn latency can exceed a few
	// hundred ms on loaded CI/dev machines; the grandchild sleeps 30s
	// so the timeout still fires while the tree is alive.
	res := execx.Run(context.Background(), []string{"sleep-tree"}, "", 5*time.Second)
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("grandchild pid file missing: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad grandchild pid %q: %v", raw, err)
	}
	// Poll until the grandchild is gone; fail if it outlives the timeout.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH/EPERM: process gone (or reaped)
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild pid %d survived timeout kill", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
