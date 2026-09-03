package streams_test

import (
	"os"
	"path/filepath"
	"runtime"
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
