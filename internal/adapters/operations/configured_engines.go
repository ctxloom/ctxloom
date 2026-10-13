package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// namedBackend resolves ONE named backend, refusing an unregistered name:
// every layer below reads an unregistered backend as a permitted no-op, so a
// typo would otherwise report success having delivered nothing.
func namedBackend(reg engine.Registry, backend string) ([]string, error) {
	if !EngineExists(reg, backend) {
		return nil, fmt.Errorf("unknown backend %q (supported: %s)", backend, strings.Join(EngineNames(reg), ", "))
	}
	return []string{backend}, nil
}

// ConfiguredEngines returns every backend THIS PROJECT configures, sorted and
// de-duplicated: the engines resolved from the configured agents' LLM labels.
// It is what an unqualified materialize targets.
//
// A project that configures none gets the registry's default engine (the
// one it runs on), never the whole registry; only a registry that ships no
// default yields an empty list.
func ConfiguredEngines(reg engine.Registry, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(label string) {
		if backend, _ := ResolveBackend(reg, cfg, label); backend != "" {
			seen[backend] = true
		}
	}
	for _, a := range cfg.GetConfiguredAgents() {
		add(a.LLM)
	}
	// The DEFAULTS count too: a project with no explicit agent still runs on
	// its primary (and distills on its fast) engine, and a materialize that skipped
	// them would write nothing for the most common setup of all.
	lm := cfg.GetLMConfig()
	add(lm.Defaults.Primary)
	add(lm.Defaults.Fast)
	// A project that names no engine anywhere still RUNS on one — the shipped
	// default — so it is what an unqualified materialize targets. Returning nothing
	// here would silently write nothing for the simplest possible project.
	if len(seen) == 0 {
		if def := DefaultEngineName(reg); def != "" {
			seen[def] = true
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
