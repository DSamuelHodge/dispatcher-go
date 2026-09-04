package verbs_test

import (
	"strings"
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

const minimalYAML = `
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 30
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 1024
  stream_buffer_default: 128
verbs:
  - name: battery.status
    tier: A
    argv: ["termux-battery-status"]
    parser: json
    watch: false
`

func TestParseMinimal(t *testing.T) {
	cat, err := verbs.Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cat.ByName) != 1 {
		t.Fatalf("verbs=%d want 1", len(cat.ByName))
	}
	v, ok := cat.Get("battery.status")
	if !ok || v.Argv[0] != "termux-battery-status" {
		t.Fatalf("Get battery.status: ok=%v v=%+v", ok, v)
	}
}

func TestRejectUnknownBinary(t *testing.T) {
	y := strings.Replace(minimalYAML, "termux-battery-status", "termux-not-a-real-bin", 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("want allowlist error, got %v", err)
	}
}

func TestRejectUnknownFlag(t *testing.T) {
	y := strings.Replace(minimalYAML,
		`argv: ["termux-battery-status"]`,
		`argv: ["termux-battery-status", "--bogus"]`, 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("want unknown flag error, got %v", err)
	}
}

func TestRejectTierAMutating(t *testing.T) {
	y := strings.Replace(minimalYAML, "termux-battery-status", "termux-sms-send", 1)
	y = strings.Replace(y, `argv: ["termux-sms-send"]`, `argv: ["termux-sms-send", "-n", "{{.number}}"]`, 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "mutating") {
		t.Fatalf("want mutating tier A error, got %v", err)
	}
}

func TestRejectNonLoopbackListen(t *testing.T) {
	y := strings.Replace(minimalYAML, "127.0.0.1:8477", "0.0.0.0:8477", 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("want loopback error, got %v", err)
	}
}

func TestAllowTemplateTokens(t *testing.T) {
	y := strings.Replace(minimalYAML,
		`- name: battery.status
    tier: A
    argv: ["termux-battery-status"]
    parser: json
    watch: false`,
		`- name: location.once
    tier: A
    argv: ["termux-location", "-p", "{{.provider}}", "-r", "once"]
    parser: json
    watch: false`, 1)
	cat, err := verbs.Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := cat.Get("location.once"); !ok {
		t.Fatal("missing location.once")
	}
}

func TestLoadRepoVerbsYAML(t *testing.T) {
	cat, err := verbs.Load("../../verbs.yaml")
	if err != nil {
		t.Fatalf("Load repo verbs.yaml: %v", err)
	}
	if len(cat.ByName) < 5 {
		t.Fatalf("expected seed catalog, got %d verbs", len(cat.ByName))
	}
	if _, ok := cat.Get("battery.status"); !ok {
		t.Fatal("seed missing battery.status")
	}
	// alias
	if _, ok := cat.Get("wifi.connection-info"); !ok {
		// only works if wifi.info present
		if _, ok2 := cat.Get("wifi.info"); ok2 {
			t.Fatal("alias wifi.connection-info should resolve when wifi.info exists")
		}
	}
}

func TestRejectUnknownVerbField(t *testing.T) {
	y := strings.Replace(minimalYAML,
		`    parser: json`,
		`    parser: json
    timeou_s: 5`, 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "timeou_s") {
		t.Fatalf("want strict unknown-field error for timeou_s, got %v", err)
	}
}

func TestRejectUnknownDaemonField(t *testing.T) {
	y := strings.Replace(minimalYAML,
		`  max_retries: 5`,
		`  max_retries: 5
  task_timeout_se: 30`, 1)
	_, err := verbs.Parse([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "task_timeout_se") {
		t.Fatalf("want strict unknown-field error for task_timeout_se, got %v", err)
	}
}

func TestRejectUnknownArgField(t *testing.T) {
	base := `
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 30
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 1024
  stream_buffer_default: 128
verbs:
  - name: call-log.read
    tier: A
    argv: ["termux-call-log"]
    args:
      - {name: limit, flag: -l, type: int, required: false, requird: true}
    parser: json
    watch: false
`
	_, err := verbs.Parse([]byte(base))
	if err == nil || !strings.Contains(err.Error(), "requird") {
		t.Fatalf("want strict unknown-field error for requird, got %v", err)
	}
}

func TestStreamAllowlist(t *testing.T) {
	streamVerb := func(name, argv0 string) string {
		return strings.Replace(minimalYAML,
			`- name: battery.status
    tier: A
    argv: ["termux-battery-status"]
    parser: json
    watch: false`,
			`- name: `+name+`
    tier: B
    argv: ["`+argv0+`"]
    parser: json
    watch: {mode: stream, buffer: 32}`, 1)
	}
	for _, tc := range []struct{ name, argv0 string }{
		{"location.stream", "termux-location"},
		{"sensor.stream", "termux-sensor"},
	} {
		if _, err := verbs.Parse([]byte(streamVerb(tc.name, tc.argv0))); err != nil {
			t.Fatalf("%s with stream watch should parse: %v", tc.name, err)
		}
	}
	// toast.show must not stream.
	if _, err := verbs.Parse([]byte(streamVerb("toast.show", "termux-toast"))); err == nil ||
		!strings.Contains(err.Error(), "stream") {
		t.Fatalf("want stream-allowlist error for toast.show, got %v", err)
	}
	// location.stream on the wrong binary must not stream either.
	if _, err := verbs.Parse([]byte(streamVerb("location.stream", "termux-toast"))); err == nil ||
		!strings.Contains(err.Error(), "stream") {
		t.Fatalf("want stream-binary error for location.stream on termux-toast, got %v", err)
	}
}

func TestRejectTierANFCWriteEquals(t *testing.T) {
	for _, flag := range []string{"-w", "-w=x"} {
		y := strings.Replace(minimalYAML,
			`argv: ["termux-battery-status"]`,
			`argv: ["termux-nfc", "`+flag+`"]`, 1)
		// tier stays A: the -w flag must be rejected as mutating.
		_, err := verbs.Parse([]byte(y))
		if err == nil || !strings.Contains(err.Error(), "mutating") {
			t.Fatalf("want mutating error for termux-nfc %q, got %v", flag, err)
		}
	}
}

func TestDaemonMaxRetriesExplicitZero(t *testing.T) {
	y := strings.Replace(minimalYAML, "  max_retries: 5", "  max_retries: 0", 1)
	cat, err := verbs.Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cat.Daemon.MaxRetries != 0 {
		t.Fatalf("explicit max_retries: 0 must stay 0, got %d", cat.Daemon.MaxRetries)
	}
}

func TestDaemonMaxRetriesOmittedDefaults(t *testing.T) {
	y := strings.Replace(minimalYAML, "  max_retries: 5\n", "", 1)
	cat, err := verbs.Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cat.Daemon.MaxRetries != 5 {
		t.Fatalf("omitted max_retries must default to 5, got %d", cat.Daemon.MaxRetries)
	}
}

func TestPerVerbRetriesExplicitZeroVsOmitted(t *testing.T) {
	withZero := strings.Replace(minimalYAML,
		`    argv: ["termux-battery-status"]`,
		`    argv: ["termux-battery-status"]
    retries: 0`, 1)
	cat, err := verbs.Parse([]byte(withZero))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v, ok := cat.Get("battery.status")
	if !ok {
		t.Fatal("missing battery.status")
	}
	if v.Retries == nil || *v.Retries != 0 {
		t.Fatalf("explicit retries: 0 must stay 0, got %+v", v.Retries)
	}
	cat2, err := verbs.Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v2, _ := cat2.Get("battery.status")
	if v2.Retries != nil {
		t.Fatalf("omitted retries must stay nil (inherit daemon), got %d", *v2.Retries)
	}
}
