package launchtest

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// FixtureModel is the fixture engine's permission model: a "mode" key over
// default|acceptEdits|plan|bypass (default undeclared, plan the floor) and a
// "deny" list, each key from the nearest declaration, the flag's mode over
// them. It enforces workspace-write only on the host, and serves a reviewer
// only when HasReviewer says so.
type FixtureModel struct{ HasReviewer bool }

// ErrFixtureMode refuses a mode outside the fixture's vocabulary.
var ErrFixtureMode = errors.New("fixture: not a mode")

func (FixtureModel) Keys() []string     { return []string{"mode", "deny"} }
func (FixtureModel) Postures() []string { return []string{"default", "acceptEdits", "plan", "bypass"} }

func (m FixtureModel) Validate(doc map[string]any) error {
	for k, v := range doc {
		if !slices.Contains(m.Keys(), k) {
			return fmt.Errorf("fixture: no key %q", k)
		}
		if s, _ := v.(string); k == "mode" && !slices.Contains(m.Postures(), s) {
			return fmt.Errorf("%w: %q (known: %s)", ErrFixtureMode, v, strings.Join(m.Postures(), "|"))
		}
	}
	return nil
}

func (m FixtureModel) Resolve(req engine.PostureRequest) (map[string]any, error) {
	out := map[string]any{}
	for _, key := range m.Keys() {
		for _, d := range req.Declared {
			if v, ok := d.Document[key]; ok {
				out[key] = v
				break
			}
		}
	}
	if req.Mode != "" {
		out["mode"] = req.Mode
	}
	if _, ok := out["mode"]; !ok {
		out["mode"] = "default"
	}
	if err := m.Validate(out); err != nil {
		err = fmt.Errorf("%w (from %s)", err, from(req))
		if !req.Degraded {
			return nil, err
		}
		if req.Warn != nil {
			req.Warn("--degraded: %v, so this run drops to the plan floor", err)
		}
		return m.Floor(), nil
	}
	return out, nil
}

func from(req engine.PostureRequest) string {
	if req.Mode != "" {
		return "the --permissions flag"
	}
	for _, d := range req.Declared {
		if _, ok := d.Document["mode"]; ok {
			return d.From
		}
	}
	return "the default"
}

func (FixtureModel) Floor() map[string]any { return map[string]any{"mode": "plan"} }
func (m FixtureModel) Decode(doc map[string]any) (string, error) {
	if err := m.Validate(doc); err != nil {
		return "", err
	}
	s, _ := doc["mode"].(string)
	return s, nil
}
func (FixtureModel) Transitions(map[string]any) []string { return []string{"default"} }
func (FixtureModel) Sandboxes(runtime string) []engine.Sandbox {
	if runtime == "host" {
		return []engine.Sandbox{engine.SandboxWorkspaceWrite, engine.SandboxFull}
	}
	return []engine.Sandbox{engine.SandboxFull}
}
func (FixtureModel) DefaultSandbox() engine.Sandbox { return engine.SandboxFull }
func (m FixtureModel) Reviewer() bool               { return m.HasReviewer }
