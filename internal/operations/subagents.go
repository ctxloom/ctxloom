package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// SubagentEntry is one subagent's declared definition, the listing/show shape.
// It carries only what the user wrote — no resolution — so listing many
// subagents stays cheap.
type SubagentEntry struct {
	Name     string   `json:"name"`
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
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
			Name:     s.Name,
			Engine:   s.Engine,
			Profiles: s.Profiles,
			Source:   s.Source,
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
		Name:     sub.Name,
		Engine:   sub.Engine,
		Profiles: sub.Profiles,
		Source:   sub.Source,
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
//     the declared engine wins; an empty engine falls back to the composed
//     profiles' llm, then the project's primary/default backend ("default = the
//     project backend").
//
// The subagent DEFINITION is ungated config: resolution touches no trust gate
// and no baseline. (Its constituent fragments/mcp/hooks still gate downstream
// when the composed context is actually assembled/applied.)
func ResolveSubagent(ctx context.Context, cfg *config.Config, name string) (*ResolvedSubagent, error) {
	sub, ok := cfg.Subagent(name)
	if !ok {
		return nil, fmt.Errorf("subagent %q not found", name)
	}
	if len(sub.Profiles) == 0 {
		// A subagent with no profiles composes empty context — surface it (the
		// binding is almost certainly a mistake) but don't fail: fault tolerance.
		clidiag.Warn("ctxloom", "subagent %q declares no profiles; composing empty context", name)
	}

	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: sub.Profiles})
	if err != nil {
		return nil, fmt.Errorf("subagent %q: compose profiles: %w", name, err)
	}

	// Engine override beats the composed profiles' llm; an empty engine falls
	// back to that llm, then the project default — the same precedence run uses.
	label := resolveOneshotLabel(cfg, sub.Engine, ctxResult.ProfileLLM)
	backend, model := ResolveBackend(cfg, label)

	return &ResolvedSubagent{
		Name:      name,
		Engine:    sub.Engine,
		Profiles:  sub.Profiles,
		Label:     label,
		Backend:   backend,
		Model:     model,
		Context:   ctxResult.Context,
		Fragments: ctxResult.FragmentsLoaded,
	}, nil
}
