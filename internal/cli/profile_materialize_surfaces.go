package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file carries `profile materialize --surface <kind>=<approach>`: the
// override, and the help that makes it usable without reading source.
//
// The vocabulary is ctxloom's own, which is the problem it has to solve. Every
// name a user types here is derived from the enums in internal/shared/agent —
// never restated — so the flag, its error text, its --help table and its shell
// completion cannot drift from each other or from the code.

// parseSurfaceOverrides turns repeated `kind=approach` pairs into the map
// MaterializeProfileRequest carries.
//
// Both halves are validated HERE rather than deep in delivery, so a typo is
// reported against the flag the user typed instead of surfacing later as a
// surface that quietly kept its default. Whether the pair is SUPPORTED by the
// chosen engine is a different question, answered by the builder's Build()
// against that engine's own table — this only rejects names that exist nowhere.
func parseSurfaceOverrides(pairs []string) (map[agent.SurfaceKind]agent.Approach, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[agent.SurfaceKind]agent.Approach, len(pairs))
	for _, p := range pairs {
		name, approach, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--surface %q is not <kind>=<approach> (e.g. --surface context=unsafe-file; kinds: %s)",
				p, strings.Join(agent.SurfaceKindNames(), ", "))
		}
		k, err := agent.ParseSurfaceKind(strings.TrimSpace(name))
		if err != nil {
			return nil, fmt.Errorf("--surface %q: %w", p, err)
		}
		a, err := agent.ParseApproach(strings.TrimSpace(approach))
		if err != nil {
			return nil, fmt.Errorf("--surface %q: %w", p, err)
		}
		if prev, dup := out[k]; dup && prev != a {
			return nil, fmt.Errorf("--surface names %s twice, as %s and %s; a surface is delivered one way",
				k, prev, a)
		}
		out[k] = a
	}
	return out, nil
}

// configuredEngines reports the engines THIS project actually uses: its default
// backend plus every engine an agent binding names, deduplicated and sorted.
//
// Plural on purpose. A project routinely runs more than one engine, and the
// whole reason this flag exists is that engines differ — claude-code delivers
// context through a hook, kiro only ever writes a file. Help that showed one
// engine's table would be answering a question the user did not ask on the two
// occasions it matters most.
func configuredEngines(cfg *config.Config) []string {
	seen := map[string]bool{}
	if d := cfg.GetDefaultLLM(); d != "" {
		seen[d] = true
	}
	for _, a := range operations.ListAgents(cfg) {
		if a.Engine != "" {
			seen[a.Engine] = true
		}
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// surfaceHelpFor renders the per-engine table of what each surface kind can be
// overridden to, marking each engine's default.
//
// It is computed at help time against the REAL backends rather than written
// down, because a table in prose is a second source that goes stale silently —
// and the one thing a reader needs from it is exactly the thing that varies.
func surfaceHelpFor(engines []string) string {
	var b strings.Builder
	b.WriteString("\nSurface overrides (--surface <kind>=<approach>, repeatable)\n\n" +
		"  This is what MATERIALIZE can write, which is not always how an engine\n" +
		"  receives context in a project ctxloom is installed in. Installed, an\n" +
		"  engine with a session hook is fed at session start, so its context\n" +
		"  reflects the profile as composed at launch. Materialize writes a target\n" +
		"  dir for a launch with ctxloom OUT of the loop, where no hook of ours\n" +
		"  fires — so it favours files an engine reads on its own.\n")
	if len(engines) == 0 {
		b.WriteString("  No engine is configured for this project yet, so there is nothing to\n" +
			"  override. Run `ctxloom config init --engine <engine>` first.\n")
		return b.String()
	}
	for _, e := range engines {
		set, err := backends.SurfacesFor(e)
		if err != nil || set == nil {
			fmt.Fprintf(&b, "\n  %s: (no surface information available)\n", e)
			continue
		}
		fmt.Fprintf(&b, "\n  %s\n", e)
		for _, k := range agent.SurfaceKindNames() {
			kind, perr := agent.ParseSurfaceKind(k)
			if perr != nil {
				continue
			}
			supported := set.SupportedApproaches(kind)
			if len(supported) == 0 {
				// An empty result is not a gap: the engine folds this kind into
				// another surface (codex carries MCP in its config). Saying so
				// beats omitting the row, which reads as an oversight.
				fmt.Fprintf(&b, "    %-9s (folded into another surface on this engine)\n", k)
				continue
			}
			def, hasDefault := set.DefaultApproach(kind)
			names := make([]string, 0, len(supported))
			for _, a := range supported {
				if hasDefault && a == def {
					names = append(names, a.String()+" (default)")
					continue
				}
				names = append(names, a.String())
			}
			fmt.Fprintf(&b, "    %-9s %s\n", k, strings.Join(names, ", "))
		}
	}
	// The example is the DEFAULT invocation, not an override, because keeping
	// the assembled context as a document needs no flag: materialize writes
	// whichever context file the engine reads natively — CLAUDE.md for claude,
	// AGENTS.md for codex.
	//
	// An earlier version of this help inferred "cannot produce a file" from an
	// engine whose context surface declares no unsafe-file approach, and told
	// codex users there was no way to keep their context as a document. codex
	// writes AGENTS.md. The lesson is narrow and worth keeping: the approach
	// table names HOW a surface is delivered, not whether a reader is left with
	// a file, so it cannot answer that question and must not be asked it.
	b.WriteString("\n  Keeping your assembled context as a document needs no override — it is\n" +
		"  what materialize already does, into each engine's own native file:\n" +
		"    ctxloom profile materialize default --target ./keep\n" +
		"\n  Use --surface only to choose a DIFFERENT delivery than the default above.\n")
	return b.String()
}

// completeSurfaceOverrides offers `kind=approach` pairs valid for the backend
// ALREADY on the command line, so completion answers for the engine being used
// rather than listing every name the enums carry.
func completeSurfaceOverrides(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	engine := materializeBackend
	if f := cmd.Flags().Lookup("backend"); f != nil && f.Changed {
		engine = f.Value.String()
	}
	set, err := backends.SurfacesFor(engine)
	if err != nil || set == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, k := range surfaceKinds() {
		for _, a := range set.SupportedApproaches(k) {
			pair := k.String() + "=" + a.String()
			if strings.HasPrefix(pair, toComplete) {
				if def, ok := set.DefaultApproach(k); ok && def == a {
					pair += "\tdefault for " + engine
				}
				out = append(out, pair)
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// surfaceKinds re-resolves the kind enum from its names, so this file holds no
// second copy of the enumeration.
func surfaceKinds() []agent.SurfaceKind {
	names := agent.SurfaceKindNames()
	out := make([]agent.SurfaceKind, 0, len(names))
	for _, n := range names {
		if k, err := agent.ParseSurfaceKind(n); err == nil {
			out = append(out, k)
		}
	}
	return out
}
