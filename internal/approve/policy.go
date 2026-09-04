// Package approve resolves effective approval mode and runs the confirm gate.
package approve

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DSamuelHodge/dispatcher-go/internal/config"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Mode is the effective gate decision before prompting.
type Mode string

const (
	ModeAsk           Mode = "ask"
	ModeAlwaysApprove Mode = "always-approve"
)

// PolicyFile is ~/.agent/approval-policy.json (runtime override).
type PolicyFile struct {
	// ApprovalMode overrides daemon.approval_mode when set.
	ApprovalMode string `json:"approval_mode"`
}

// DefaultPolicyPath is the conventional runtime policy location.
const DefaultPolicyPath = ".agent/approval-policy.json"

// LoadPolicy reads path; missing file yields empty policy (not an error).
func LoadPolicy(path string) (PolicyFile, error) {
	var p PolicyFile
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, fmt.Errorf("read approval policy: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parse approval policy: %w", err)
	}
	// Normalize surrounding whitespace so " always-approve " behaves like
	// "always-approve" instead of falling through to the ask branch.
	p.ApprovalMode = strings.TrimSpace(p.ApprovalMode)
	switch p.ApprovalMode {
	case "", string(ModeAsk), string(ModeAlwaysApprove):
	default:
		return p, fmt.Errorf("approval_policy.approval_mode must be ask|always-approve, got %q", p.ApprovalMode)
	}
	return p, nil
}

// Decision is the resolved gate plan for one verb invocation.
type Decision struct {
	Mode Mode
	// NeedsPrompt is true when termux-dialog (or backend) must run.
	NeedsPrompt bool
	// By labels audit approved{by}: "user" | "policy"
	By string
	// Reason is a short debug string (not secret).
	Reason string
	// Unattended is true only when the -unattended override bypassed a
	// force_ask gate (see ResolveUnattended). Audited on approval.
	Unattended bool
}

// ResolveUnattended behaves like Resolve, except when unattended is true
// AND the effective global mode (policy file, else daemon) is
// always-approve: any gate that would otherwise prompt — force_ask or
// per-verb ask — is overridden to approve. This is the remote-agent
// full-autonomy escape hatch (-unattended flag): it makes an explicit
// global always-approve absolute. Without global always-approve,
// unattended changes nothing (gates still prompt and time out to denied
// without a human). Decision.Unattended is true exactly when a prompt
// was bypassed, so callers can audit it loudly.
func ResolveUnattended(v verbs.Verb, daemon config.Daemon, policy PolicyFile, unattended bool) Decision {
	plain := Resolve(v, daemon, policy)
	if !unattended || !plain.NeedsPrompt {
		return plain
	}
	global := strings.TrimSpace(string(daemon.ApprovalMode))
	if strings.TrimSpace(policy.ApprovalMode) != "" {
		global = strings.TrimSpace(policy.ApprovalMode)
	}
	if Mode(global) != ModeAlwaysApprove {
		return plain
	}
	return Decision{
		Mode: ModeAlwaysApprove, NeedsPrompt: false, By: "policy",
		Reason:     "global always-approve (unattended override of ask/force_ask gates)",
		Unattended: true,
	}
}

// Resolve applies FR-4.1 order:
// force_ask > per-verb ask|always-approve > policy file > daemon.approval_mode.
// dialog.* verbs are never gated (approval primitive).
func Resolve(v verbs.Verb, daemon config.Daemon, policy PolicyFile) Decision {
	if isDialogVerb(v) {
		return Decision{Mode: ModeAlwaysApprove, NeedsPrompt: false, By: "policy", Reason: "dialog verb never gated"}
	}
	if v.ForceAskEvenIfGlobalAuto {
		return Decision{Mode: ModeAsk, NeedsPrompt: true, By: "user", Reason: "force_ask_even_if_global_auto"}
	}
	switch v.Approval {
	case verbs.ApprovalAsk:
		return Decision{Mode: ModeAsk, NeedsPrompt: true, By: "user", Reason: "per-verb ask"}
	case verbs.ApprovalAlwaysApprove:
		return Decision{Mode: ModeAlwaysApprove, NeedsPrompt: false, By: "policy", Reason: "per-verb always-approve"}
	}

	// inherit → policy file then daemon (both trimmed: callers may build
	// PolicyFile by hand, and config values may carry stray whitespace).
	global := strings.TrimSpace(string(daemon.ApprovalMode))
	if strings.TrimSpace(policy.ApprovalMode) != "" {
		global = strings.TrimSpace(policy.ApprovalMode)
	}
	switch Mode(global) {
	case ModeAlwaysApprove:
		return Decision{Mode: ModeAlwaysApprove, NeedsPrompt: false, By: "policy", Reason: "global always-approve"}
	default:
		// Tier A with risk none/low often still inherits ask — still needs prompt only when ModeAsk.
		// Spec: gated Tier B; for inherit+ask we still prompt for any non-dialog verb when mode is ask.
		// Practical MVP: ask only when risk is medium|high OR tier B. Tier A risk none with ask would be noisy.
		// Spec FR-4 says ask mode for gated verbs; seed uses inherit for Tier A. Interpret:
		// needs prompt when effective mode is ask AND (tier B OR risk medium/high OR force already handled).
		//
		// DECISION (pinned by test, do not change casually): under global ask,
		// Tier A verbs with risk none/low pass through with zero prompt
		// ("tier A low-risk passthrough under ask"). Rationale: prompting for
		// every read-only/low-risk Tier A verb would make ask mode unusably
		// noisy, and Tier A verbs are read-only by construction. Tier A with
		// risk medium/high, and ALL Tier B verbs, still gate. If this
		// passthrough ever needs tightening, change `need` below and update
		// TestResolveTierRiskMatrix accordingly.
		need := v.Tier == verbs.TierB || v.Risk == verbs.RiskMedium || v.Risk == verbs.RiskHigh
		if !need {
			return Decision{Mode: ModeAlwaysApprove, NeedsPrompt: false, By: "policy", Reason: "tier A low-risk passthrough under ask"}
		}
		return Decision{Mode: ModeAsk, NeedsPrompt: true, By: "user", Reason: "global ask"}
	}
}

func isDialogVerb(v verbs.Verb) bool {
	if strings.HasPrefix(v.Name, "dialog.") {
		return true
	}
	if len(v.Argv) > 0 && v.Argv[0] == "termux-dialog" {
		return true
	}
	return false
}
