package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Validate is the ONE place an engine name in config data is checked
// against the engines the process was composed with: every llm entry's
// type and every isolation engine name must be a registered kind, and the
// registry's one default-distribution engine is bound as the engine an
// untyped llm entry or a missed label resolves to (DefaultEngine). An
// untyped entry declares no engine and is not checked. The refusal names
// the entry, the name it carries and the names ctxloom knows.
func (c *Config) Validate(reg engine.Registry) error {
	def, err := reg.Default()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	c.defaultEngine = string(def.Root().Name)
	known := func() string {
		names := reg.Names(nil)
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = string(n)
		}
		return strings.Join(out, ", ")
	}
	for _, label := range slices.Sorted(maps.Keys(c.lm.Configs)) {
		typ := c.lm.Configs[label].Type
		if typ == "" {
			continue
		}
		if _, ok := reg.Lookup(engine.Name(typ)); !ok {
			return fmt.Errorf("config: llm.configs.%s: unknown engine type %q; ctxloom knows: %s", label, typ, known())
		}
	}
	for _, name := range c.isolationEngines {
		if _, ok := reg.Lookup(engine.Name(name)); !ok {
			return fmt.Errorf("config: isolation.engines: unknown engine %q; ctxloom knows: %s", name, known())
		}
	}
	if err := c.validateAgentAuth(reg); err != nil {
		return err
	}
	if err := c.validatePermissions(reg); err != nil {
		return err
	}
	return c.validateMayDelegate()
}

// validateAgentAuth runs engine.CheckAuth — the one auth check, which
// `agent create/edit` and every launch also run — for each agent whose
// engine its own `llm:` names. An agent whose engine comes from its
// profiles is checked when it launches, where that engine is known.
func (c *Config) validateAgentAuth(reg engine.Registry) error {
	for _, a := range c.LoadAgents() {
		if a.Auth == "" {
			continue
		}
		name := a.LLM
		if entry, ok := c.lm.Configs[a.LLM]; ok {
			name = c.EffectiveType(entry)
		}
		kind, ok := reg.Lookup(engine.Name(name))
		if !ok {
			continue
		}
		if _, err := engine.CheckAuth(kind.Root().Name, kind.Home().Auth, a.Auth); err != nil {
			return fmt.Errorf("config: agents.%s: %w", a.Name, err)
		}
	}
	return nil
}

// DefaultEngine is the engine an untyped llm entry or a missed label
// resolves to: the registry's default, bound by Validate. Empty until a
// registry is bound.
func (c *Config) DefaultEngine() string { return c.defaultEngine }

// EffectiveType returns the engine the entry drives: its type, or the bound
// default when it names none. Every consumer of LLMConfig.Type resolves it
// through this method so the defaulting rule lives in one place.
func (c *Config) EffectiveType(entry LLMConfig) string {
	if entry.Type == "" {
		return c.defaultEngine
	}
	return entry.Type
}

// ErrPermissions refuses, at load, a permission declaration or a
// may_delegate no launch could honour.
var ErrPermissions = errors.New("config: permissions")

// validatePermissions refuses, at load, a permission declaration no launch
// could honour: a neutral value outside its vocabulary; an agent's block
// for an engine ctxloom does not know, or one that engine refuses; an llm
// label's engine keys its engine (the label's type) refuses. An engine
// without a permission model accepts no engine keys at all.
func (c *Config) validatePermissions(reg engine.Registry) error {
	if err := checkNeutral("permissions", c.permissions); err != nil {
		return err
	}
	for _, label := range slices.Sorted(maps.Keys(c.lm.Configs)) {
		entry := c.lm.Configs[label]
		at := "llm.configs." + label + ".permissions"
		if err := checkNeutral(at, entry.Permissions.NeutralPermissions); err != nil {
			return err
		}
		if err := checkEngineDoc(reg, at, c.EffectiveType(entry), entry.Permissions.Engine); err != nil {
			return err
		}
	}
	for _, a := range c.LoadAgents() {
		at := "agents." + a.Name + ".permissions"
		if err := checkNeutral(at, a.Permissions.NeutralPermissions); err != nil {
			return err
		}
		for _, name := range slices.Sorted(maps.Keys(a.Permissions.Engines)) {
			if err := checkEngineDoc(reg, at+"."+name, name, a.Permissions.Engines[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkNeutral parses each declared neutral field.
func checkNeutral(at string, n agents.NeutralPermissions) error {
	if n.Approver != "" {
		if _, ok := engine.ParseApprover(n.Approver); !ok {
			return fmt.Errorf("%w: %s: approver %q (known: %s)", ErrPermissions, at, n.Approver, strings.Join(engine.ApproverNames(), "|"))
		}
	}
	if n.Sandbox != "" {
		if _, ok := engine.ParseSandbox(n.Sandbox); !ok {
			return fmt.Errorf("%w: %s: sandbox %q (known: %s)", ErrPermissions, at, n.Sandbox, strings.Join(engine.SandboxNames(), "|"))
		}
	}
	if n.ApprovalTimeout != "" {
		if _, err := engine.ParseApprovalTimeout(n.ApprovalTimeout); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrPermissions, at, err)
		}
	}
	return nil
}

// checkEngineDoc has the named engine validate its document.
func checkEngineDoc(reg engine.Registry, at, name string, doc map[string]any) error {
	if doc == nil {
		return nil
	}
	kind, ok := reg.Lookup(engine.Name(name))
	if !ok {
		return fmt.Errorf("%w: %s: no engine %q; ctxloom knows: %s", ErrPermissions, at, name, strings.Join(engineNames(reg), ", "))
	}
	model, ok := kind.Permissions().Get()
	if !ok {
		if len(doc) == 0 {
			return nil
		}
		return fmt.Errorf("%w: %s: engine %s declares no permission keys (%s)", ErrPermissions, at, name, kind.Permissions().AbsentReason())
	}
	if err := model.Validate(doc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrPermissions, at, err)
	}
	return nil
}

// validateMayDelegate refuses a may_delegate naming no agent binding.
func (c *Config) validateMayDelegate() error {
	names := slices.Sorted(maps.Keys(c.agents))
	for _, name := range names {
		for _, role := range c.agents[name].MayDelegate {
			if !slices.Contains(names, role) {
				return fmt.Errorf("%w: agents.%s.may_delegate: %q names no agent binding (agents: %s)", ErrPermissions, name, role, strings.Join(names, ", "))
			}
		}
	}
	return nil
}

func engineNames(reg engine.Registry) []string {
	names := reg.Names(nil)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
