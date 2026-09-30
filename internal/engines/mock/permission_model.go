package mock

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Permissions is the mock's minimal permission model: a mode (default,
// plan, bypass) and rule lists of any one-line text. It enforces nothing:
// it exists so the permission route can be driven without a real engine.
func (m Mock) Permissions() engine.Declared[engine.PermissionModel] {
	return engine.Provide[engine.PermissionModel](permissionModel{})
}

type permissionModel struct{}

var errMockPermissions = errors.New("mock permissions")

func (permissionModel) Keys() []string     { return []string{"mode", "allow", "deny", "ask"} }
func (permissionModel) Postures() []string { return []string{"default", "plan", "bypass"} }

// Validate refuses an unknown key, a mode outside the vocabulary and a
// blank or multi-line rule.
func (m permissionModel) Validate(doc map[string]any) error {
	for k, v := range doc {
		if !slices.Contains(m.Keys(), k) {
			return fmt.Errorf("%w: no key %q (known: %s)", errMockPermissions, k, strings.Join(m.Keys(), ", "))
		}
		if k == "mode" {
			if s, _ := v.(string); !slices.Contains(m.Postures(), s) {
				return fmt.Errorf("%w: mode %v (known: %s)", errMockPermissions, v, strings.Join(m.Postures(), "|"))
			}
			continue
		}
		if _, err := mockRules(v); err != nil {
			return fmt.Errorf("%w: %s: %v", errMockPermissions, k, err)
		}
	}
	return nil
}

// mockRules reads a rule list.
func mockRules(v any) ([]string, error) {
	list, ok := v.([]any)
	if s, isStrings := v.([]string); isStrings {
		list = nil
		for _, e := range s {
			list = append(list, e)
		}
		ok = true
	}
	if !ok {
		return nil, fmt.Errorf("%v is not a list of rules", v)
	}
	var out []string
	for _, e := range list {
		r, _ := e.(string)
		if err := (approvalCodec{}).ValidateRule(r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Resolve takes each key from the nearest declaration, the flag's mode
// over them, default when none; degraded drops to the floor.
func (m permissionModel) Resolve(req engine.PostureRequest) (map[string]any, error) {
	out, err := m.resolve(req)
	if err != nil && req.Degraded {
		if req.Warn != nil {
			req.Warn("--degraded: %v, so this run drops to the plan floor", err)
		}
		return m.Floor(), nil
	}
	return out, err
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
				if key != "mode" {
					v, _ = mockRules(v)
				}
				out[key] = v
				break
			}
		}
	}
	if req.Mode != "" {
		if err := m.Validate(map[string]any{"mode": req.Mode}); err != nil {
			return nil, fmt.Errorf("%w (from the --permissions flag)", err)
		}
		out["mode"] = req.Mode
	}
	if _, ok := out["mode"]; !ok {
		out["mode"] = "default"
	}
	return out, nil
}

func (permissionModel) Floor() map[string]any { return map[string]any{"mode": "plan"} }

func (m permissionModel) Decode(doc map[string]any) (string, error) {
	if err := m.Validate(doc); err != nil {
		return "", err
	}
	s, _ := doc["mode"].(string)
	if s == "" {
		return "", fmt.Errorf("%w: a resolved document names its mode", errMockPermissions)
	}
	return s, nil
}

func (permissionModel) Transitions(map[string]any) []string { return nil }
func (permissionModel) Sandboxes(string) []engine.Sandbox {
	return []engine.Sandbox{engine.SandboxFull}
}
func (permissionModel) DefaultSandbox() engine.Sandbox { return engine.SandboxFull }
func (permissionModel) Reviewer() bool                 { return false }
