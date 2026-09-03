package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/api"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func setup(t *testing.T) (*api.Server, string, func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh shim")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "termux-battery-status")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho '{\"percentage\":42,\"status\":\"DISCHARGING\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cat, err := verbs.Parse([]byte(`
version: 1
daemon:
  listen: "127.0.0.1:0"
  approval_mode: ask
  approval_backend: dialog
  task_timeout_s: 5
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 100
  stream_buffer_default: 128
verbs:
  - name: battery.status
    tier: A
    risk: none
    approval: inherit
    argv: ["termux-battery-status"]
    parser: json
    watch: false
`))
	if err != nil {
		t.Fatal(err)
	}
	// override listen validation already passed with 127.0.0.1:0 — port 0 ok for tests via manual ln
	tok, err := auth.LoadOrCreate(filepath.Join(dir, ".agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	s := api.New(cat, tok, queue.NewMemory())
	s.SyncExec = true

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go srv.Serve(ln)
	base := "http://" + ln.Addr().String()
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = ln.Close()
	}
	return s, base, cleanup
}

func TestUnauthorized(t *testing.T) {
	_, base, cleanup := setup(t)
	defer cleanup()
	res, err := http.Get(base + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.StatusCode)
	}
}

func TestBatteryRoundTrip(t *testing.T) {
	s, base, cleanup := setup(t)
	defer cleanup()
	tok := s.Token.String()

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/battery.status", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.Header, tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != queue.StateExecuted {
		t.Fatalf("status=%s want executed body=%s", out.Status, body)
	}

	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	greq.Header.Set(auth.Header, tok)
	gres, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer gres.Body.Close()
	var task queue.Task
	if err := json.NewDecoder(gres.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.State != queue.StateExecuted {
		t.Fatalf("task=%+v", task)
	}
	m, ok := task.Result.(map[string]any)
	if !ok {
		// json numbers decode as float64 via encoding/json on interface
		t.Fatalf("result type %T val=%v", task.Result, task.Result)
	}
	if m["percentage"] == nil {
		t.Fatalf("result=%v", m)
	}

	// wrong token
	bad, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/battery.status", bytes.NewReader([]byte(`{}`)))
	bad.Header.Set(auth.Header, "wrong")
	bres, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer bres.Body.Close()
	if bres.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", bres.StatusCode)
	}
}

func TestUnknownVerb(t *testing.T) {
	s, base, cleanup := setup(t)
	defer cleanup()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/no.such", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(auth.Header, s.Token.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", res.StatusCode)
	}
}
