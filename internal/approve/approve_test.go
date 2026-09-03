package approve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/config"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func TestResolveForceAskBeatsGlobalAuto(t *testing.T) {
	v := verbs.Verb{
		Name:                     "sms.send",
		Tier:                     verbs.TierB,
		Risk:                     verbs.RiskHigh,
		Approval:                 verbs.ApprovalInherit,
		ForceAskEvenIfGlobalAuto: true,
	}
	d := config.Default()
	d.ApprovalMode = config.ApprovalAlwaysApprove
	dec := approve.Resolve(v, d, approve.PolicyFile{ApprovalMode: "always-approve"})
	if !dec.NeedsPrompt || dec.Mode != approve.ModeAsk {
		t.Fatalf("dec=%+v", dec)
	}
}

func TestResolvePolicyOverridesDaemon(t *testing.T) {
	v := verbs.Verb{Name: "toast.show", Tier: verbs.TierB, Risk: verbs.RiskLow, Approval: verbs.ApprovalInherit}
	d := config.Default()
	d.ApprovalMode = config.ApprovalAsk
	dec := approve.Resolve(v, d, approve.PolicyFile{ApprovalMode: "always-approve"})
	if dec.NeedsPrompt || dec.Mode != approve.ModeAlwaysApprove {
		t.Fatalf("dec=%+v", dec)
	}
}

func TestResolveDialogNeverGated(t *testing.T) {
	v := verbs.Verb{Name: "dialog.confirm", Tier: verbs.TierB, Risk: verbs.RiskLow, Argv: []string{"termux-dialog", "confirm"}, Approval: verbs.ApprovalAsk}
	dec := approve.Resolve(v, config.Default(), approve.PolicyFile{})
	if dec.NeedsPrompt {
		t.Fatalf("dialog should not prompt: %+v", dec)
	}
}

func TestLoadPolicy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "approval-policy.json")
	if err := os.WriteFile(p, []byte(`{"approval_mode":"always-approve"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := approve.LoadPolicy(p)
	if err != nil || pol.ApprovalMode != "always-approve" {
		t.Fatalf("%v %+v", err, pol)
	}
	_, err = approve.LoadPolicy(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRedactArgs(t *testing.T) {
	v := verbs.Verb{StdinArg: &verbs.StdinArg{Arg: "text"}}
	out := approve.RedactArgs(v, map[string]any{"text": "sekrit", "number": "555"})
	if out["text"] != approve.RedactedMarker || out["number"] != "555" {
		t.Fatalf("%v", out)
	}
}

func TestStaticPrompter(t *testing.T) {
	p := approve.StaticPrompter{Approve: false}
	r := p.Confirm(context.Background(), "t", "b", time.Second)
	if r.Approved {
		t.Fatal("expected deny")
	}
}

func TestParseConfirmViaStaticRaw(t *testing.T) {
	// covered via DialogPrompter integration with PATH shim in api tests
}
