package claude

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// This file is claude's permission model: the keys a claude permissions
// document takes, its mode vocabulary, and how a document is validated,
// resolved and read back. Core carries the resolved document without
// reading it; only this engine interprets it.

// Claude's modes. dontAsk and auto are claude modes too, but ctxloom names
// them by who answers — approver: none and approver: reviewer — so they
// are not a mode a document may declare.
const (
	modeDefault     = "default"
	modeAcceptEdits = "acceptEdits"
	modePlan        = "plan"
	modeBypass      = "bypass"
)

// The document's keys.
const (
	keyMode      = "mode"
	keyAfterPlan = "after_plan"
	keyAllow     = "allow"
	keyDeny      = "deny"
	keyAsk       = "ask"
)

// Permissions is claude's permission model.
func (c Claude) Permissions() engine.Declared[engine.PermissionModel] {
	return engine.Provide[engine.PermissionModel](permissionModel{})
}

type permissionModel struct{}

func (permissionModel) Keys() []string {
	return []string{keyMode, keyAfterPlan, keyAllow, keyDeny, keyAsk}
}

func (permissionModel) Postures() []string {
	return []string{modeDefault, modeAcceptEdits, modePlan, modeBypass}
}

// afterPlanModes are the modes an approved plan may continue at.
func afterPlanModes() []string { return []string{modeDefault, modeAcceptEdits} }

// replacedModes names what took over a claude mode ctxloom does not accept
// as one.
var replacedModes = map[string]string{
	"dontask": "approver: none",
	"auto":    "approver: reviewer",
}

var errNotADocument = errors.New("claude permissions")

// Validate refuses an unknown key, a mode claude's vocabulary does not
// name, an after_plan that is not a continuation of a plan, and a rule
// claude's syntax refuses.
func (m permissionModel) Validate(doc map[string]any) error {
	for k := range doc {
		if !slices.Contains(m.Keys(), k) {
			return fmt.Errorf("%w: no key %q (known: %s)", errNotADocument, k, strings.Join(m.Keys(), ", "))
		}
	}
	mode, err := m.mode(doc)
	if err != nil {
		return err
	}
	if err := checkAfterPlan(doc, mode); err != nil {
		return err
	}
	for _, key := range []string{keyAllow, keyDeny, keyAsk} {
		if _, err := rulesOf(doc, key); err != nil {
			return err
		}
	}
	return nil
}

// mode reads the document's mode, "" when it declares none.
func (m permissionModel) mode(doc map[string]any) (string, error) {
	raw, ok := doc[keyMode]
	if !ok {
		return "", nil
	}
	s, isString := raw.(string)
	if !isString {
		return "", fmt.Errorf("%w: mode %v is not a mode name", errNotADocument, raw)
	}
	return m.parseMode(s)
}

// parseMode maps a spelling to claude's mode, naming the replacement of a
// mode ctxloom now spells as an approver.
func (m permissionModel) parseMode(s string) (string, error) {
	for _, p := range m.Postures() {
		if strings.EqualFold(strings.TrimSpace(s), p) {
			return p, nil
		}
	}
	if repl, ok := replacedModes[strings.ToLower(strings.TrimSpace(s))]; ok {
		return "", fmt.Errorf("%w: mode %q is declared as %s", errNotADocument, s, repl)
	}
	return "", fmt.Errorf("%w: mode %q is not a claude mode (known: %s)", errNotADocument, s, strings.Join(m.Postures(), "|"))
}

// checkAfterPlan refuses an after_plan that is not a plan's continuation,
// or that sits beside a mode other than plan.
func checkAfterPlan(doc map[string]any, mode string) error {
	raw, ok := doc[keyAfterPlan]
	if !ok {
		return nil
	}
	s, _ := raw.(string)
	if !slices.Contains(afterPlanModes(), s) {
		return fmt.Errorf("%w: after_plan %v is not a mode an approved plan may continue at (known: %s)", errNotADocument, raw, strings.Join(afterPlanModes(), "|"))
	}
	if mode != "" && mode != modePlan {
		return fmt.Errorf("%w: after_plan continues a plan, but the mode is %s — declare mode: plan beside it", errNotADocument, mode)
	}
	return nil
}

// rulesOf reads a rule list, each rule checked against claude's syntax.
func rulesOf(doc map[string]any, key string) ([]string, error) {
	raw, ok := doc[key]
	if !ok {
		return nil, nil
	}
	var out []string
	switch t := raw.(type) {
	case []string:
		out = t
	case []any:
		for _, e := range t {
			s, isString := e.(string)
			if !isString {
				return nil, fmt.Errorf("%w: %s holds %v, not a rule", errNotADocument, key, e)
			}
			out = append(out, s)
		}
	default:
		return nil, fmt.Errorf("%w: %s is a list of rules, not %v", errNotADocument, key, raw)
	}
	for _, r := range out {
		if _, err := parseRule(r); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", errNotADocument, key, err)
		}
	}
	return out, nil
}

// Resolve takes each key from the nearest declaration that has it, the
// flag's mode over all of them, acceptEdits when no mode is declared (the
// host default: edits unasked, the rest to the approver). An after_plan
// survives only beside a resolved plan.
func (m permissionModel) Resolve(req engine.PostureRequest) (map[string]any, error) {
	doc, err := m.resolve(req)
	if err == nil {
		return doc, nil
	}
	if !req.Degraded {
		return nil, err
	}
	if req.Warn != nil {
		req.Warn("--degraded: %v, so this run drops to the %s floor", err, modePlan)
	}
	return m.Floor(), nil
}

func (m permissionModel) resolve(req engine.PostureRequest) (map[string]any, error) {
	for _, d := range req.Declared {
		if err := m.Validate(d.Document); err != nil {
			return nil, fmt.Errorf("%w (from %s)", err, d.From)
		}
	}
	out := map[string]any{}
	for _, key := range m.Keys() {
		for _, d := range req.Declared {
			if v, ok := d.Document[key]; ok {
				out[key] = normalise(key, v)
				break
			}
		}
	}
	if req.Mode != "" {
		mode, err := m.parseMode(req.Mode)
		if err != nil {
			return nil, fmt.Errorf("%w (from the --permissions flag)", err)
		}
		out[keyMode] = mode
	}
	if _, ok := out[keyMode]; !ok {
		out[keyMode] = modeAcceptEdits
	}
	if out[keyMode] != modePlan {
		delete(out, keyAfterPlan)
	}
	return out, nil
}

// normalise stores a validated value in the resolved document's form:
// modes canonical, rules as []string.
func normalise(key string, v any) any {
	switch key {
	case keyAllow, keyDeny, keyAsk:
		rules, _ := rulesOf(map[string]any{key: v}, key)
		return rules
	case keyMode:
		mode, _ := permissionModel{}.parseMode(v.(string))
		return mode
	}
	return v
}

// Floor is plan: read-only is the one answer to "what was asked cannot be
// honoured" that cannot widen what was typed.
func (permissionModel) Floor() map[string]any { return map[string]any{keyMode: modePlan} }

// Decode reads a resolved document and names its mode.
func (m permissionModel) Decode(doc map[string]any) (string, error) {
	if err := m.Validate(doc); err != nil {
		return "", err
	}
	mode, _ := m.mode(doc)
	if mode == "" {
		return "", fmt.Errorf("%w: a resolved document names its mode", errNotADocument)
	}
	return mode, nil
}

// Transitions: an approval may move any session but a bypass one to
// default or acceptEdits (a plan's continuation, the setMode suggestion
// claude makes on an edit).
func (m permissionModel) Transitions(doc map[string]any) []string {
	if mode, _ := m.mode(doc); mode == modeBypass {
		return nil
	}
	return afterPlanModes()
}

// Sandboxes are the values claude can be made to enforce, failing closed:
// its sandbox (bubblewrap on Linux, Seatbelt on macOS) covers workspace
// writes on the host; read-only needs a filesystem deny over the working
// tree nobody has verified, native Windows has no sandbox, and an engine
// sandbox inside a container cell is unverified — so those are full only.
func (permissionModel) Sandboxes(runtimeAxis string) []engine.Sandbox {
	if runtimeAxis == "host" && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
		return []engine.Sandbox{engine.SandboxWorkspaceWrite, engine.SandboxFull}
	}
	return []engine.Sandbox{engine.SandboxFull}
}

// DefaultSandbox is full: an undeclared sandbox is today's behaviour.
func (permissionModel) DefaultSandbox() engine.Sandbox { return engine.SandboxFull }

// Reviewer: claude's auto mode hands each call to its classifier.
func (permissionModel) Reviewer() bool { return true }
