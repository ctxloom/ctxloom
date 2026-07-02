package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/subagents"
)

// SubagentEntry is one subagent's declared definition, the listing/show shape.
// It carries only what the user wrote — no resolution — so listing many
// subagents stays cheap.
type SubagentEntry struct {
	Name     string   `json:"name"`
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
	// Isolation is the subagent's declared per-agent isolation policy (none |
	// worktree | container), as written; empty inherits the project default.
	Isolation string `json:"isolation,omitempty"`
	// Source is "config" for a config.yaml `subagents:` entry, otherwise the
	// .ctxloom/subagents/*.yaml file path it was read from.
	Source string `json:"source,omitempty"`
}

// ListSubagents returns every locally-defined subagent (config key + directory),
// merged and sorted by name. Definitions only — no profile composition or engine
// resolution — so it is cheap over many subagents.
func ListSubagents(cfg *config.Config) []SubagentEntry {
	subs := cfg.LoadSubagents()
	out := make([]SubagentEntry, 0, len(subs))
	for _, s := range subs {
		out = append(out, SubagentEntry{
			Name:      s.Name,
			Engine:    s.Engine,
			Profiles:  s.Profiles,
			Isolation: s.Isolation,
			Source:    s.Source,
		})
	}
	return out
}

// GetSubagent returns one subagent's declared definition, or an error if no
// subagent of that name is defined locally. Definition only (no resolution).
func GetSubagent(cfg *config.Config, name string) (*SubagentEntry, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	sub, ok := cfg.Subagent(name)
	if !ok {
		return nil, fmt.Errorf("subagent %q not found", name)
	}
	return &SubagentEntry{
		Name:      sub.Name,
		Engine:    sub.Engine,
		Profiles:  sub.Profiles,
		Isolation: sub.Isolation,
		Source:    sub.Source,
	}, nil
}

// ResolvedSubagent is a subagent resolved into something run / map / weave can
// consume: its profiles composed into ONE assembled context, plus the engine
// applied as the backend (overriding the composed profiles' llm). Phase C wires
// map/weave to fan across these; Phase B provides the resolver and the entity.
type ResolvedSubagent struct {
	Name string `json:"name"`
	// Engine is the subagent's DECLARED engine (may be empty); Label is the
	// engine actually resolved after applying the override precedence.
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles"`
	// Label is the resolved LLM config label, and Backend/Model the transport it
	// maps to — the same (label → backend, model) resolution run/oneshot use.
	Label   string `json:"label"`
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	// Context is the assembled context composed from the subagent's profiles;
	// Fragments names what loaded into it.
	Context   string   `json:"context,omitempty"`
	Fragments []string `json:"fragments,omitempty"`
	// Isolation is the RESOLVED per-agent isolation policy for this member
	// (subagent's own choice → project default → ""). Empty means "none" (host,
	// today's behaviour); the fan-out maps it through isolation.Resolve to build
	// this member's client factory + workspace.
	Isolation string `json:"isolation,omitempty"`
}

// ResolveSubagent resolves the named subagent into a composed context + an
// applied engine:
//
//  1. compose the subagent's profiles[] into one assembled context, via the
//     shared multi-profile assembly path (AssembleContext with Profiles) — the
//     same profile loader run/map/weave resolve through, so local, top-level
//     remote, and bundle profiles ("<bundle>#profiles/<name>") all work, and the
//     merge mirrors profile-parent semantics (later wins / union);
//  2. apply the subagent's engine as the backend, OVERRIDING the composed
//     profiles' llm. Precedence (resolveOneshotLabel, shared with run/oneshot):
//     engineOverride (a caller-level -l/--llm) wins over the declared engine;
//     either wins over the composed profiles' llm, then the project's
//     primary/default backend ("default = the project backend"). Pass "" for no
//     override.
//
// The subagent DEFINITION is ungated config: resolution touches no trust gate
// and no baseline. (Its constituent fragments/mcp/hooks still gate downstream
// when the composed context is actually assembled/applied.)
func ResolveSubagent(ctx context.Context, cfg *config.Config, name, engineOverride string) (*ResolvedSubagent, error) {
	sub, ok := cfg.Subagent(name)
	if !ok {
		return nil, fmt.Errorf("subagent %q not found", name)
	}
	return resolveSubagentBinding(ctx, cfg, name, sub, engineOverride, nil)
}

// resolveSubagentBinding is the shared compose+engine core ResolveSubagent and
// the map/weave fan both go through, so the engine-override precedence and the
// profile composition live in exactly one place (no duplication across the
// named-subagent path and the bare-profile-sugar path).
//
//   - name is the member identifier (a real subagent name, or a bare profile
//     used as sugar); it labels the result and the no-profiles warning.
//   - sub is the binding to resolve: a configured subagent, or a synthetic
//     single-profile/empty-engine subagent the fan builds for a bare profile.
//   - engineOverride, when non-empty, REPLACES the subagent's declared engine for
//     this resolution (the map/weave -l/--llm member override wins over the
//     binding's own engine). Empty leaves the binding's engine in force.
//   - loader, when non-nil, is the bundle loader used to assemble the context (a
//     test seam / shared loader); nil falls back to the gated exposure loader.
//
// Engine precedence (resolveOneshotLabel): the effective engine (override else
// the binding's engine) wins; an empty effective engine falls back to the
// composed profiles' llm, then the project default backend.
func resolveSubagentBinding(ctx context.Context, cfg *config.Config, name string, sub subagents.Subagent, engineOverride string, loader *bundles.Loader) (*ResolvedSubagent, error) {
	if len(sub.Profiles) == 0 && name != "" {
		// A NAMED subagent with no profiles composes empty context — surface it
		// (the binding is almost certainly a mistake) but don't fail: fault
		// tolerance. The synthetic default-profile member (empty name, no
		// profiles → composes the configured defaults) is intentional, so it is
		// excluded from the warning.
		clidiag.Warn("ctxloom", "subagent %q declares no profiles; composing empty context", name)
	}

	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: sub.Profiles, Loader: loader})
	if err != nil {
		return nil, fmt.Errorf("subagent %q: compose profiles: %w", name, err)
	}

	// Effective engine: an explicit override (map/weave --llm) wins over the
	// binding's declared engine; either then beats the composed profiles' llm,
	// then the project default — the same precedence run/oneshot use.
	engine := sub.Engine
	if engineOverride != "" {
		engine = engineOverride
	}
	label := resolveOneshotLabel(cfg, engine, ctxResult.ProfileLLM)
	backend, model := ResolveBackend(cfg, label)

	// Effective isolation policy: the subagent's own choice wins, else the
	// project's top-level default (cfg.Isolation), else empty (→ none
	// downstream). Empty is byte-identical to today's host behaviour.
	isolation := sub.Isolation
	if isolation == "" {
		isolation = cfg.Isolation
	}

	return &ResolvedSubagent{
		Name:      name,
		Engine:    sub.Engine,
		Profiles:  sub.Profiles,
		Label:     label,
		Backend:   backend,
		Model:     model,
		Context:   ctxResult.Context,
		Fragments: ctxResult.FragmentsLoaded,
		Isolation: isolation,
	}, nil
}

// resolveMember resolves one map/weave member identifier into the engine +
// composed context it runs with. A name matching a configured subagent resolves
// via that subagent's {engine, profiles}; any other name is a BARE PROFILE used
// as sugar — an implicit single-profile, default-engine subagent (compose just
// that profile; its engine falls back to the profile's own llm then the project
// default, i.e. exactly the pre-Phase-C member behavior). An empty member is the
// default-profile member: it composes the configured default profiles.
//
// Both paths route through resolveSubagentBinding, so a bare profile reuses the
// subagent compose/engine logic verbatim rather than re-deriving it.
func resolveMember(ctx context.Context, cfg *config.Config, member, engineOverride string, loader *bundles.Loader) (*ResolvedSubagent, error) {
	if sub, ok := cfg.Subagent(member); ok {
		return resolveSubagentBinding(ctx, cfg, member, sub, engineOverride, loader)
	}
	// Bare-profile sugar: a synthetic single-profile, empty-engine subagent. An
	// empty member carries no profile so the composition degrades to the
	// configured defaults (matching the old RunOneshot(Profile:"") member).
	syn := subagents.Subagent{Name: member}
	if member != "" {
		syn.Profiles = []string{member}
	}
	return resolveSubagentBinding(ctx, cfg, member, syn, engineOverride, loader)
}
