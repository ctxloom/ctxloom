package claude

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// posture is a resolved permission policy read in claude's terms: its own
// document read back, and the neutral fields claude maps.
type posture struct {
	mode, afterPlan  string
	allow, deny, ask []string
	approver         engine.Approver
	sandbox          engine.Sandbox
	network          bool
}

// ErrPosture refuses a policy claude cannot honour as written.
var ErrPosture = errors.New("claude: permission posture")

// postureOf reads a resolved policy. It refuses another engine's document,
// an approver claude has no mode for beside the declared one, and a
// sandbox claude's settings cannot express — failing closed rather than
// running wider than declared.
func postureOf(p engine.PermissionPolicy, interactive bool) (posture, error) {
	if p.Posture.Engine != EngineName {
		return posture{}, fmt.Errorf("%w: the session carries %q's posture, not claude's", ErrPosture, p.Posture.Engine)
	}
	doc := p.Posture.Document
	mode, err := permissionModel{}.Decode(doc)
	if err != nil {
		return posture{}, fmt.Errorf("%w: %v", ErrPosture, err)
	}
	out := posture{mode: mode, approver: p.Approver, sandbox: p.Sandbox, network: p.Network}
	out.afterPlan, _ = doc[keyAfterPlan].(string)
	out.allow, _ = rulesOf(doc, keyAllow)
	out.deny, _ = rulesOf(doc, keyDeny)
	out.ask, _ = rulesOf(doc, keyAsk)
	if _, err := out.claudeMode(interactive); err != nil {
		return posture{}, err
	}
	switch {
	case out.sandbox == engine.SandboxReadOnly:
		return posture{}, fmt.Errorf("%w: sandbox read-only is not one claude can enforce", ErrPosture)
	case out.sandbox == engine.SandboxWorkspaceWrite && out.network:
		// claude's sandbox names the domains it lets through; an "every
		// domain" spelling is unverified, so network: true is refused
		// rather than guessed.
		return posture{}, fmt.Errorf("%w: sandbox workspace-write with network: true — claude's sandbox lets through only named domains; declare network: false, or sandbox: full", ErrPosture)
	}
	return out, nil
}

// planFirst reports a plan posture that continues past an approved plan.
func (p posture) planFirst() bool { return p.mode == modePlan && p.afterPlan != "" }

// claudeMode is claude's own mode for the posture: the declared mode, or —
// where the approver is not the human — the claude mode that names that
// approver (none: dontAsk, deny what the rules leave open; reviewer: auto,
// claude's classifier decides), which pair with default. Headless, nobody
// sits at the engine, so what is left open is denied (--permission-prompts
// none) whatever the approver, and none pairs with any mode. Bypass asks
// nobody, so every approver pairs with it. Any other pairing has no claude
// mode, and is refused.
func (p posture) claudeMode(interactive bool) (string, error) {
	switch {
	case p.mode == modeBypass, p.approver == engine.ApproverHuman:
		return p.mode, nil
	case p.mode == modeDefault && p.approver == engine.ApproverNone:
		return "dontAsk", nil
	case p.mode == modeDefault && p.approver == engine.ApproverReviewer:
		return "auto", nil
	case p.approver == engine.ApproverNone && !interactive:
		return p.mode, nil
	}
	return "", fmt.Errorf("%w: claude has no mode for %s with approver %s — only default or bypass pair with it", ErrPosture, p.mode, p.approver)
}

// sandboxDoc is the settings' sandbox member for workspace-write: bash runs
// inside claude's sandbox, which must start or claude exits
// (failIfUnavailable), with no escape hatch out of it, no auto-allow the
// posture did not grant, and every host not listed denied rather than
// asked. Full needs none.
func (p posture) sandboxDoc() *sandboxDoc {
	if p.sandbox != engine.SandboxWorkspaceWrite {
		return nil
	}
	return &sandboxDoc{Enabled: true, FailIfUnavailable: true, Network: sandboxNetworkDoc{StrictAllowlist: true}}
}

// sandboxDoc is claude's settings sandbox member, as far as ctxloom writes
// it (claude's settings schema, verified on 2.1.285).
type sandboxDoc struct {
	Enabled                  bool              `json:"enabled"`
	FailIfUnavailable        bool              `json:"failIfUnavailable"`
	AllowUnsandboxedCommands bool              `json:"allowUnsandboxedCommands"`
	AutoAllowBashIfSandboxed bool              `json:"autoAllowBashIfSandboxed"`
	Network                  sandboxNetworkDoc `json:"network"`
}

type sandboxNetworkDoc struct {
	StrictAllowlist bool `json:"strictAllowlist"`
}

// defaultPolicy is the policy of a session projected from a request that
// carries none: claude's own default mode (the human answers what is
// asked), the human as approver, no sandbox — not the launch default, which
// only a resolved launch grants.
func defaultPolicy() engine.PermissionPolicy {
	return engine.PermissionPolicy{Posture: engine.Posture{Engine: EngineName, Document: map[string]any{keyMode: modeDefault}}, Sandbox: engine.SandboxFull}
}
