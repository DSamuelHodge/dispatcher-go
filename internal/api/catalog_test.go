package api

import (
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func TestListVerbsDetailAndSearch(t *testing.T) {
	cat, err := verbs.Parse([]byte(`
version: 1
daemon:
  listen: "127.0.0.1:8477"
  task_timeout_s: 30
  max_retries: 5
  backoff_base_s: 1
  cb_trip_threshold: 5
  cb_open_s: 60
  max_queue_depth: 100
  stream_buffer_default: 8
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
    parser: exit
    watch: false
`))
	if err != nil {
		t.Fatal(err)
	}
	names, err := listVerbsDetail(cat, "names")
	if err != nil {
		t.Fatal(err)
	}
	m := names.(map[string]any)
	if m["count"] != 2 {
		t.Fatalf("%v", m)
	}
	sum, err := listVerbsDetail(cat, "summary")
	if err != nil {
		t.Fatal(err)
	}
	sm := sum.(map[string]any)
	verbs := sm["verbs"].([]map[string]any)
	if verbs[0]["name"] != "battery.status" {
		t.Fatalf("%v", verbs[0])
	}
	if _, ok := verbs[0]["argv"]; ok {
		t.Fatal("summary must not include argv")
	}
	full, err := listVerbsDetail(cat, "full")
	if err != nil {
		t.Fatal(err)
	}
	fv := full.(map[string]any)["verbs"].([]map[string]any)
	if _, ok := fv[1]["args"]; !ok {
		t.Fatalf("full sms missing args: %v", fv[1])
	}
	hits := searchVerbs(cat, "sms", 5)
	hs := hits["hits"].([]verbSearchHit)
	if len(hs) < 1 || hs[0].Name != "sms.send" {
		t.Fatalf("%v", hits)
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("abcdef", 4); got != "a..." {
		t.Fatalf("%q", got)
	}
	if got := truncateStr("ab", 10); got != "ab" {
		t.Fatalf("%q", got)
	}
}
