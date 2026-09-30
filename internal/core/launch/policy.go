package launch

import (
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// permissionRung is one declared permissions block, and how to name it.
type permissionRung struct {
	block agents.Permissions
	from  string
}

// permissionRungs are the declared blocks, highest first: the binding, the
// label, the project. The --permissions flag sits above them all and sets
// the mode alone.
func permissionRungs(sel selection, label string, labelPerm agents.Permissions, cfg *config.Config) []permissionRung {
	return []permissionRung{
		{sel.permissions, fmt.Sprintf("agent %q", sel.agent)},
		{labelPerm, fmt.Sprintf("llm label %q", label)},
		{cfg.GetPermissions(), "the project config"},
	}
}

// first returns the first rung whose field (read by get) is declared.
func first(rungs []permissionRung, get func(agents.Permissions) string) (string, string, bool) {
	for _, r := range rungs {
		if v := strings.TrimSpace(get(r.block)); v != "" {
			return v, r.from, true
		}
	}
	return "", "", false
}

// firstRules returns the first rung that declares the rule list get reads.
func firstRules(rungs []permissionRung, get func(agents.Permissions) []string) ([]string, string) {
	for _, r := range rungs {
		if rules := get(r.block); len(rules) > 0 {
			return rules, r.from
		}
	}
	return nil, ""
}

// resolvePolicy is THE permission resolution, applied once. Each field is
// the first rung's that declares it — the flag (the mode alone), the
// binding, the label, the project — else the default: the engine's host
// posture, no rules, the human as approver, the default timeout. Anything
// declared that cannot be honoured is refused, naming the value and its
// rung; only an unparseable mode has a --degraded fallback (the floor),
// because only the mode has a posture that cannot widen what was typed.
func resolvePolicy(rep report.Reporter, src Source, rungs []permissionRung, eng engine.Engine) (engine.PermissionPolicy, error) {
	facts := eng.Root().Permissions
	mode, err := resolveMode(rep, src, rungs, facts)
	if err != nil {
		return engine.PermissionPolicy{}, err
	}
	p := engine.PermissionPolicy{Mode: mode, Ceiling: mode}
	if p.AfterPlan, err = resolveAfterPlan(rungs, mode); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if after, ok := p.AfterPlan.Get(); ok {
		p.Ceiling = after
	}
	if err := capAtParent(p, src.ParentCeiling); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if p.Approver, err = resolveApprover(rungs, eng); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if p.ApprovalTimeout, err = resolveTimeout(rungs); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if err := resolveRules(&p, rungs, eng); err != nil {
		return engine.PermissionPolicy{}, err
	}
	return p, nil
}

// resolveMode is the starting posture: the first declared, else the
// engine's host default. A declaration that does not parse is refused, or
// under --degraded dropped to PermissionFloor with a warning (never
// widened); plan collapses to default on an engine with no read-only tier.
// A Structured run keeps whatever resolved: the engine denies what its
// posture and rules leave open rather than waiting on nobody.
func resolveMode(rep report.Reporter, src Source, rungs []permissionRung, facts engine.PermissionFacts) (engine.PermissionMode, error) {
	value, from, ok := src.Permission.String(), "the --permissions flag", src.Permission != engine.PermissionNotRequested
	if !ok {
		value, from, ok = first(rungs, func(b agents.Permissions) string { return b.Mode })
	}
	if !ok {
		if facts.HostDefault == engine.PermissionNotRequested {
			return engine.PermissionDefault, nil
		}
		return facts.HostDefault.CollapsePlanIfUnenforced(facts.ReadOnlyPlan), nil
	}
	m, parsed := engine.ParsePermissionMode(value)
	if !parsed {
		known := strings.Join(engine.PermissionModeNames(), "|")
		if !src.Degraded {
			return 0, fmt.Errorf("%w: %q from %s is not a posture (known: %s)", ErrPermissionUnhonoured, value, from, known)
		}
		rep.Warnf("--degraded: permissions %q from %s is not a posture (known: %s), so this run drops to the %s floor", value, from, known, engine.PermissionFloor)
		return engine.PermissionFloor, nil
	}
	return m.CollapsePlanIfUnenforced(facts.ReadOnlyPlan), nil
}

// resolveAfterPlan is the plan-first continuation: the first declared
// after_plan, which must be default or acceptEdits and must sit on a rung
// whose own mode (if it declares one) is plan. It applies only when the
// resolved mode IS plan — a flag, or a collapse, that moved the mode off
// plan leaves nothing for it to continue from.
func resolveAfterPlan(rungs []permissionRung, mode engine.PermissionMode) (engine.Declared[engine.PermissionMode], error) {
	var none engine.Declared[engine.PermissionMode]
	for _, r := range rungs {
		v := strings.TrimSpace(r.block.AfterPlan)
		if v == "" {
			continue
		}
		after, ok := engine.ParsePermissionMode(v)
		if !ok || (after != engine.PermissionDefault && after != engine.PermissionAcceptEdits) {
			return none, fmt.Errorf("%w: after_plan %q from %s is not a posture an approved plan may continue at (known: default|acceptEdits)", ErrPermissionUnhonoured, v, r.from)
		}
		if declared, ok := engine.ParsePermissionMode(r.block.Mode); ok && declared != engine.PermissionPlan {
			return none, fmt.Errorf("%w: after_plan from %s continues a plan, but %s declares mode %s — declare mode: plan beside it", ErrPermissionUnhonoured, r.from, r.from, declared)
		}
		if mode != engine.PermissionPlan {
			return none, nil
		}
		return engine.Provide(after), nil
	}
	return none, nil
}

// capAtParent refuses a posture — the starting mode, or the one an
// approved plan continues at — wider than the launching session's ceiling.
// No parent ceiling caps nothing.
func capAtParent(p engine.PermissionPolicy, parent engine.PermissionMode) error {
	if parent == engine.PermissionNotRequested {
		return nil
	}
	if !p.Mode.Within(parent) {
		return fmt.Errorf("%w: mode %s exceeds the launching session's ceiling %s — a child launches no wider than its parent may reach", ErrPermissionUnhonoured, p.Mode, parent)
	}
	if after, ok := p.AfterPlan.Get(); ok && !after.Within(parent) {
		return fmt.Errorf("%w: after_plan %s exceeds the launching session's ceiling %s — a child launches no wider than its parent may reach", ErrPermissionUnhonoured, after, parent)
	}
	return nil
}

// resolveApprover is the first declared approver, else the human. The
// human is only an approver on an engine that can put a request to one.
func resolveApprover(rungs []permissionRung, eng engine.Engine) (engine.Approver, error) {
	a := engine.ApproverHuman
	if v, from, ok := first(rungs, func(b agents.Permissions) string { return b.Approver }); ok {
		parsed, known := engine.ParseApprover(v)
		if !known {
			return 0, fmt.Errorf("%w: approver %q from %s (known: %s)", ErrPermissionUnhonoured, v, from, strings.Join(engine.ApproverNames(), "|"))
		}
		a = parsed
	}
	approvals := eng.Approvals()
	if _, ok := approvals.Get(); !ok && a == engine.ApproverHuman {
		return 0, fmt.Errorf("%w: the approver is the human, but engine %s cannot put a request to one (%s) — declare `permissions: {approver: none}` to deny what the rules leave open", ErrPermissionUnhonoured, eng.Root().Name, approvals.AbsentReason())
	}
	return a, nil
}

// resolveTimeout is the first declared approval_timeout, else the default;
// it must be a duration above zero and at most the cap.
func resolveTimeout(rungs []permissionRung) (time.Duration, error) {
	v, from, ok := first(rungs, func(b agents.Permissions) string { return b.ApprovalTimeout })
	if !ok {
		return engine.DefaultApprovalTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%w: approval_timeout %q from %s is not a duration (e.g. 20m)", ErrPermissionUnhonoured, v, from)
	}
	if d <= 0 || d > engine.MaxApprovalTimeout {
		return 0, fmt.Errorf("%w: approval_timeout %q from %s must be above 0 and at most %dm", ErrPermissionUnhonoured, v, from, int(engine.MaxApprovalTimeout.Minutes()))
	}
	return d, nil
}

// resolveRules sets each rule list from the first rung that declares it,
// every rule validated by the engine's approval codec — which an engine
// without one cannot do, so it can carry no rules.
func resolveRules(p *engine.PermissionPolicy, rungs []permissionRung, eng engine.Engine) error {
	lists := []struct {
		name string
		get  func(agents.Permissions) []string
		set  *[]string
	}{
		{"allow", func(b agents.Permissions) []string { return b.Allow }, &p.Allow},
		{"deny", func(b agents.Permissions) []string { return b.Deny }, &p.Deny},
		{"ask", func(b agents.Permissions) []string { return b.Ask }, &p.Ask},
	}
	codec, hasCodec := eng.Approvals().Get()
	for _, l := range lists {
		rules, from := firstRules(rungs, l.get)
		if len(rules) > 0 && !hasCodec {
			return fmt.Errorf("%w: engine %s has no approval codec to validate the permission rules %s from %s", ErrPermissionUnhonoured, eng.Root().Name, strings.Join(rules, ", "), from)
		}
		for _, r := range rules {
			if err := codec.ValidateRule(r); err != nil {
				return fmt.Errorf("%w: %s rule %q from %s: %v", ErrPermissionUnhonoured, l.name, r, from, err)
			}
		}
		*l.set = append([]string(nil), rules...)
	}
	return nil
}
