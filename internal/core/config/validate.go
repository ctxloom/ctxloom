package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

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
	return c.validateAgentAuth(reg)
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
