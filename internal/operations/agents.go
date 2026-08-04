package operations

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// AgentEntry is one agent's declared definition, the listing/show shape.
// It carries only what the user wrote — no resolution — so listing many
// agents stays cheap.
type AgentEntry struct {
	Name     string   `json:"name"`
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
	// Runtime is the agent's declared runtime axis (host | container), as
	// written; empty inherits the project `runtime:` default.
	Runtime string `json:"runtime,omitempty"`
	// Permissions is the agent's declared permission posture
	// (default|acceptEdits|plan|bypass), as written; empty inherits the engine
	// label's default and finally the built-in default.
	Permissions string `json:"permissions,omitempty"`
	// Coordinator is the agent's declared Coordinator flag (trust-boundary
	// gate): whether this agent, when run as a delegated child, is trusted
	// with the coordinator-only MCP tools (agent_run/roster/agent_stop/
	// agent_fetch_artifact). Default false = leaf.
	Coordinator bool `json:"coordinator,omitempty"`
	// Driving is the agent's declared per-turn execution axis
	// (conversational|oneshot), as written; empty means conversational (the
	// default — see agents.Agent.Driving).
	Driving agents.DrivingMode `json:"driving,omitempty"`
	// Escalation is the agent's declared approval-policy ladder, as written
	// (previously invisible here — settable only by hand-editing YAML and
	// undetectable from `agent list`/`agent show`). Empty means the ladder is
	// derived from Permissions at resolve time (see agents.Agent.Escalation's
	// doc).
	Escalation []agents.EscalationRung `json:"escalation,omitempty"`
	// Source is "config" for a config.yaml `agents:` entry, otherwise the
	// .ctxloom/agents/*.yaml file path it was read from.
	Source string `json:"source,omitempty"`
}

// ListAgents returns every locally-defined agent (config key + directory),
// merged and sorted by name. Definitions only — no profile composition or engine
// resolution — so it is cheap over many agents.
func ListAgents(cfg *config.Config) []AgentEntry {
	subs := cfg.LoadAgents()
	out := make([]AgentEntry, 0, len(subs))
	for _, s := range subs {
		out = append(out, AgentEntry{
			Name:        s.Name,
			Engine:      s.Engine,
			Profiles:    s.Profiles,
			Runtime:     s.Runtime,
			Permissions: s.Permissions,
			Coordinator: s.Coordinator,
			Driving:     s.Driving,
			Escalation:  s.Escalation,
			Source:      s.Source,
		})
	}
	return out
}

// GetAgent returns one agent's declared definition, or an error if no
// agent of that name is defined locally. Definition only (no resolution).
func GetAgent(cfg *config.Config, name string) (*AgentEntry, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	sub, ok := cfg.Agent(name)
	if !ok {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return &AgentEntry{
		Name:        sub.Name,
		Engine:      sub.Engine,
		Profiles:    sub.Profiles,
		Runtime:     sub.Runtime,
		Permissions: sub.Permissions,
		Coordinator: sub.Coordinator,
		Driving:     sub.Driving,
		Escalation:  sub.Escalation,
		Source:      sub.Source,
	}, nil
}

// SetAgentRequest is the input for SetAgent: the binding to add or update
// under the local `agents:` config key. Engine is optional (empty = project
// default / the composed profiles' llm); Profiles compose into one context;
// Runtime is optional (host | container; empty inherits the project
// `runtime:` default); Permissions is optional (default|acceptEdits|plan|bypass;
// empty inherits the engine label's default). The workspace axis is deliberately
// NOT settable here — it is a session trait chosen at invocation time, never
// stored on a binding.
type SetAgentRequest struct {
	Name string `json:"name"`
	// Engine is the LLM engine/label to bind. A non-empty value is REJECTED
	// unless it names something operations.AvailableLLMNames knows (a
	// registered backend or a config-declared label) — see checkAgentWrite.
	// Empty CLEARS the override, falling back to the profiles' llm and then
	// the project default.
	Engine      *string   `json:"engine,omitempty"`
	Profiles    *[]string `json:"profiles,omitempty"`
	Runtime     *string   `json:"runtime,omitempty"`
	Permissions *string   `json:"permissions,omitempty"`
	// Coordinator sets the trust-boundary gate flag: whether this agent, when
	// run as a delegated child, is trusted with the coordinator-only MCP
	// tools (agent_run/roster/agent_stop/agent_fetch_artifact). Default false
	// = leaf; set true only for an agent that itself spawns/manages children.
	Coordinator *bool `json:"coordinator,omitempty"`
	// Driving sets the per-turn execution axis (conversational|oneshot);
	// empty = conversational (the default, see agents.Agent.Driving). Unlike
	// Runtime/Permissions below, an unknown value here is REJECTED (SetAgent
	// returns an error, nothing is persisted) rather than warned-and-stored —
	// see agents.ValidateDriving's doc for why.
	Driving *string `json:"driving,omitempty"`
}

// orKeep dereferences an optional request field: nil means "the caller did not
// name this field", so the existing value is kept.
func orKeep[T any](set *T, existing T) T {
	if set == nil {
		return existing
	}
	return *set
}

// A binding write's axes split two ways, and which side an axis falls on is a
// judgement about what an unknown value DOES. warnAgentAxisTypos holds the
// advisory half, validateAgentAxes the refusing half; SetAgent runs both before
// it opens its write transaction.
//
// warnAgentAxisTypos covers the axes whose unknown values still resolve to a
// working default at run time (Runtime → host, Permissions → the default
// posture), so they are stored as written per fault tolerance — but warned about
// NOW, so a typo surfaces at write time rather than at the first run. The
// shadowed-definition notice rides along: it is likewise about where a
// definition lives, not about whether the binding works.
func warnAgentAxisTypos(cfg *config.Config, name string, req SetAgentRequest) {
	// A directory-sourced agent of the same name would be shadowed by this
	// config-key entry (config wins, see config.LoadAgents). Surface it so the
	// user knows the file is now inert rather than silently double-defining.
	if existing, ok := cfg.Agent(name); ok && !existing.FromConfig() {
		clidiag.Warn("ctxloom",
			"agent %q is also defined in %s; the config.yaml entry written now takes precedence",
			name, existing.Source)
	}

	if req.Runtime != nil && *req.Runtime != "" && !slices.Contains(isolation.RuntimeNames(), *req.Runtime) {
		clidiag.Warn("ctxloom",
			"agent %q declares unknown runtime %q (known: %s); it will run on the host",
			name, *req.Runtime, strings.Join(isolation.RuntimeNames(), "|"))
	}

	if req.Permissions != nil && *req.Permissions != "" {
		if _, ok := agent.ParsePermissionMode(*req.Permissions); !ok {
			clidiag.Warn("ctxloom",
				"agent %q declares unknown permissions %q (known: %s); it will use the default posture",
				name, *req.Permissions, strings.Join(agent.PermissionModeNames(), "|"))
		}
	}
}

// validateAgentAxes covers the axes an unknown value BREAKS rather than
// degrades, so a non-nil return means nothing may be written:
//
//   - Engine: a name nothing defines leaves the binding broken. `agent edit dev
//     --engine <typo>` used to exit 0 and print "Updated agent" over exactly
//     that; the failure then surfaced later, somewhere else, as whatever a
//     missing engine happens to look like downstream — the silent-no-op shape
//     this codebase treats as a bug rather than a shortcut.
//   - Driving: it changes execution semantics (whether the child's engine
//     process survives a turn boundary). Reasoning in agents.ValidateDriving.
//
// The engine membership set is AvailableLLMNames — registered backends UNION
// the labels this config declares — because an agent's engine is a LABEL
// resolved through resolveOneshotLabel/ResolveBackend, not a backend name:
// `agent create finder --engine claude-fast` is one of this command's own help
// examples. That is the SAME set `llm default` accepts and the same one it
// offers on rejection, so this message can never list a name it would refuse.
// Checking backends.Exists alone (init's guard, where a freshly scaffolded
// config.yaml genuinely has no labels yet) would reject the documented
// invocation. An explicitly empty engine stays legal: it CLEARS the override,
// falling back to the composed profiles' llm and then the project default.
func validateAgentAxes(cfg *config.Config, name string, req SetAgentRequest) error {
	if req.Engine != nil && *req.Engine != "" {
		if available := AvailableLLMNames(cfg); !slices.Contains(available, *req.Engine) {
			return fmt.Errorf("agent %q: unknown engine %q; valid engines: %s",
				name, *req.Engine, strings.Join(available, ", "))
		}
	}

	if req.Driving != nil {
		if err := agents.ValidateDriving(agents.DrivingMode(*req.Driving)); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	return nil
}

// SetAgent adds or updates a LOCAL agent under the `agents:` config key,
// inside one Manager.Update transaction — the write is a single locked,
// freshly-reloaded read-modify-write rather than a Load, a mutation, and a
// later Save that could race a concurrent writer. It is the write half the
// agent-assisted setup calls to record the engine↔profile binding the user
// chose.
//
// cfg is used only for the advisory reads below (the same-name shadow
// warning, and resolving remote aliases for canonicalize-on-store); it is
// never mutated. The bind itself is a per-FIELD update applied to the
// transaction's fresh Draft: a nil request field means "the caller did not
// name this field" and keeps whatever the existing binding holds, while an
// explicitly-supplied empty value clears it. It used to be a whole-binding
// REPLACE, which meant `ctxloom agent set dev --runtime container` silently
// destroyed dev's engine, profiles, permission posture, coordinator flag and
// — worst, because the request type cannot even express it — its approval
// escalation ladder. Merging inside Update also keeps the read-modify-write
// under the same lock, so a concurrent writer cannot land
// between the read of the existing record and the write of the merged one.
//
// Name-agnostic by construction: it stores whatever name/profiles the caller
// passes — the role taxonomy (developer/finder/code-review, per-language ×
// lens) is the user's choice from the live scan, never enumerated here. Engine
// is the one exception: it is checked for MEMBERSHIP (see validateAgentAxes)
// against the engines this config actually exposes, because an engine nothing
// defines leaves the binding broken. Which backend it maps to, and the
// override precedence, still resolve later in ResolveAgent.
func SetAgent(mgr *config.Manager, cfg *config.Config, req SetAgentRequest) (*AgentEntry, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}
	name := req.Name
	if name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	// Pre-flight, both halves BEFORE the Manager.Update transaction opens, so
	// a refusal writes nothing and a typo'd `agent edit` cannot half-apply over
	// a live binding.
	warnAgentAxisTypos(cfg, name, req)
	if err := validateAgentAxes(cfg, name, req); err != nil {
		return nil, err
	}

	// Canonicalize-on-store (decision B): a per-remote short profile ref
	// ("<remote>/<bundle>#profiles/<name>") is expanded to its canonical URL so the
	// binding persists a stable identity, not a machine-local alias that a later
	// remote rename would strand. Bare/local names stay verbatim (decision A). This
	// replaces the old verbatim store.
	var entry agents.Agent
	err := mgr.Update(func(d *config.Draft) error {
		if d.Agents == nil {
			d.Agents = make(map[string]agents.Agent)
		}
		// Start from the record as it stands RIGHT NOW inside the transaction
		// (not from cfg, which was loaded before the lock), so every field the
		// request does not name — including Escalation, which SetAgentRequest
		// has no way to carry — survives untouched.
		entry = d.Agents[name]
		if req.Profiles != nil {
			entry.Profiles = canonicalizeProfileRefs(*req.Profiles, aliasToURLResolver(cfg))
		}
		entry.Engine = orKeep(req.Engine, entry.Engine)
		entry.Runtime = orKeep(req.Runtime, entry.Runtime)
		entry.Permissions = orKeep(req.Permissions, entry.Permissions)
		entry.Coordinator = orKeep(req.Coordinator, entry.Coordinator)
		if req.Driving != nil {
			entry.Driving = agents.DrivingMode(*req.Driving)
		}
		d.Agents[name] = entry
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("save agent %q: %w", name, err)
	}
	return &AgentEntry{
		Name:        name,
		Engine:      entry.Engine,
		Profiles:    entry.Profiles,
		Runtime:     entry.Runtime,
		Permissions: entry.Permissions,
		Coordinator: entry.Coordinator,
		Driving:     entry.Driving,
		Escalation:  entry.Escalation,
		Source:      agents.SourceConfig,
	}, nil
}

// RemoveAgent deletes a LOCAL agent from the `agents:` config key, inside one
// Manager.Update transaction: the existence check and the delete happen
// against the same locked, freshly-reloaded Draft, so a concurrent writer can
// never resurrect the entry between the check and the save. Only config-key
// agents are removable here: a directory-sourced agent
// (.ctxloom/agents/<name>.yaml) is its own file and is reported as such
// rather than silently left in place (the caller deletes the file). An
// unknown name errors.
//
// cfg is consulted only to distinguish "defined as a file" from "not defined
// at all" for the error message; it is never mutated.
func RemoveAgent(mgr *config.Manager, cfg *config.Config, name string) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if mgr == nil {
		return fmt.Errorf("manager is required")
	}
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	return mgr.Update(func(d *config.Draft) error {
		if _, ok := d.Agents[name]; !ok {
			// Distinguish "defined as a file" from "not defined at all" for a clear
			// message — the config-key write path cannot delete a file.
			if existing, found := cfg.Agent(name); found && !existing.FromConfig() {
				return fmt.Errorf("agent %q is defined in %s, not config.yaml; delete that file to remove it", name, existing.Source)
			}
			return fmt.Errorf("agent %q not found in config.yaml", name)
		}
		delete(d.Agents, name)
		return nil
	})
}

// AgentSetupNudge returns a one-line, user-facing nudge toward
// `ctxloom init prompt` when the project HAS profiles but NO agents
// configured, or "" otherwise. It is the detection signal Phase F wires into the
// SessionStart message path: once the user has profiles worth orchestrating but
// hasn't bound any engine↔profile agents, ctxloom prompts them to set them up
// WITH the agent. The moment any agent exists, the nudge goes silent.
//
// Fault-tolerant and name-agnostic: it inspects only counts (profiles present?
// agents present?), never any role/lens/engine names, and never blocks.
func AgentSetupNudge(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if len(cfg.LoadAgents()) > 0 {
		return "" // already orchestrating — nothing to nudge
	}
	if !hasAnyProfiles(cfg) {
		return "" // nothing to bind engines to yet
	}
	return "ctxloom: this project has profiles but no agents configured. " +
		"Run `ctxloom init prompt` (or ask your agent to) to bind engines to profiles — " +
		"the standard trio is a coordinator you drive, a containerized developer, and a cheap finder, " +
		"plus code-review lenses and any other roles you want to orchestrate."
}

// hasAnyProfiles reports whether the project has any profile to bind an agent
// to: a configured default, an inline definition, or a directory profile. Used
// only to gate the setup nudge, so a directory-scan failure degrades to "none"
// (the nudge simply stays quiet) rather than erroring.
func hasAnyProfiles(cfg *config.Config) bool {
	if len(cfg.DefaultAgentProfiles()) > 0 || len(cfg.GetProfileDefinitions()) > 0 {
		return true
	}
	list, _ := cfg.GetProfileLoader().List()
	return len(list) > 0
}

// ResolvedAgent is an agent resolved into something run / agent_run can
// consume: its profiles composed into ONE assembled context, plus the engine
// applied as the backend (overriding the composed profiles' llm). Phase B
// provides the resolver and the entity.
type ResolvedAgent struct {
	Name string `json:"name"`
	// Engine is the agent's DECLARED engine (may be empty); Label is the
	// engine actually resolved after applying the override precedence.
	Engine   string   `json:"engine,omitempty"`
	Profiles []string `json:"profiles"`
	// Label is the resolved LLM config label, and Backend/Model the transport it
	// maps to — the same (label → backend, model) resolution run/oneshot use.
	Label   string `json:"label"`
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	// Context is the assembled context composed from the agent's profiles;
	// Fragments names what loaded into it.
	Context   string   `json:"context,omitempty"`
	Fragments []string `json:"fragments,omitempty"`
	// Runtime is the RESOLVED runtime axis for this agent (its own choice →
	// project `runtime:` default → ""). Empty means "host" — today's
	// behaviour. Only the runtime axis resolves here: the WORKSPACE axis is a
	// session trait the invocation supplies; the two meet in isolation.Axes at
	// launch.
	Runtime string `json:"runtime,omitempty"`
	// Permissions is the agent's DECLARED launch-time permission posture (may be
	// empty). The run resolver applies the engine-label default and the built-in
	// fallback on top; the `run --permissions` flag overrides it.
	Permissions string `json:"permissions,omitempty"`
	// EffectivePermissions is the posture an interactive run resolves to WITHOUT a
	// --permissions flag: declared → engine-label config → built-in default
	// (claude-code → bypass, else prompt). It makes a blank-declared claude-code
	// agent's real host-bypass posture visible. A headless run may floor this up to
	// bypass; --permissions overrides it.
	EffectivePermissions string `json:"effectivePermissions,omitempty"`
	// Escalation is the agent's DECLARED approval-request ladder (may be
	// empty — the coordinator derives a preset ladder from Permissions when
	// so). Raw, unvalidated config; the coordinator's
	// spawn-time ladder builder validates and converts it.
	Escalation []agents.EscalationRung `json:"escalation,omitempty"`
	// Coordinator mirrors agents.Agent.Coordinator: whether this agent, when
	// run as a delegated child, is trusted with the coordinator-only MCP
	// tools (agent_run/roster/agent_stop/agent_fetch_artifact). Default false
	// = leaf.
	Coordinator bool `json:"coordinator,omitempty"`
	// Driving mirrors agents.Agent.Driving: the agent's declared per-turn
	// execution axis (conversational|oneshot; empty = conversational). The
	// coordinator's per-engine resume-capability gate (coord.resolveResumeMode)
	// consumes this to decide SpawnPlan.ResumeMode.
	Driving agents.DrivingMode `json:"driving,omitempty"`
}

// ResolveAgent resolves the named agent into a composed context + an
// applied engine:
//
//  1. compose the agent's profiles[] into one assembled context, via the
//     shared multi-profile assembly path (AssembleContext with Profiles) — the
//     same profile loader run/agent_run resolve through, so local, top-level
//     remote, and bundle profiles ("<bundle>#profiles/<name>") all work, and the
//     merge mirrors profile-parent semantics (later wins / union);
//  2. apply the agent's engine as the backend, OVERRIDING the composed
//     profiles' llm. Precedence (resolveOneshotLabel, shared with run/oneshot):
//     engineOverride (a caller-level -l/--llm) wins over the declared engine;
//     either wins over the composed profiles' llm, then the project's
//     primary/default backend ("default = the project backend"). Pass "" for no
//     override.
//
// The agent DEFINITION is ungated config: resolution touches no trust gate
// and no baseline. (Its constituent fragments/mcp/hooks still gate downstream
// when the composed context is actually assembled/applied.)
func ResolveAgent(ctx context.Context, cfg *config.Config, name, engineOverride string) (*ResolvedAgent, error) {
	sub, ok := cfg.Agent(name)
	if !ok {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return resolveAgentBinding(ctx, cfg, name, sub, engineOverride, nil)
}

// resolveAgentBinding is the shared compose+engine core ResolveAgent goes
// through, so the engine-override precedence and the profile composition live
// in exactly one place. Its name/sub split is general enough that a fan-out
// caller could once build a synthetic bare-profile agent here too (the retired
// map/weave member path did); ResolveAgent's sole surviving caller always
// passes a real configured agent.
//
//   - name is the agent identifier; it labels the result and the no-profiles
//     warning.
//   - sub is the binding to resolve — a configured agent.
//   - engineOverride, when non-empty, REPLACES the agent's declared engine for
//     this resolution (a caller-level -l/--llm override wins over the
//     binding's own engine). Empty leaves the binding's engine in force.
//   - pipe, when non-nil, is the process stage used to assemble the context (a
//     test seam / shared pipeline); nil falls back to the gated exposure one.
//
// Engine precedence (resolveOneshotLabel): the effective engine (override else
// the binding's engine) wins; an empty effective engine falls back to the
// composed profiles' llm, then the project default backend.
func resolveAgentBinding(ctx context.Context, cfg *config.Config, name string, sub agents.Agent, engineOverride string, pipe *bundles.Pipeline) (*ResolvedAgent, error) {
	// Reject an unknown Driving value here too, not just at ParseAgent/
	// SetAgent: this is the ONLY load path a config-key `agents:` entry
	// walks (it is parsed by the whole-config.yaml yaml.Unmarshal, which
	// never routes through agents.ParseAgent), so a hand-edited config.yaml
	// with a typo'd `driving:` must still fail loud here rather than
	// silently resolving to the conversational default.
	if err := agents.ValidateDriving(sub.Driving); err != nil {
		return nil, fmt.Errorf("agent %q: %w", name, err)
	}
	if len(sub.Profiles) == 0 && name != "" {
		// A NAMED agent with no profiles composes empty context — surface it
		// (the binding is almost certainly a mistake) but don't fail: fault
		// tolerance. The synthetic default-profile member (empty name, no
		// profiles → composes the configured defaults) is intentional, so it is
		// excluded from the warning.
		clidiag.Warn("ctxloom", "agent %q declares no profiles; composing empty context", name)
	}

	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: sub.Profiles, Pipeline: pipe})
	if err != nil {
		return nil, fmt.Errorf("agent %q: compose profiles: %w", name, err)
	}

	// Effective engine: an explicit override (a caller-level --llm) wins over the
	// binding's declared engine; either then beats the composed profiles' llm,
	// then the project default — the same precedence run/oneshot use.
	engine := sub.Engine
	if engineOverride != "" {
		engine = engineOverride
	}
	label := resolveOneshotLabel(cfg, engine, ctxResult.ProfileLLM)
	backend, model := ResolveBackend(cfg, label)

	// Effective runtime axis: the agent's own choice wins, else the project's
	// `runtime:` default (cfg.Runtime), else empty (→ host downstream). Empty
	// is byte-identical to today's host behaviour.
	runtime := sub.Runtime
	if runtime == "" {
		runtime = cfg.GetRuntime()
	}
	labelEntry, _ := cfg.GetLLMEntry(label)

	return &ResolvedAgent{
		Name:        name,
		Engine:      sub.Engine,
		Profiles:    sub.Profiles,
		Label:       label,
		Backend:     backend,
		Model:       model,
		Context:     ctxResult.Context,
		Fragments:   ctxResult.FragmentsLoaded,
		Runtime:     runtime,
		Permissions: sub.Permissions,
		// The interactive base an unflagged run resolves to (declared → label →
		// built-in default), so a blank claude-code posture shows its real bypass.
		EffectivePermissions: agent.ResolveDefault(
			[]string{sub.Permissions, labelEntry.Permissions},
			backend == config.BackendClaudeCode).String(),
		Escalation:  sub.Escalation,
		Coordinator: sub.Coordinator,
		Driving:     sub.Driving,
	}, nil
}
