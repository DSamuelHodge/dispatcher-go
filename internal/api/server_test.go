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
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/api"
	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
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
  - name: sms.send
    tier: B
    risk: high
    approval: ask
    force_ask_even_if_global_auto: true
    argv: ["termux-sms-send", "-n", "{{.number}}"]
    args:
      - {name: number, flag: -n, type: string, required: true}
    stdin_arg: {arg: text}
    timeout_s: 30
    parser: exit
    watch: false
  - name: clipboard.set
    tier: B
    risk: medium
    approval: inherit
    argv: ["termux-clipboard-set"]
    stdin_arg: {arg: text}
    parser: exit
    watch: false
`

func setup(t *testing.T, prompter approve.Prompter) (*api.Server, string, *audit.Logger, func()) {
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
	if prompter != nil {
		s.Prompter = prompter
	} else {
		s.Prompter = approve.StaticPrompter{Approve: true}
	}

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
	_, base, _, cleanup := setup(t, nil)
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
	s, base, _, cleanup := setup(t, nil)
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

func TestSMSSendDeniedNoRetry(t *testing.T) {
	s, base, log, cleanup := setup(t, approve.StaticPrompter{Approve: false})
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
	if out.Status != queue.StateDenied {
		t.Fatalf("want denied got %s (%s)", out.Status, raw)
	}
	// GET task must not contain secret
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/tasks/"+out.TaskID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, _ := http.DefaultClient.Do(greq)
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("secret leaked in task GET: %s", gbody)
	}
	if log.Contains(secret) {
		t.Fatal("secret leaked in audit")
	}
	// denied never becomes retry_scheduled / pending again
	task, _ := s.Tasks.Get(out.TaskID)
	if task.State != queue.StateDenied {
		t.Fatalf("state=%s", task.State)
	}
}

func TestSMSSendApproved(t *testing.T) {
	s, base, log, cleanup := setup(t, approve.StaticPrompter{Approve: true})
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
	gres, _ := http.DefaultClient.Do(greq)
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("secret in GET %s", gbody)
	}
}

func TestClipboardRedaction(t *testing.T) {
	// clipboard inherits ask + tier B → needs prompt; approve auto
	s, base, log, cleanup := setup(t, approve.StaticPrompter{Approve: true})
	defer cleanup()
	secret := "clipboard-secret-42"
	b, _ := json.Marshal(map[string]any{"stdin": secret})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/clipboard.set", bytes.NewReader(b))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
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
	gres, _ := http.DefaultClient.Do(greq)
	defer gres.Body.Close()
	gbody, _ := io.ReadAll(gres.Body)
	if strings.Contains(string(gbody), secret) {
		t.Fatalf("GET leak %s", gbody)
	}
}

func TestForceAskBeatsPolicyAlways(t *testing.T) {
	s, base, _, cleanup := setup(t, approve.StaticPrompter{Approve: false})
	defer cleanup()
	s.Policy = approve.PolicyFile{ApprovalMode: "always-approve"}
	b, _ := json.Marshal(map[string]any{"args": map[string]any{"number": "1"}, "stdin": "x"})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/verbs/sms.send", bytes.NewReader(b))
	req.Header.Set(auth.Header, s.Token.String())
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Status != queue.StateDenied {
		t.Fatalf("force_ask should still prompt/deny, got %s %s", out.Status, raw)
	}
}

func TestUnknownVerb(t *testing.T) {
	s, base, _, cleanup := setup(t, nil)
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
	s, base, _, cleanup := setup(t, nil)
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
	s, base, _, cleanup := setup(t, nil)
	defer cleanup()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "termux-location"), []byte("#!/bin/sh\nwhile true; do echo loc-$(date +%s); sleep 0.05; done\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.Streams = streams.NewRegistry(8)
	// inject stream verb into catalog
	cat2, err := verbs.Parse([]byte(catalogYAML + `
  - name: location.stream
    tier: B
    risk: medium
    approval: always-approve
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
		Name: "location.stream", Tier: verbs.TierB, Risk: verbs.RiskMedium,
		Approval: verbs.ApprovalAlwaysApprove,
		Argv:     []string{"termux-location", "-p", "gps", "-r", "updates"},
		Watch:    map[string]any{"mode": "stream", "buffer": float64(8)},
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
	dres, _ := http.DefaultClient.Do(dreq)
	dres.Body.Close()
	if dres.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", dres.StatusCode)
	}
	// 404 after delete
	greq, _ := http.NewRequest(http.MethodGet, base+"/v1/streams/"+out.StreamID, nil)
	greq.Header.Set(auth.Header, s.Token.String())
	gres, _ := http.DefaultClient.Do(greq)
	gres.Body.Close()
	if gres.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", gres.StatusCode)
	}
}
