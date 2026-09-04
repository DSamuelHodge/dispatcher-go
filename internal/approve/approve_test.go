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

func TestLoadPolicyTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "approval-policy.json")
	if err := os.WriteFile(p, []byte(`{"approval_mode":"  always-approve  "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := approve.LoadPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	if pol.ApprovalMode != "always-approve" {
		t.Fatalf("mode not trimmed: %q", pol.ApprovalMode)
	}
	// A padded global mode must not fall into the ask branch.
	v := verbs.Verb{Name: "toast.show", Tier: verbs.TierB, Risk: verbs.RiskLow, Approval: verbs.ApprovalInherit}
	d := config.Default()
	d.ApprovalMode = config.ApprovalAsk
	dec := approve.Resolve(v, d, approve.PolicyFile{ApprovalMode: "  always-approve\t\n"})
	if dec.NeedsPrompt || dec.Mode != approve.ModeAlwaysApprove {
		t.Fatalf("padded always-approve should passthrough: %+v", dec)
	}
}

func TestResolveTierRiskMatrix(t *testing.T) {
	d := config.Default()
	d.ApprovalMode = config.ApprovalAsk
	ask := approve.PolicyFile{}
	cases := []struct {
		name   string
		tier   verbs.Tier
		risk   verbs.Risk
		prompt bool
	}{
		{"TierA none passthrough", verbs.TierA, verbs.RiskNone, false},
		{"TierA low passthrough", verbs.TierA, verbs.RiskLow, false},
		{"TierA medium gates", verbs.TierA, verbs.RiskMedium, true},
		{"TierA high gates", verbs.TierA, verbs.RiskHigh, true},
		{"TierB none gates", verbs.TierB, verbs.RiskNone, true},
		{"TierB low gates", verbs.TierB, verbs.RiskLow, true},
		{"TierB medium gates", verbs.TierB, verbs.RiskMedium, true},
		{"TierB high gates", verbs.TierB, verbs.RiskHigh, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verbs.Verb{Name: "x.y", Tier: tc.tier, Risk: tc.risk, Approval: verbs.ApprovalInherit}
			dec := approve.Resolve(v, d, ask)
			if dec.NeedsPrompt != tc.prompt {
				t.Fatalf("tier=%q risk=%q prompt=%v, want %v (%+v)", tc.tier, tc.risk, dec.NeedsPrompt, tc.prompt, dec)
			}
		})
	}
}

func forceAskVerb() verbs.Verb {
	return verbs.Verb{
		Name:                     "sms.send",
		Tier:                     verbs.TierB,
		Risk:                     verbs.RiskHigh,
		Approval:                 verbs.ApprovalInherit,
		ForceAskEvenIfGlobalAuto: true,
	}
}

func TestResolveUnattendedBypassesForceAsk(t *testing.T) {
	v := forceAskVerb()
	d := config.Default()
	d.ApprovalMode = config.ApprovalAlwaysApprove
	dec := approve.ResolveUnattended(v, d, approve.PolicyFile{ApprovalMode: "always-approve"}, true)
	if dec.NeedsPrompt {
		t.Fatalf("unattended should not prompt: %+v", dec)
	}
	if !dec.Unattended {
		t.Fatalf("bypass must be flagged: %+v", dec)
	}
}

func TestResolveUnattendedOffPrompts(t *testing.T) {
	v := forceAskVerb()
	d := config.Default()
	d.ApprovalMode = config.ApprovalAlwaysApprove
	dec := approve.ResolveUnattended(v, d, approve.PolicyFile{ApprovalMode: "always-approve"}, false)
	if !dec.NeedsPrompt || dec.Unattended {
		t.Fatalf("dec=%+v", dec)
	}
}

func TestResolveUnattendedStillGatesUnderGlobalAsk(t *testing.T) {
	// Unattended removes ONLY the force_ask carve-out, not the gate:
	// under global ask the verb still prompts (times out to denied).
	v := forceAskVerb()
	d := config.Default()
	d.ApprovalMode = config.ApprovalAsk
	dec := approve.ResolveUnattended(v, d, approve.PolicyFile{}, true)
	if !dec.NeedsPrompt || dec.Unattended {
		t.Fatalf("dec=%+v", dec)
	}
}
