package operations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// AgentEntry is one agent's declared definition, the listing/show shape.
// It carries only what the user wrote — no resolution — so listing many
// agents stays cheap.
type AgentEntry struct {
	Name     string   `json:"name"`
	LLM      string   `json:"llm,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
	// Runtime is the agent's declared runtime axis (host | container), as
	// written; empty inherits the project `runtime:` default.
	Runtime string `json:"runtime,omitempty"`
	// Permissions is the agent's declared permission posture
	// (engine.PermissionModeNames), as written; empty inherits the engine
	// label's default and finally the built-in default.
	Permissions agents.Permissions `json:"permissions,omitempty"`
	// Driving is the agent's declared per-turn execution axis
	// (conversational|oneshot), as written; empty means conversational (the
	// default — see agents.Agent.Driving).
	Driving agents.DrivingMode `json:"driving,omitempty"`
	// HomeMode is the agent's declared per-engine engine-home policy
	// (session|host), as written; empty (undeclared) defaults to session at
	// resolve time — see agents.Agent.HomeMode's doc.
	HomeMode string `json:"engine_home,omitempty"`
	// Auth is the agent's declared auth mode (login|token|api-key), as
	// written; empty (undeclared) is token at resolve time — see
	// agents.Agent.Auth's doc.
	Auth string `json:"auth,omitempty"`
}

// ListAgents returns every locally-defined agent (the `agents:` config key),
// sorted by name. Definitions only — no profile composition or engine
// resolution — so it is cheap over many agents.
func ListAgents(cfg *config.Config) []AgentEntry {
	subs := cfg.LoadAgents()
	out := make([]AgentEntry, 0, len(subs))
	for _, s := range subs {
		out = append(out, AgentEntry{
			Name:        s.Name,
			LLM:         s.LLM,
			Profiles:    s.Profiles,
			Runtime:     s.Runtime,
			Permissions: s.Permissions,
			Driving:     s.Driving,
			HomeMode:    s.HomeMode,
			Auth:        s.Auth,
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
		LLM:         sub.LLM,
		Profiles:    sub.Profiles,
		Runtime:     sub.Runtime,
		Permissions: sub.Permissions,
		Driving:     sub.Driving,
		HomeMode:    sub.HomeMode,
		Auth:        sub.Auth,
	}, nil
}

// SetAgentRequest is the input for SetAgent: the binding to add or update
// under the local `agents:` config key. Engine is optional (empty = project
// default / the composed profiles' llm); Profiles compose into one context;
// Runtime is optional (one of isolation.RuntimeNames — host |
// container-rootless | container-rootful; empty inherits the project
// `runtime:` default); Permissions is optional (a mode of the engine the
// agent binds, written into that engine's block; empty clears it). The workspace axis is deliberately
// NOT settable here — it is a session trait chosen at invocation time, never
// stored on a binding.
type SetAgentRequest struct {
	Name string `json:"name"`
	// LLM is the LLM engine/label to bind. A non-empty value is REJECTED
	// unless it names something operations.AvailableLLMNames knows (a
	// registered backend or a config-declared label) — see checkAgentWrite.
	// Empty CLEARS the override, falling back to the profiles' llm and then
	// the project default.
	LLM      *string   `json:"llm,omitempty"`
	Profiles *[]string `json:"profiles,omitempty"`
	// Runtime is optional (one of isolation.RuntimeNames — host |
	// container-rootless | container-rootful). An unknown non-empty value is
	// REJECTED (SetAgent returns an error, nothing is persisted) — see
	// validateAgentAxes's doc for why this axis breaks rather than degrades.
	// Empty CLEARS the override, inheriting the project `runtime:` default.
	Runtime *string `json:"runtime,omitempty"`
	// Surfaces sets the binding's delivery preference (kind -> approach). It is
	// validated against the engine this write RESULTS IN — the requested one if
	// the same call sets it, otherwise the one already recorded — so a pair is
	// refused by the command that typed it rather than by a later session.
	Surfaces map[string]string `json:"surfaces,omitempty"`
	// Roots sets the binding's root selection per surface kind (kind ->
	// root), validated the same way against the engine this write results
	// in.
	Roots       map[string]string `json:"roots,omitempty"`
	Permissions *string           `json:"permissions,omitempty"`
	// Driving sets the per-turn execution axis (conversational|oneshot);
	// empty = conversational (the default, see agents.Agent.Driving). An
	// unknown value here is REJECTED (SetAgent returns an error, nothing is
	// persisted) — see agents.ValidateDriving's doc for why.
	Driving *string `json:"driving,omitempty"`
	// HomeMode sets the binding's per-engine engine-home policy
	// (session|host); empty (undeclared) defaults to session at resolve
	// time, and host is the unsafe selection.
	// An unknown value here is REJECTED (SetAgent returns an error, nothing
	// is persisted) — the same treatment Surfaces gets, and for the same
	// reason: see agents.Agent.HomeMode's doc.
	HomeMode *string `json:"engine_home,omitempty"`
	// Auth sets the binding's auth mode (login|token|api-key); empty
	// (undeclared) is token at resolve time. An unknown mode, or one the
	// engine this write results in does not support, is REJECTED.
	Auth *string `json:"auth,omitempty"`
}

// orKeep dereferences an optional request field: nil means "the caller did not
// name this field", so the existing value is kept.
func orKeep[T any](set *T, existing T) T {
	if set == nil {
		return existing
	}
	return *set
}

// errPermissionsNeedEngine refuses a --permissions write whose engine this
// write does not settle: the mode is the engine's grammar, and lands in
// that engine's block.
var errPermissionsNeedEngine = errors.New("permissions: the mode is checked against, and written into the block of, the engine this agent binds, and this write binds none")

// permissionMode is the key an engine's permission document names its mode
// by, for the --permissions write.
const permissionMode = "mode"

// agentPermissionEngine is the engine whose permissions block a
// --permissions write lands in: the one this write results in, which must
// take the mode (its PermissionModel validates it). "" when the request
// sets no permissions. Refused, writing nothing, when it cannot be checked:
// the config loader would refuse what it cannot honour.
func agentPermissionEngine(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) (string, error) {
	if req.Permissions == nil {
		return "", nil
	}
	backend, _ := ResolveBackend(reg, cfg, resultingAgentEngine(cfg, name, req))
	kind, ok := reg.Lookup(engine.Name(backend))
	if !ok {
		return "", report.Errorf("set --llm in the same command, so the mode can be checked against the engine it binds",
			"agent %q: %w", name, errPermissionsNeedEngine)
	}
	if *req.Permissions == "" {
		return backend, nil
	}
	model, ok := kind.Permissions().Get()
	if !ok {
		return "", fmt.Errorf("agent %q: engine %s takes no permission mode (%s)", name, backend, kind.Permissions().AbsentReason())
	}
	if err := model.Validate(map[string]any{permissionMode: *req.Permissions}); err != nil {
		return "", fmt.Errorf("agent %q: %w", name, err)
	}
	return backend, nil
}

// withBlockMode is p with mode written into its block for backend (see
// setBlockMode); a nil mode leaves p as it is.
func withBlockMode(p agents.Permissions, backend string, mode *string) agents.Permissions {
	if mode == nil {
		return p
	}
	p = p.Clone()
	setBlockMode(&p, backend, *mode)
	return p
}

// setBlockMode writes mode into p's block for backend; "" clears it,
// dropping a block left empty.
func setBlockMode(p *agents.Permissions, backend, mode string) {
	block := p.Engines[backend]
	if mode == "" {
		delete(block, permissionMode)
		if len(block) == 0 {
			delete(p.Engines, backend)
		}
		return
	}
	if block == nil {
		block = map[string]any{}
	}
	block[permissionMode] = mode
	if p.Engines == nil {
		p.Engines = map[string]map[string]any{}
	}
	p.Engines[backend] = block
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
//   - Runtime: an unknown value is a security-relevant SUBSTITUTION, not a
//     degrade. `agent create dev --runtime container` used to warn "unknown
//     runtime ... it will run on the host", persist `runtime: container`
//     anyway, and print it back in the success line as if it had been
//     honored — so the user who asked for a container boundary silently got
//     none, the config then failed schema validation at the next `ctxloom
//     run` (exit 3), and the project stopped working until they hand-edited
//     the file. isolation.warnUnknownAxes already refuses this same value at
//     launch time (a fatal ClassIsolation finding) rather than substitute —
//     refusing it here too means the CLI catches its own typo instead of
//     writing a value that only explodes later, one layer down. There is
//     deliberately no "any container" alias to fall back to (see
//     isolation.IsContainerRuntimeAxis's doc): rootless and rootful differ in
//     UID mapping, so silently picking one would itself be a substitution.
//
// The engine membership set is AvailableLLMNames — registered backends UNION
// the labels this config declares — because an agent's engine is a LABEL
// resolved through resolveOneshotLabel/ResolveBackend, not a backend name:
// `agent create finder --engine claude-fast` is one of this command's own help
// examples. That is the SAME set `llm default` accepts and the same one it
// offers on rejection, so this message can never list a name it would refuse.
// Checking EngineExists alone (init's guard, where a freshly scaffolded
// config.yaml genuinely has no labels yet) would reject the documented
// invocation. An explicitly empty engine stays legal: it CLEARS the override,
// falling back to the composed profiles' llm and then the project default.
func validateAgentAxes(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) error {
	if err := validateAgentEngine(reg, cfg, name, req); err != nil {
		return err
	}
	if err := validateAgentRuntime(name, req); err != nil {
		return err
	}
	if req.Driving != nil {
		if err := agents.ValidateDriving(agents.DrivingMode(*req.Driving)); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	if err := validateAgentApproaches(reg, cfg, name, req); err != nil {
		return err
	}
	if err := validateContainerStory(reg, cfg, name, req); err != nil {
		return err
	}
	if err := validateAgentAuth(reg, cfg, name, req); err != nil {
		return err
	}
	return validateAgentHomeMode(name, req)
}

// validateAgentAuth runs the one auth check (checkAgentAuth, the same one
// every launch runs) against the engine this write results in — on every
// runtime alike —
// then asks the engine whether the credential is available now: a mode whose
// credential the human must supply (an API key, a cloud provider's
// variables) is refused until it is, with the engine's own remedy. A missing
// token is not refused: it is read where a run is LAUNCHED — often injected
// there by a secret manager — not in the shell that edits the config, and
// refusing it would make the default mode undeclarable on a fresh machine; a
// run without one is refused instead. Nothing is persisted on a refusal.
func validateAgentAuth(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) error {
	if req.Auth == nil || *req.Auth == "" {
		return nil
	}
	backend, _ := ResolveBackend(reg, cfg, resultingAgentEngine(cfg, name, req))
	if _, ok := reg.Lookup(engine.Name(backend)); !ok {
		return report.Errorf("set --llm in the same command, so the mode can be checked against the engine it binds",
			"agent %q: auth %s: %w", name, *req.Auth, errAuthNeedsEngine)
	}
	a, mode, err := checkAgentAuth(reg, backend, *req.Auth)
	if err != nil || a == nil {
		return wrapAgentErr(name, err)
	}
	_, err = a.Credentials(mode, os.LookupEnv)
	if errors.Is(err, engine.ErrNoCredential) && mode == engine.AuthToken {
		return nil
	}
	return wrapAgentErr(name, err)
}

// errAuthNeedsEngine: an auth mode is written with no engine to check it
// against.
var errAuthNeedsEngine = errors.New("no known engine to check the auth mode against")

// wrapAgentErr names the agent a refusal is about; its remedy stays
// reachable through %w, where the renderer reads it.
func wrapAgentErr(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("agent %q: %w", name, err)
}

// validateAgentEngine refuses a non-empty engine outside AvailableLLMNames.
func validateAgentEngine(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) error {
	if req.LLM == nil || *req.LLM == "" {
		return nil
	}
	if available := AvailableLLMNames(reg, cfg); !slices.Contains(available, *req.LLM) {
		return fmt.Errorf("agent %q: unknown engine %q; valid engines: %s",
			name, *req.LLM, strings.Join(available, ", "))
	}
	return nil
}

// validateAgentRuntime refuses an unparseable runtime axis.
func validateAgentRuntime(name string, req SetAgentRequest) error {
	if req.Runtime == nil {
		return nil
	}
	if _, rterr := launch.ParseRuntimeAxis(*req.Runtime); rterr != nil {
		return fmt.Errorf("agent %q: %w", name, rterr)
	}
	return nil
}

// validateAgentApproaches validates the surface preferences and root
// selections against the engine this write RESULTS IN, not the one recorded
// before it. `agent set x --engine <e> --surface context=system-prompt` for an
// engine without that approach must be refused as one act: checking against
// the OLD engine would accept a pair the new engine cannot honour, and the
// binding would be written already broken.
func validateAgentApproaches(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) error {
	if len(req.Surfaces) > 0 {
		engine := resultingAgentEngine(cfg, name, req)
		if engine == "" {
			return fmt.Errorf("agent %q: a surface preference needs a known engine — set --engine in the same command, "+
				"since which approaches exist is the engine's answer, not ctxloom's", name)
		}
		if _, err := ResolveAgentSurfaces(reg, engine, req.Surfaces); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	if len(req.Roots) > 0 {
		engine := resultingAgentEngine(cfg, name, req)
		if engine == "" {
			return fmt.Errorf("agent %q: a root selection needs a known engine — set --llm in the same command, "+
				"since which roots an approach offers is the engine's answer, not ctxloom's", name)
		}
		if _, err := ResolveAgentRoots(reg, engine, req.Roots); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
	}
	return nil
}

// validateAgentHomeMode refuses an unknown engine_home. It breaks rather than
// degrades: an unknown value here would otherwise silently resolve to the host
// default at launch (fault tolerance's usual treatment), which for THIS key
// means silently dropping the very opt-in the write was trying to make —
// refused here instead, before it is ever persisted.
func validateAgentHomeMode(name string, req SetAgentRequest) error {
	if req.HomeMode == nil || *req.HomeMode == "" {
		return nil
	}
	if _, err := agents.ParseHomeMode(*req.HomeMode); err != nil {
		return fmt.Errorf("agent %q: %w", name, err)
	}
	return nil
}

// resultingAgentEngine is the engine label the binding carries once req is
// written: the requested one if req sets it (even to empty), else the
// recorded one, else empty.
func resultingAgentEngine(cfg *config.Config, name string, req SetAgentRequest) string {
	if req.LLM != nil {
		return *req.LLM
	}
	if existing, ok := cfg.Agent(name); ok {
		return existing.LLM
	}
	return ""
}

// resultingAgentRuntime is the raw runtime the binding launches under once
// req is written: the requested one, else the recorded one, else (when
// either is empty) the project `runtime:` default.
func resultingAgentRuntime(cfg *config.Config, name string, req SetAgentRequest) string {
	runtime := ""
	if req.Runtime != nil {
		runtime = *req.Runtime
	} else if existing, ok := cfg.Agent(name); ok {
		runtime = existing.Runtime
	}
	if runtime == "" {
		runtime = cfg.GetRuntime()
	}
	return runtime
}

// noContainerStory is the phrase both container refusals (validateContainerStory
// and AgentRuntimeOffer's withheld reason) carry, so a test can pin WHAT is
// missing without restating the sentence around it.
const noContainerStory = "declares no container story"

// validateContainerStory refuses a binding whose {engine, runtime: container}
// pair names an engine that declares no container story, so it cannot run
// inside a container at all.
//
// The container story is keyed on the ENGINE (isolation.HasContainerStory over
// engineContainerSpecFor's table), and an engine with no mapping — a generic
// `acp` backend, or any engine nobody has written a resolver for — fails closed
// at PrepareWorkspace: the launch aborts with "no container story is declared
// for this engine". That gate stays as the last line for the paths that never
// went through a binding, but a BINDING is knowable now, so the refusal belongs
// here, at the command that typed the pair, rather than at the first run of an
// agent that has looked fine in `agent list` all along.
//
// Same shape as validateAgentApproaches: validated against the pair this write
// RESULTS IN (the requested field if this call sets it, else the recorded one),
// and for runtime the project `runtime:` default underneath both — a container
// project default makes an unmapped engine just as unlaunchable as an explicit
// `--runtime container` does. An agent with NO engine on the binding is left
// alone: its engine comes from the composed profiles' llm and then the project
// default at resolve time, so there is no pair here to judge.
func validateContainerStory(reg engine.Registry, cfg *config.Config, name string, req SetAgentRequest) error {
	runtime := resultingAgentRuntime(cfg, name, req)
	// Two of the three sources above are still raw at this point: the RECORDED
	// binding and the project `runtime:` default (only req.Runtime was parsed,
	// by the caller). Asserted past the parser, an unrecognized spelling
	// answers "not a container" and this gate returns clean — so the binding
	// is written for an engine that cannot run inside a container,
	// and only the launch discovers it. The runtime axis is a security
	// boundary, so the typo is refused here instead.
	axis, rterr := launch.ParseRuntimeAxis(runtime)
	if rterr != nil {
		return fmt.Errorf("agent %q: %w", name, rterr)
	}
	if !isolation.IsContainerRuntimeAxis(axis) {
		return nil
	}

	label := resultingAgentEngine(cfg, name, req)
	if label == "" {
		return nil
	}
	backend, _ := ResolveBackend(reg, cfg, label)
	if isolation.HasContainerStory(backend) {
		return nil
	}
	engine := fmt.Sprintf("%q", label)
	if backend != label {
		engine = fmt.Sprintf("%q (backend %q)", label, backend)
	}
	return fmt.Errorf("agent %q: engine %s %s, so `runtime: container` cannot run it; engines with a container story: %s — bind one of those, or use runtime: host",
		name, engine, noContainerStory, strings.Join(isolation.ContainerStoryEngines(), ", "))
}

// SetAgent adds or updates a LOCAL agent under the `agents:` config key,
// inside one Owner.Update transaction — the write is a single locked,
// freshly-reloaded read-modify-write that publishes the next generation,
// rather than a read, a mutation, and a later Save that could race a
// concurrent writer. It is the write half the
// agent-assisted setup calls to record the engine↔profile binding the user
// chose.
//
// cfg is used only for the advisory reads below (the same-name shadow
// warning, and resolving remote aliases for canonicalize-on-store); it is
// never mutated. The bind itself is a per-FIELD update applied to the
// transaction's fresh Draft: a nil request field means "the caller did not
// name this field" and keeps whatever the existing binding holds, while an
// explicitly-supplied empty value clears it — never a whole-binding replace,
// which would destroy every field the request did not name. Merging inside
// Update also keeps the read-modify-write
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
// ErrAgentWithoutEngine is the refusal for a binding that would end the write
// with neither an llm nor profiles — nothing that could resolve an engine.
// Config load refuses the same shape on read (config.WarnKindEnginelessAgent),
// so a binding cannot be authored here that the loader would then drop.
var ErrAgentWithoutEngine = errors.New("an agent needs an llm or at least one profile to bind an engine; neither was given")

func SetAgent(ctx context.Context, app *App, cfg *config.Config, req SetAgentRequest) (*AgentEntry, error) {
	reg := app.Engines()
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if app == nil {
		return nil, fmt.Errorf("app is required")
	}
	name := req.Name
	if name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	// Pre-flight, both halves BEFORE the Update transaction opens, so
	// a refusal writes nothing and a typo'd `agent edit` cannot half-apply over
	// a live binding.
	if err := validateAgentAxes(reg, cfg, name, req); err != nil {
		return nil, err
	}
	permEngine, err := agentPermissionEngine(reg, cfg, name, req)
	if err != nil {
		return nil, err
	}

	// Canonicalize-on-store (decision B): a per-remote short profile ref
	// ("<remote>/<bundle>#profiles/<name>") is expanded to its canonical URL so the
	// binding persists a stable identity, not a machine-local alias that a later
	// remote rename would strand. Bare/local names stay verbatim (decision A). This
	// replaces the old verbatim store.
	var entry agents.Agent
	_, err = app.Update(ctx, func(d *config.Draft) error {
		if d.Agents == nil {
			d.Agents = make(map[string]agents.Agent)
		}
		// Start from the record as it stands RIGHT NOW inside the transaction
		// (not from cfg, which was loaded before the lock), so every field the
		// request does not name survives untouched.
		entry = d.Agents[name]
		if req.Profiles != nil {
			entry.Profiles = canonicalizeProfileRefs(*req.Profiles, aliasToURLResolver(cfg))
		}
		entry.LLM = orKeep(req.LLM, entry.LLM)
		// A nil map means "not named" and keeps what is stored; an EMPTY
		// non-nil map is how a caller clears the preference back to the
		// engine's default, matching how the pointer fields above treat an
		// explicit empty string.
		if req.Surfaces != nil {
			if len(req.Surfaces) == 0 {
				entry.Surfaces = nil
			} else {
				entry.Surfaces = maps.Clone(req.Surfaces)
			}
		}
		if req.Roots != nil {
			if len(req.Roots) == 0 {
				entry.Roots = nil
			} else {
				entry.Roots = maps.Clone(req.Roots)
			}
		}
		entry.Runtime = orKeep(req.Runtime, entry.Runtime)
		entry.Permissions = withBlockMode(entry.Permissions, permEngine, req.Permissions)
		if req.Driving != nil {
			entry.Driving = agents.DrivingMode(*req.Driving)
		}
		entry.HomeMode = orKeep(req.HomeMode, entry.HomeMode)
		entry.Auth = orKeep(req.Auth, entry.Auth)
		// Checked against the record the write RESULTS IN, inside the
		// transaction, for the same reason the surface preference is: a
		// create with no --llm/--profiles and an edit that clears the last of
		// them both land here, and returning abandons the Update so the live
		// binding (if any) survives untouched.
		if entry.LLM == "" && len(entry.Profiles) == 0 {
			return fmt.Errorf("agent %q: %w", name, ErrAgentWithoutEngine)
		}
		d.Agents[name] = entry
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("save agent %q: %w", name, err)
	}
	return &AgentEntry{
		Name:        name,
		LLM:         entry.LLM,
		Profiles:    entry.Profiles,
		Runtime:     entry.Runtime,
		Permissions: entry.Permissions,
		Driving:     entry.Driving,
		HomeMode:    entry.HomeMode,
		Auth:        entry.Auth,
	}, nil
}

// RemoveAgent deletes a LOCAL agent from the `agents:` config key, inside one
// Owner.Update transaction: the existence check and the delete happen
// against the same locked, freshly-reloaded Draft, so a concurrent writer can
// never resurrect the entry between the check and the save. An unknown name
// errors.
func RemoveAgent(ctx context.Context, app *App, name string) error {
	if app == nil {
		return fmt.Errorf("app is required")
	}
	if name == "" {
		return fmt.Errorf("agent name is required")
	}
	_, err := app.Update(ctx, func(d *config.Draft) error {
		if _, ok := d.Agents[name]; !ok {
			return fmt.Errorf("agent %q not found in config.yaml", name)
		}
		delete(d.Agents, name)
		return nil
	})
	return err
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
		"the standard trio is an orchestrator you drive, a containerized developer, and a cheap finder, " +
		"plus code-review lenses and any other roles you want to orchestrate."
}

// hasAnyProfiles reports whether the project has any profile to bind an agent
// to: a configured default or a directory profile. Used only to gate the setup
// nudge, so a directory-scan failure degrades to "none" (the nudge simply stays
// quiet) rather than erroring.
func hasAnyProfiles(cfg *config.Config) bool {
	if len(cfg.DefaultAgentProfiles()) > 0 {
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
	// LLM is the agent's DECLARED llm label (may be empty); Label is the
	// label actually resolved after applying the override precedence.
	LLM      string   `json:"llm,omitempty"`
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
	// Surfaces is the agent's DELIVERY PREFERENCE, parsed and already checked
	// against this engine's Declaration. Empty takes the engine default.
	Surfaces map[agent.SurfaceKind]string `json:"-"`
	// Runtime is the RESOLVED runtime axis for this agent (its own choice →
	// project `runtime:` default → RuntimeHost), already PARSED by
	// resolveAgentBinding via launch.ParseRuntimeAxis — a typo'd runtime string
	// on either source fails the resolve loudly rather than reaching here as
	// an unvalidated string a later caller would have to re-interpret. Only
	// the runtime axis resolves here: the WORKSPACE axis is a session trait
	// the invocation supplies; the two meet in isolation.Axes at launch.
	Runtime launch.RuntimeAxis `json:"runtime,omitempty"`
	// Permissions is the agent's DECLARED permission block (may be empty).
	// The run resolver applies the label's keys and the engine's default
	// under it; the `run --permissions` flag overrides the mode.
	Permissions agents.Permissions `json:"permissions,omitempty"`
	// EffectivePermissions is the posture a run resolves to WITHOUT a
	// --permissions flag, named by the engine (EffectivePosture), so a
	// blank-declared agent's real posture is visible rather than "";
	// --permissions overrides it.
	EffectivePermissions string `json:"effectivePermissions,omitempty"`
	// Driving mirrors agents.Agent.Driving: the agent's declared per-turn
	// execution axis (conversational|oneshot; empty = conversational). The
	// coordinator's per-engine resume-capability gate (spawn.resolveResumeMode)
	// consumes this to decide SpawnPlan.ResumeMode.
	Driving agents.DrivingMode `json:"driving,omitempty"`
	// HomeMode is the agent's EFFECTIVE, already-resolved config-home
	// policy — always agents.HomeModeSession or agents.HomeModeHost,
	// never empty, whatever the binding declared (agents.ParseHomeMode's
	// undeclared/unresolvable → session default already applied). It is
	// the value `agent show` reports; the launch resolver reads the same
	// declaration off the binding itself (launch.HomeMode on the
	// CellRequest) and the cells adapter threads it into the environment's
	// Spec (isolation.SpecBuilder.Home) — a launch with NO binding gets the session
	// home by the resolver's own default, not by this field's value.
	HomeMode agents.HomeMode `json:"engine_home,omitempty"`
	// Auth is the agent's EFFECTIVE auth mode: the declared one, or token
	// when undeclared (engine.ParseAuthMode). The launch resolver reads the
	// same declaration off the binding itself.
	Auth engine.AuthMode `json:"auth,omitempty"`
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
func ResolveAgent(ctx context.Context, reg engine.Registry, cfg *config.Config, name, engineOverride string) (*ResolvedAgent, error) {
	sub, ok := cfg.Agent(name)
	if !ok {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return resolveAgentBinding(ctx, reg, cfg, name, sub, engineOverride)
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
func resolveAgentBinding(ctx context.Context, reg engine.Registry, cfg *config.Config, name string, sub agents.Agent, engineOverride string) (*ResolvedAgent, error) {
	// Reject an unknown Driving value here too, not just at SetAgent: an
	// `agents:` entry is parsed by the whole-config.yaml unmarshal, which
	// validates no axis of its own, so a hand-edited config.yaml with a
	// typo'd `driving:` must still fail loud here rather than silently
	// resolving to the conversational default.
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

	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: sub.Profiles})
	if err != nil {
		return nil, fmt.Errorf("agent %q: compose profiles: %w", name, err)
	}

	// Effective engine: an explicit override (a caller-level --llm) wins over the
	// binding's declared engine; either then beats the composed profiles' llm,
	// then the project default — the same precedence run/oneshot use.
	llmName := sub.LLM
	if engineOverride != "" {
		llmName = engineOverride
	}
	label := resolveOneshotLabel(cfg, llmName, ctxResult.ProfileLLM)
	backend, model := ResolveBackend(reg, cfg, label)

	// Effective runtime axis: the agent's own choice wins, else the project's
	// `runtime:` default (cfg.Runtime), else empty (→ RuntimeHost downstream).
	// Empty is byte-identical to today's host behaviour. Parsed HERE — as
	// early as practical, once the two string sources are combined — via the
	// single canonical ParseRuntimeAxis: a typo on either source is refused
	// loudly rather than riding into ResolvedAgent.Runtime as a string a
	// downstream caller would have to re-interpret (and could get wrong).
	runtimeStr := sub.Runtime
	if runtimeStr == "" {
		runtimeStr = cfg.GetRuntime()
	}
	runtime, rterr := launch.ParseRuntimeAxis(runtimeStr)
	if rterr != nil {
		return nil, fmt.Errorf("agent %q: %w", name, rterr)
	}
	labelEntry, _ := cfg.GetLLMEntry(label)

	// The two arms below degrade rather than block, and that rests on one
	// invariant: config.yaml's `agents:` key is the ONLY place a binding comes
	// from, and SetAgent — which refuses both of these values — is the only
	// writer of it. So a value that fails validation HERE was hand-edited into
	// config.yaml after the fact (or, for Surfaces, the engine changed under a
	// pair that was valid when written). That is a config the user can see and
	// fix, not a launch worth refusing; warning names what was dropped so the
	// degrade is never the silent kind.

	surfaces, serr := ResolveAgentSurfaces(reg, backend, sub.Surfaces)
	if serr != nil {
		clidiag.Warn("ctxloom", "agent %q: %v — using %s's default delivery", name, serr, backend)
	}

	configHome, authMode := resolvedHomeAndAuth(name, sub)

	// What an unflagged run resolves to, in the engine's own words; the
	// launch, not this listing, refuses a declaration it cannot honour.
	effectivePerm := PostureName(reg, EffectivePosture(reg, backend, sub.Permissions, labelEntry.Permissions))

	return &ResolvedAgent{
		Name:                 name,
		Surfaces:             surfaces,
		LLM:                  sub.LLM,
		Profiles:             sub.Profiles,
		Label:                label,
		Backend:              backend,
		Model:                model,
		Context:              ctxResult.Context,
		Fragments:            ctxResult.FragmentsLoaded,
		Runtime:              runtime,
		Permissions:          sub.Permissions,
		EffectivePermissions: effectivePerm,
		Driving:              sub.Driving,
		HomeMode:             configHome,
		Auth:                 authMode,
	}, nil
}

// resolvedHomeAndAuth is the binding's effective engine-home and auth modes
// as `agent show` reports them. A declaration that does not parse warns and
// reports the default; the launch itself refuses an unparseable auth.
func resolvedHomeAndAuth(name string, sub agents.Agent) (agents.HomeMode, engine.AuthMode) {
	home, err := agents.ParseHomeMode(sub.HomeMode)
	if err != nil {
		clidiag.Warn("ctxloom", "agent %q: %v — using the real host config home", name, err)
	}
	auth, err := engine.ParseAuthMode(sub.Auth)
	if err != nil {
		clidiag.Warn("ctxloom", "agent %q: %v — a launch refuses it", name, err)
		auth = engine.AuthToken
	}
	return home, auth
}
