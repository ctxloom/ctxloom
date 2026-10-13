package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// opencode's permission model. A posture is one of opencode's own modes and
// an optional opencode `permission` object (tool → ask|allow|deny, or tool →
// {pattern: action}); the resolved document reaches opencode per turn as
// OPENCODE_PERMISSION, which it applies after every config source, so no
// repository's config can loosen it. `run` rejects whatever is left to ask,
// unless the posture is auto (--auto approves it once). The container is the
// only sandbox: opencode has none of its own.
const (
	keyMode       = "mode"
	keyPermission = "permission"

	modeBuild = "build" // opencode's default: its rules as declared
	modePlan  = "plan"  // edits and shell denied, whatever the rules say
	modeAuto  = "auto"  // --auto: what the rules leave to ask is approved
)

var errPermissions = errors.New("opencode permissions")

// actions are opencode's permission actions.
var actions = []string{"ask", "allow", "deny"}

// Permissions is opencode's permission model.
func (Opencode) Permissions() engine.Declared[engine.PermissionModel] {
	return engine.Provide[engine.PermissionModel](permissionModel{})
}

type permissionModel struct{}

func (permissionModel) Keys() []string     { return []string{keyMode, keyPermission} }
func (permissionModel) Postures() []string { return []string{modeBuild, modePlan, modeAuto} }

// Label is opencode's own name for a posture.
func (m permissionModel) Label(posture string) string {
	if slices.Contains(m.Postures(), posture) {
		return posture
	}
	return ""
}

// Validate refuses an unknown key, a mode outside the vocabulary, and a
// permission object opencode would not take.
func (m permissionModel) Validate(doc map[string]any) error {
	for k, v := range doc {
		switch k {
		case keyMode:
			if s, _ := v.(string); !slices.Contains(m.Postures(), s) {
				return fmt.Errorf("%w: mode %v (known: %s)", errPermissions, v, strings.Join(m.Postures(), "|"))
			}
		case keyPermission:
			if err := validRules(v); err != nil {
				return fmt.Errorf("%w: permission: %v", errPermissions, err)
			}
		default:
			return fmt.Errorf("%w: no key %q (known: %s)", errPermissions, k, strings.Join(m.Keys(), ", "))
		}
	}
	return nil
}

// validRules accepts an opencode permission object: each tool an action, or
// a map of patterns to actions.
func validRules(v any) error {
	rules, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%v is not a map of tools to actions", v)
	}
	for tool, r := range rules {
		switch r := r.(type) {
		case string:
			if !slices.Contains(actions, r) {
				return fmt.Errorf("%s: %q is not one of %s", tool, r, strings.Join(actions, "|"))
			}
		case map[string]any:
			for pattern, a := range r {
				if s, _ := a.(string); !slices.Contains(actions, s) {
					return fmt.Errorf("%s: %s: %v is not one of %s", tool, pattern, a, strings.Join(actions, "|"))
				}
			}
		default:
			return fmt.Errorf("%s: %v is neither an action nor a map of patterns to actions", tool, r)
		}
	}
	return nil
}

// Resolve takes each key from the nearest declaration, the flag's mode over
// them, build when none; --degraded drops a posture that does not resolve
// to the floor.
func (m permissionModel) Resolve(req engine.PostureRequest) (map[string]any, error) {
	return kit.ResolveDegraded(req, m.resolve, m.Floor())
}

func (m permissionModel) resolve(req engine.PostureRequest) (map[string]any, error) {
	out := map[string]any{}
	for _, d := range req.Declared {
		if err := m.Validate(d.Document); err != nil {
			return nil, fmt.Errorf("%w (from %s)", err, d.From)
		}
	}
	for _, key := range m.Keys() {
		for _, d := range req.Declared {
			if v, ok := d.Document[key]; ok {
				out[key] = v
				break
			}
		}
	}
	if req.Mode != "" {
		if err := m.Validate(map[string]any{keyMode: req.Mode}); err != nil {
			return nil, fmt.Errorf("%w (from the --permissions flag)", err)
		}
		out[keyMode] = req.Mode
	}
	if _, ok := out[keyMode]; !ok {
		out[keyMode] = modeBuild
	}
	return out, nil
}

func (permissionModel) Floor() map[string]any { return map[string]any{keyMode: modePlan} }

func (m permissionModel) Decode(doc map[string]any) (string, error) {
	if err := m.Validate(doc); err != nil {
		return "", err
	}
	s, _ := doc[keyMode].(string)
	if s == "" {
		return "", fmt.Errorf("%w: a resolved document names its mode", errPermissions)
	}
	return s, nil
}

// PlansFirst: plan plans before it acts.
func (permissionModel) PlansFirst(doc map[string]any) bool { return doc[keyMode] == modePlan }

// Transitions: a plan continues at build; build and auto move nowhere.
func (m permissionModel) Transitions(doc map[string]any) []engine.PostureTransition {
	if doc[keyMode] != modePlan {
		return nil
	}
	return []engine.PostureTransition{{Posture: modeBuild, Label: m.Label(modeBuild), Default: true}}
}

func (permissionModel) Sandboxes(string) []engine.Sandbox {
	return []engine.Sandbox{engine.SandboxFull}
}
func (permissionModel) DefaultSandbox() engine.Sandbox { return engine.SandboxFull }
func (permissionModel) Reviewer() bool                 { return false }

// defaultPolicy is the policy of a session projected from a request that
// carries none: build, nobody to approve (run rejects every ask).
func defaultPolicy() engine.PermissionPolicy {
	return engine.PermissionPolicy{
		Posture:  engine.Posture{Engine: EngineName, Document: map[string]any{keyMode: modeBuild}},
		Approver: engine.ApproverNone,
		Sandbox:  engine.SandboxFull,
	}
}

// posture is a session's resolved posture, decoded.
type posture struct {
	mode  string
	rules map[string]any
}

// postureOf decodes a session's policy; an undeclared one is build.
func postureOf(p engine.PermissionPolicy) (posture, error) {
	doc := p.Posture.Document
	if len(doc) == 0 {
		return posture{mode: modeBuild}, nil
	}
	mode, err := permissionModel{}.Decode(doc)
	if err != nil {
		return posture{}, err
	}
	rules, _ := doc[keyPermission].(map[string]any)
	return posture{mode: mode, rules: rules}, nil
}

// turnMode is the mode a turn runs at: the turn's own, else the session's.
func (p posture) turnMode(t engine.TurnPosture) string {
	if t.Mode != "" {
		return t.Mode
	}
	return p.mode
}

// planDenies are what plan denies whatever the rules say: every edit and
// every shell command.
var planDenies = map[string]any{"edit": map[string]any{"*": "deny"}, "bash": map[string]any{"*": "deny"}}

// document is the OPENCODE_PERMISSION a turn at mode ("" = the session's)
// runs with: the declared rules, with plan's denies over them.
func (p posture) document(mode string) (string, error) {
	if mode == "" {
		mode = p.mode
	}
	if !slices.Contains(permissionModel{}.Postures(), mode) {
		return "", fmt.Errorf("%w: a turn asks for mode %q (known: %s)", errPermissions, mode, strings.Join(permissionModel{}.Postures(), "|"))
	}
	rules := maps.Clone(p.rules)
	if rules == nil {
		rules = map[string]any{}
	}
	if mode == modePlan {
		maps.Copy(rules, planDenies)
	}
	b, err := json.Marshal(rules)
	return string(b), err
}
