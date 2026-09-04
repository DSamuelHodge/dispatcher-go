package streams_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/streams"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func TestRingEvicts(t *testing.T) {
	r := streams.NewRing(3)
	r.Push("t", "a")
	r.Push("t", "b")
	r.Push("t", "c")
	r.Push("t", "d")
	ev := r.Since(0)
	if len(ev) != 3 || ev[0].Line != "b" || ev[2].Line != "d" {
		t.Fatalf("%+v", ev)
	}
	ev2 := r.Since(ev[0].Seq)
	if len(ev2) != 2 {
		t.Fatalf("%+v", ev2)
	}
}

func TestStartGetDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "termux-location")
	// emit lines slowly
	script := "#!/bin/sh\nfor i in 1 2 3 4 5; do echo line-$i; sleep 0.05; done; sleep 10\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	v := verbs.Verb{Name: "location.stream", Argv: []string{"termux-location"}, Watch: map[string]any{"mode": "stream", "buffer": 128}}
	reg := streams.NewRegistry(128)
	s, err := reg.Start(v, []string{"termux-location"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.Ring.Len() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if s.Ring.Len() < 1 {
		t.Fatal("no events")
	}
	got, ok := reg.Get(s.ID)
	if !ok || got.ID != s.ID {
		t.Fatal("get")
	}
	if err := reg.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get(s.ID); ok {
		t.Fatal("still present")
	}
	// process should be dead
	time.Sleep(50 * time.Millisecond)
}

func TestStartRejectsInvalidBuffer(t *testing.T) {
	cases := map[string]any{
		"zero":    0,
		"neg":     -5,
		"over":    4097,
		"huge":    1 << 20,
		"float":   float64(5000),
		"float0":  float64(0),
		"int64ov": int64(8192),
	}
	for name, buf := range cases {
		t.Run(name, func(t *testing.T) {
			v := verbs.Verb{Name: "location.stream", Watch: map[string]any{"mode": "stream", "buffer": buf}}
			reg := streams.NewRegistry(128)
			if _, err := reg.Start(v, []string{"sleep", "10"}); !errors.Is(err, streams.ErrInvalidBuffer) {
				t.Fatalf("buffer=%v: want ErrInvalidBuffer, got %v", buf, err)
			}
			if reg.Count() != 0 {
				t.Fatalf("count=%d, want 0", reg.Count())
			}
		})
	}
}

func TestStartRejectsBadDefaultBuffer(t *testing.T) {
	v := verbs.Verb{Name: "location.stream", Watch: map[string]any{"mode": "stream"}}
	reg := streams.NewRegistry(8192)
	if _, err := reg.Start(v, []string{"sleep", "10"}); !errors.Is(err, streams.ErrInvalidBuffer) {
		t.Fatalf("want ErrInvalidBuffer, got %v", err)
	}
}

func TestStartAcceptsMaxBuffer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	v := verbs.Verb{Name: "location.stream", Watch: map[string]any{"mode": "stream", "buffer": 4096}}
	reg := streams.NewRegistry(128)
	s, err := reg.Start(v, []string{"sleep", "10"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.CloseAll()
	if s.Ring == nil {
		t.Fatal("nil ring")
	}
	if err := reg.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStartCapsActiveStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	reg := streams.NewRegistry(8)
	defer reg.CloseAll()
	v := verbs.Verb{Name: "location.stream", Watch: map[string]any{"mode": "stream", "buffer": 8}}
	for i := 0; i < streams.MaxStreams; i++ {
		if _, err := reg.Start(v, []string{"sleep", "10"}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if reg.Count() != streams.MaxStreams {
		t.Fatalf("count=%d, want %d", reg.Count(), streams.MaxStreams)
	}
	if _, err := reg.Start(v, []string{"sleep", "10"}); !errors.Is(err, streams.ErrTooManyStreams) {
		t.Fatalf("want ErrTooManyStreams, got %v", err)
	}
}

func TestStreamErrSurfacesScannerLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "longline.sh")
	// Single 1.1MB line exceeds the 1MB scanner limit -> "token too long".
	script := "#!/bin/sh\nawk 'BEGIN{for(i=0;i<1100000;i++)printf \"A\"; printf \"\\n\"}'\nsleep 5\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	v := verbs.Verb{Name: "sensor.stream", Watch: map[string]any{"mode": "stream", "buffer": 8}}
	reg := streams.NewRegistry(8)
	defer reg.CloseAll()
	s, err := reg.Start(v, []string{shim})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Err(); got != nil {
		t.Fatalf("fresh stream Err=%v, want nil", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	var serr error
	for time.Now().Before(deadline) {
		if serr = s.Err(); serr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if serr == nil {
		t.Fatal("stream error never surfaced")
	}
	if !strings.Contains(serr.Error(), "too long") {
		t.Fatalf("Err=%q, want it to mention \"too long\"", serr.Error())
	}
}
