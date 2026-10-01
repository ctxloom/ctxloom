package launch

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// permissionDecls are the config's permission declarations for one launch:
// the agent binding's block, the llm label's, the project's.
type permissionDecls struct {
	agent   string
	binding agents.Permissions
	label   string
	labels  agents.LabelPermissions
	project agents.NeutralPermissions
}

func declsFor(sel selection, label string, labelPerm agents.LabelPermissions, cfg *config.Config) permissionDecls {
	return permissionDecls{agent: sel.agent, binding: sel.permissions, label: label, labels: labelPerm, project: cfg.GetPermissions()}
}

// neutralRung is one level's neutral fields, and how to name it.
type neutralRung struct {
	fields agents.NeutralPermissions
	from   string
}

// neutral are the neutral fields' rungs, nearest first.
func (d permissionDecls) neutral() []neutralRung {
	return []neutralRung{
		{d.binding.NeutralPermissions, fmt.Sprintf("agent %q", d.agent)},
		{d.labels.NeutralPermissions, fmt.Sprintf("llm label %q", d.label)},
		{d.project, "the project config"},
	}
}

// first returns the first rung's value of the field get reads.
func first(rungs []neutralRung, get func(agents.NeutralPermissions) string) (string, string, bool) {
	for _, r := range rungs {
		if v := strings.TrimSpace(get(r.fields)); v != "" {
			return v, r.from, true
		}
	}
	return "", "", false
}

// resolvePolicy is THE permission resolution, applied once. The engine
// resolves its own posture from its block on the binding and the label's
// keys (the --permissions flag over both); each neutral field is the first
// of the binding, the label and the project that declares it, else the
// engine's default. Anything declared that cannot be honoured is refused,
// naming the value and where it was declared.
func resolvePolicy(rep report.Reporter, src Source, d permissionDecls, eng engine.Engine, runtime RuntimeAxis) (engine.PermissionPolicy, error) {
	name := eng.Root().Name
	p := engine.PermissionPolicy{Posture: engine.Posture{Engine: name}}
	declared := eng.Permissions()
	model, hasModel := declared.Get()
	doc, err := resolvePosture(rep, src, d, name, declared)
	if err != nil {
		return engine.PermissionPolicy{}, err
	}
	p.Posture.Document = doc
	if hasModel {
		p.Posture = p.Posture.Named(model)
	}
	rungs := d.neutral()
	if p.Approver, err = resolveApprover(rungs, eng, model, hasModel); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if p.ApprovalTimeout, err = resolveTimeout(rungs); err != nil {
		return engine.PermissionPolicy{}, err
	}
	if p.Sandbox, err = resolveSandbox(rungs, name, runtime, model, hasModel); err != nil {
		return engine.PermissionPolicy{}, err
	}
	for _, r := range rungs {
		if r.fields.Network != nil {
			p.Network = *r.fields.Network
			break
		}
	}
	return p, nil
}

// declarations are the engine's own documents, nearest first: the
// binding's block for name, then the label's keys; hasBlock reports the
// binding's.
func (d permissionDecls) declarations(name engine.Name) (decls []engine.Declaration, hasBlock bool) {
	block, hasBlock := d.binding.Engines[string(name)]
	if hasBlock {
		decls = append(decls, engine.Declaration{Document: block, From: fmt.Sprintf("agent %q", d.agent)})
	}
	if len(d.labels.Engine) > 0 {
		decls = append(decls, engine.Declaration{Document: d.labels.Engine, From: fmt.Sprintf("llm label %q", d.label)})
	}
	return decls, hasBlock
}

// resolvePosture has the engine resolve its document. A binding that
// carries engine blocks but none for this engine is refused — or, under
// --degraded, runs at the engine's floor, announced. An engine without a
// permission model takes no declaration at all.
func resolvePosture(rep report.Reporter, src Source, d permissionDecls, name engine.Name, declared engine.Declared[engine.PermissionModel]) (map[string]any, error) {
	decls, hasBlock := d.declarations(name)
	model, ok := declared.Get()
	if !ok {
		if len(decls) > 0 || src.Permission != "" || len(d.binding.Engines) > 0 {
			return nil, fmt.Errorf("%w: engine %s takes no permission declaration (%s)", ErrPermissionUnhonoured, name, declared.AbsentReason())
		}
		return nil, nil
	}
	if !hasBlock && len(d.binding.Engines) > 0 {
		refusal := fmt.Errorf("%w: agent %q declares permissions for %s but none for %s, the engine it resolved to — add permissions.%s", ErrPermissionUnhonoured, d.agent, strings.Join(slices.Sorted(maps.Keys(d.binding.Engines)), ", "), name, name)
		if !src.Degraded {
			return nil, refusal
		}
		rep.Warnf("--degraded: %v; this run drops to %s's floor", refusal, name)
		return model.Floor(), nil
	}
	doc, err := model.Resolve(engine.PostureRequest{Declared: decls, Mode: src.Permission, Degraded: src.Degraded, Warn: rep.Warnf})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPermissionUnhonoured, err)
	}
	return doc, nil
}

// resolveApprover is the first declared approver, else the human. The
// human is only an approver on an engine that can put a request to one;
// the reviewer only on an engine that has one.
func resolveApprover(rungs []neutralRung, eng engine.Engine, model engine.PermissionModel, hasModel bool) (engine.Approver, error) {
	a := engine.ApproverHuman
	if v, from, ok := first(rungs, func(n agents.NeutralPermissions) string { return n.Approver }); ok {
		parsed, known := engine.ParseApprover(v)
		if !known {
			return 0, fmt.Errorf("%w: approver %q from %s (known: %s)", ErrPermissionUnhonoured, v, from, strings.Join(engine.ApproverNames(), "|"))
		}
		if parsed == engine.ApproverReviewer && (!hasModel || !model.Reviewer()) {
			return 0, fmt.Errorf("%w: approver reviewer from %s, but engine %s has no reviewer", ErrPermissionUnhonoured, from, eng.Root().Name)
		}
		a = parsed
	}
	approvals := eng.Approvals()
	if _, ok := approvals.Get(); !ok && a == engine.ApproverHuman {
		return 0, fmt.Errorf("%w: the approver is the human, but engine %s cannot put a request to one (%s) — declare `permissions: {approver: none}` to deny what the rules leave open", ErrPermissionUnhonoured, eng.Root().Name, approvals.AbsentReason())
	}
	return a, nil
}

// resolveTimeout is the first declared approval_timeout, else the default.
func resolveTimeout(rungs []neutralRung) (time.Duration, error) {
	v, from, ok := first(rungs, func(n agents.NeutralPermissions) string { return n.ApprovalTimeout })
	if !ok {
		return engine.DefaultApprovalTimeout, nil
	}
	d, err := engine.ParseApprovalTimeout(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %w (from %s)", ErrPermissionUnhonoured, err, from)
	}
	return d, nil
}

// resolveSandbox is the first declared sandbox, else the engine's default;
// either must be one the engine can enforce on this runtime. It fails
// closed: there is no degraded fallback, because every fallback from an
// unenforceable sandbox is a wider one.
func resolveSandbox(rungs []neutralRung, name engine.Name, runtime RuntimeAxis, model engine.PermissionModel, hasModel bool) (engine.Sandbox, error) {
	want, from := engine.SandboxFull, fmt.Sprintf("engine %s's default", name)
	enforceable := []engine.Sandbox{engine.SandboxFull}
	if hasModel {
		want = model.DefaultSandbox()
		enforceable = model.Sandboxes(string(runtime))
	}
	if v, at, ok := first(rungs, func(n agents.NeutralPermissions) string { return n.Sandbox }); ok {
		parsed, known := engine.ParseSandbox(v)
		if !known {
			return 0, fmt.Errorf("%w: sandbox %q from %s (known: %s)", ErrPermissionUnhonoured, v, at, strings.Join(engine.SandboxNames(), "|"))
		}
		want, from = parsed, at
	}
	if slices.Contains(enforceable, want) {
		return want, nil
	}
	names := make([]string, len(enforceable))
	for i, s := range enforceable {
		names[i] = s.String()
	}
	return 0, fmt.Errorf("%w: sandbox %s from %s cannot be enforced by %s on runtime %s (it can enforce: %s)", ErrPermissionUnhonoured, want, from, name, runtime, strings.Join(names, "|"))
}
