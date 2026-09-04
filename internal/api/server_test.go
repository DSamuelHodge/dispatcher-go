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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/api"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/streams"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

const catalogYAML = `
version: 1
daemon:
  listen: "127.0.0.1:0"
  task_timeout_s: 30
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 100
  stream_buffer_default: 128
verbs:
  - name: battery.status
    tier: A
    argv: ["termux-battery-status"]
    parser: json
    watch: false
  - name: sms.send
    tier: B
    argv: ["termux-sms-send", "-n", "{{.number}}"]
    args:
      - {name: number, flag: -n, type: string, required: true}
    stdin_arg: {arg: text}
    timeout_s: 30
    parser: exit
    watch: false
  - name: clipboard.set
    tier: B
    argv: ["termux-clipboard-set"]
    stdin_arg: {arg: text}
    parser: exit
    watch: false
`

func setup(t *testing.T) (*api.Server, string, *audit.Logger, func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh shim")
	}
	dir := t.TempDir()
	writeShim(t, dir, "termux-battery-status", "#!/bin/sh\necho '{\"percentage\":42,\"status\":\"DISCHARGING\"}'\n")
	writeShim(t, dir, "termux-sms-send", "#!/bin/sh\nexit 0\n")
	writeShim(t, dir, "termux-clipboard-set", "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cat, err := verbs.Parse([]byte(catalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := auth.LoadOrCreate(filepath.Join(dir, ".agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open("")
	if err != nil {
		t.Fatal(err)
	}
	store := queue.NewMemory()
	store.SetLogger(log)
	s := api.New(cat, tok, store)
	s.SyncExec = true
	s.Audit = log

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
		_ = log.Close()
	}
	return s, base, log, cleanup
}

func writeShim(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestUnauthorized(t *testing.T) {
	_, base, _, cleanup := setup(t)
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
	s, base, _, cleanup := setup(t)
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
}

func TestSMSSendExecutesWithRedaction(t *testing.T) {
	s, base, log, cleanup := setup(t)
	defer cleanup()
	secret := "super-secret-message-xyz"
	payload := map[string]any{
		"args":  map[string]any{"number": "5551234"},
		"stdin": secret,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/sms.send", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.Header, s.Token.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Status != queue.StateExecuted {
		t.Fatalf("want executed got %s (%s)", out.Status, raw)
	}
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("secret leaked in task GET: %s", gbody)
	}
	if log.Contains(secret) {
		t.Fatal("secret leaked in audit")
	}
}

func TestSMSSendApproved(t *testing.T) {
	s, base, log, cleanup := setup(t)
	defer cleanup()
	secret := "another-secret-abc"
	payload := map[string]any{
		"args":  map[string]any{"number": "5559999"},
		"stdin": secret,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/sms.send", bytes.NewReader(b))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out struct {
		Status string `json:"status"`
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Status != queue.StateExecuted {
		t.Fatalf("want executed got %s %s", out.Status, raw)
	}
	if log.Contains(secret) {
		t.Fatal("secret in audit")
	}
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("secret in GET %s", gbody)
	}
}

func TestClipboardRedaction(t *testing.T) {
	// clipboard inherits ask + tier B → needs prompt; approve auto
	s, base, log, cleanup := setup(t)
	defer cleanup()
	secret := "clipboard-secret-42"
	b, _ := json.Marshal(map[string]any{"stdin": secret})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/clipboard.set", bytes.NewReader(b))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if log.Contains(secret) {
		t.Fatal("audit leak")
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(raw, &out)
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("GET leak %s", gbody)
	}
}

func TestUnknownVerb(t *testing.T) {
	s, base, _, cleanup := setup(t)
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

func TestCircuitOpen503(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	s.Circuits = circuit.NewRegistry(1, time.Minute)
	// trip battery breaker
	br := s.Circuits.For("battery.status", 1)
	br.Failure()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/battery.status", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "circuit_open") {
		t.Fatalf("%s", body)
	}
}

func TestStreamLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	s, base, _, cleanup := setup(t)
	defer cleanup()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "termux-location"), []byte("#!/bin/sh\nwhile true; do echo loc-$(date +%s); sleep 0.05; done\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.Streams = streams.NewRegistry(8)
	// inject stream verb into catalog
	cat2, err := verbs.Parse([]byte(catalogYAML + `
  - name: location.stream
    tier: B
    argv: ["termux-location", "-p", "gps", "-r", "updates"]
    parser: json
    watch: {mode: stream, buffer: 8}
`))
	if err != nil {
		// catalogYAML already complete — merge by reparse full
		t.Log(err)
	}
	_ = cat2
	// use existing setup catalog — add location.stream via direct map
	s.Catalog.ByName["location.stream"] = verbs.Verb{
		Name:  "location.stream",
		Tier:  verbs.TierB,
		Argv:  []string{"termux-location", "-p", "gps", "-r", "updates"},
		Watch: map[string]any{"mode": "stream", "buffer": float64(8)},
	}
	s.Catalog.Order = append(s.Catalog.Order, "location.stream")

	body, _ := json.Marshal(map[string]any{"verb": "location.stream", "args": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/streams", bytes.NewReader(body))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	var out struct {
		StreamID string `json:"stream_id"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.StreamID == "" {
		t.Fatal(raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	var events int
	for time.Now().Before(deadline) {
		greq, _ := http.NewRequest(http.MethodGet, base+"/v1/streams/"+out.StreamID+"?since=0", nil)
		greq.Header.Set(auth.Header, s.Token.String())
		gres, err := http.DefaultClient.Do(greq)
		if err != nil {
			t.Fatal(err)
		}
		gb, _ := io.ReadAll(gres.Body)
		gres.Body.Close()
		var g struct {
			Events []any `json:"events"`
		}
		_ = json.Unmarshal(gb, &g)
		events = len(g.Events)
		if events > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if events < 1 {
		t.Fatal("no stream events")
	}
	dreq, _ := http.NewRequest(http.MethodDelete, base+"/v1/streams/"+out.StreamID, nil)
	dreq.Header.Set(auth.Header, s.Token.String())
	dres, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	dres.Body.Close()
	if dres.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", dres.StatusCode)
	}
	// 404 after delete
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/streams/"+out.StreamID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	gres.Body.Close()
	if gres.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", gres.StatusCode)
	}
}

func postVerbHelper(t *testing.T, base, tok, verb, payload string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/"+verb, bytes.NewReader([]byte(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.Header, tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	tok := s.Token.String()

	st1, b1 := postVerbHelper(t, base, tok, "battery.status", `{"idempotency_key":"k-1"}`)
	if st1 != http.StatusAccepted {
		t.Fatalf("first=%d %s", st1, b1)
	}
	var first struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(b1, &first)

	st2, b2 := postVerbHelper(t, base, tok, "battery.status", `{"idempotency_key":"k-1"}`)
	if st2 != http.StatusOK {
		t.Fatalf("replay=%d %s", st2, b2)
	}
	var second struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
		Replay bool   `json:"replay"`
	}
	_ = json.Unmarshal(b2, &second)
	if !second.Replay || second.TaskID != first.TaskID {
		t.Fatalf("want replay of %s, got %+v", first.TaskID, second)
	}
	if n := len(s.Tasks.List("")); n != 1 {
		t.Fatalf("tasks=%d, want 1 (no duplicate)", n)
	}

	// Same key, different request → 409.
	st3, b3 := postVerbHelper(t, base, tok, "sms.send",
		`{"args":{"number":"123"},"idempotency_key":"k-1"}`)
	if st3 != http.StatusConflict {
		t.Fatalf("conflict=%d %s", st3, b3)
	}
	if !strings.Contains(string(b3), "idempotency_conflict") {
		t.Fatalf("%s", b3)
	}
}

func TestSyncExecFailureSchedulesRetry(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	// Shadow the succeeding shim with a failing one.
	dir := t.TempDir()
	writeShim(t, dir, "termux-battery-status", "#!/bin/sh\nexit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	st, body := postVerbHelper(t, base, s.Token.String(), "battery.status", `{}`)
	if st != http.StatusAccepted {
		t.Fatalf("status=%d %s", st, body)
	}
	var out struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Status != queue.StateRetryScheduled {
		t.Fatalf("status=%s, want retry_scheduled (spec §9, not terminal failed)", out.Status)
	}
	got, ok := s.Tasks.Get(out.TaskID)
	if !ok {
		t.Fatal("task lost")
	}
	if got.Attempt != 1 || got.NextRunAt == nil {
		t.Fatalf("attempt=%d next=%v, want 1 + scheduled", got.Attempt, got.NextRunAt)
	}
	if got.LastAttemptOutcome != "failed" || got.Error != "exit status 3" {
		t.Fatalf("outcome=%q err=%q", got.LastAttemptOutcome, got.Error)
	}
}

func injectLocationStream(t *testing.T, s *api.Server, dir string) {
	t.Helper()
	writeShim(t, dir, "termux-location", "#!/bin/sh\nwhile true; do echo loc; sleep 0.05; done\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.Catalog.ByName["location.stream"] = verbs.Verb{
		Name:  "location.stream",
		Tier:  verbs.TierB,
		Argv:  []string{"termux-location", "-p", "gps", "-r", "updates"},
		Watch: map[string]any{"mode": "stream", "buffer": float64(8)},
	}
	s.Catalog.Order = append(s.Catalog.Order, "location.stream")
}

func postStreamHelper(t *testing.T, base, tok string) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"verb": "location.stream", "args": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/streams", bytes.NewReader(payload))
	req.Header.Set(auth.Header, tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

func TestStreamStartsWithoutGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	s, base, _, cleanup := setup(t)
	defer cleanup()
	s.Streams = streams.NewRegistry(8)
	injectLocationStream(t, s, t.TempDir())
	st, body := postStreamHelper(t, base, s.Token.String())
	if st != http.StatusAccepted {
		t.Fatalf("stream: status=%d %s, want 202", st, body)
	}
}

func TestQueueFullConcurrent(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	// max_queue_depth is 100 in fixture; run 120 concurrent creates and
	// assert store depth never exceeds it and losers get 503.
	var mu sync.Mutex
	full503 := 0
	var wg sync.WaitGroup
	for i := 0; i < 120; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/battery.status", bytes.NewReader([]byte(`{}`)))
			req.Header.Set(auth.Header, s.Token.String())
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()
			if res.StatusCode == http.StatusServiceUnavailable {
				mu.Lock()
				full503++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if s.Tasks.Depth() > 100 {
		t.Fatalf("depth %d exceeds max", s.Tasks.Depth())
	}
	if full503 == 0 {
		t.Fatal("expected some 503 queue_full under flood")
	}
}

func TestHealthAutonomy(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/health", nil)
	req.Header.Set(auth.Header, s.Token.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Autonomy string `json:"autonomy"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Autonomy != "full" {
		t.Fatalf("%+v", out)
	}
}

func TestProgressiveVerbDiscovery(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	tok := s.Token.String()

	// default list is summary (no argv)
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/verbs", nil)
	req.Header.Set(auth.Header, tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var listed struct {
		Detail string           `json:"detail"`
		Count  int              `json:"count"`
		Verbs  []map[string]any `json:"verbs"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Detail != "summary" || listed.Count < 1 {
		t.Fatalf("%s", raw)
	}
	if _, ok := listed.Verbs[0]["argv"]; ok {
		t.Fatalf("default list leaked argv: %s", raw)
	}

	// names
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/verbs?detail=names", nil)
	req.Header.Set(auth.Header, tok)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var names struct {
		Verbs []string `json:"verbs"`
	}
	_ = json.Unmarshal(raw, &names)
	if len(names.Verbs) < 1 {
		t.Fatalf("%s", raw)
	}

	// search
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/verbs/search?q=sms&limit=5", nil)
	req.Header.Set(auth.Header, tok)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var search struct {
		Hits []struct {
			Name string `json:"name"`
		} `json:"hits"`
	}
	_ = json.Unmarshal(raw, &search)
	if len(search.Hits) < 1 || search.Hits[0].Name != "sms.send" {
		t.Fatalf("%s", raw)
	}

	// get one full schema
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/verbs/sms.send", nil)
	req.Header.Set(auth.Header, tok)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var one map[string]any
	_ = json.Unmarshal(raw, &one)
	if one["name"] != "sms.send" {
		t.Fatalf("%s", raw)
	}
	if _, ok := one["args"]; !ok {
		t.Fatalf("missing args: %s", raw)
	}
	if _, ok := one["stdin_arg"]; !ok {
		t.Fatalf("missing stdin_arg: %s", raw)
	}
}

func TestTaskProjectionCompactByDefault(t *testing.T) {
	s, base, _, cleanup := setup(t)
	defer cleanup()
	st, body := postVerbHelper(t, base, s.Token.String(), "battery.status", `{}`)
	if st != http.StatusAccepted {
		t.Fatalf("%d %s", st, body)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(body, &out)

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	req.Header.Set(auth.Header, s.Token.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var compact map[string]any
	_ = json.Unmarshal(raw, &compact)
	if compact["state"] != queue.StateExecuted {
		t.Fatalf("%s", raw)
	}
	if _, ok := compact["stdout"]; ok {
		t.Fatalf("default task GET should omit stdout: %s", raw)
	}
	if _, ok := compact["result"]; !ok {
		t.Fatalf("expected result in compact: %s", raw)
	}

	// full still available
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID+"?detail=full", nil)
	req.Header.Set(auth.Header, s.Token.String())
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var full map[string]any
	_ = json.Unmarshal(raw, &full)
	if _, ok := full["argv_redacted"]; !ok {
		t.Fatalf("full missing argv_redacted: %s", raw)
	}
}
