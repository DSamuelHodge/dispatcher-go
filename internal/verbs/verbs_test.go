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
  approval_mode: ask
  approval_backend: dialog
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
    risk: none
    approval: inherit
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
    risk: none
    approval: inherit
    argv: ["termux-battery-status"]
    parser: json
    watch: false`,
		`- name: location.once
    tier: A
    risk: low
    approval: inherit
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
