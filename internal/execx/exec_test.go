package execx_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
