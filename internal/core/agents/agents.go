// Package agents implements the LOCAL-ONLY agent entity: a named binding
// of an LLM engine to one or more composed profiles.
//
// "Agent" here always means this BINDING — not the running engine process
// (internal/core/agent), not the container image an engine runs in
// (isolation's "agent image"), and not Claude Code's native sub-agents
// (.claude/agents/). When ambiguity threatens, say "agent (binding)".
//
// An agent is end-user/local configuration — defined SOLELY in the user's
// .ctxloom, under the `agents:` key of config.yaml. That key is the ONE
// source: there is no directory of per-agent files, so every binding is
// schema-validated with the rest of config.yaml and refused at the write edge.
// It is NEVER a bundle item kind, NEVER remote-distributed: there is no
// Bundle.Agents, no "#agents/" ref, and no remote/pull path. Engine/model
// assignment is a user/cost/environment decision, not an author's, so it travels
// with the project, not with shippable content.
//
// The agent DEFINITION is also UNGATED orchestration/config: there is no
// trust.ItemKind for agents, they carry no review state, and they never pass
// through EffectiveTrust. (Their constituent profiles' fragments/commands/mcp/hooks
// still gate when the composed context is assembled/applied — but the binding
// itself is not a trust-addressable surface.)
//
// This package owns only the entity type and its value vocabulary. Resolution
// (composing the profiles into one context and applying the engine override)
// lives in internal/adapters/operations, which has the profile loader and backend
// selection; the source itself lives in internal/core/config, which owns
// config.yaml.
package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Agent is a named, LOCAL-only binding of an engine to a set of profiles.
//
//   - Engine is the LLM config label / backend selection hoisted to the
//     agent. It OVERRIDES the constituent profiles' own llm:. Optional; an
//     empty engine falls back to the composed profiles' llm and finally to the
//     project default backend (resolution lives in operations.ResolveAgent).
//   - Profiles are one or more profiles composed into ONE assembled context
//     (mirroring profile-parent merge: later wins / union). Members may be local,
//     top-level remote, or bundle profiles ("<bundle>#profiles/<name>") — all
//     resolve through the shared profile loader.
type Agent struct {
	// Name is the binding's name: its key in the `agents:` map, never
	// encoded in the body.
	Name string `yaml:"-"`

	// LLM is the llm.configs LABEL this binding selects; overrides the
	// profiles' llm. It is not an engine: a label names an engine AND a
	// model, and GLOSSARY.md reserves "engine" for what the runner drives.
	// `--llm` is the flag that sets it.
	//
	// The retired spelling `engine` is REFUSED at load rather than ignored —
	// see RetiredLLMKey.
	LLM string `yaml:"llm,omitempty"`
	// Surfaces is this binding's DELIVERY PREFERENCE: which approach each
	// surface kind is delivered by, as the labels the CLI already uses
	// ("context: system-prompt"). Empty — the usual case — takes the engine's
	// own default.
	//
	// Preference lives on the binding because it is a property of the CALLER,
	// not of the engine. The engine's approach table is a CAPABILITY list whose
	// first entry is only the at-rest default, and measured 2026-08-08 no
	// single order there serves launch, `profile materialize` and at-rest
	// delivery at once. An agent is always LAUNCHED, which makes it the one
	// caller that can safely prefer system-prompt — the approach that has no
	// argv sink at rest and so cannot be any table's default.
	//
	// Validated against the engine's Declaration when it is WRITTEN
	// (Validate below), not at launch: an approach the engine does not declare
	// should be refused by the command that set it rather than surface as a
	// session that behaves unexpectedly later.
	Surfaces map[string]string `yaml:"surfaces,omitempty"`
	// Roots is this binding's ROOT selection per surface kind
	// (delivery.Preference.Root): under which of the roots the engine's
	// approach for that kind offers its items land — "session-home",
	// "project-root" or "work-dir". Empty takes each approach's default,
	// and every default is the session home: a session delivers only into
	// its own home. project-root and work-dir are the UNSAFE selections —
	// the shared tree every concurrent session reads, written only by this
	// selection and never fallen back to; a run that takes one names the
	// route unsafe in its dry-run plan and launch banner. Validated
	// against the engine's declared approaches when WRITTEN
	// (operations.ResolveAgentRoots), so a run never sees a root the
	// approach does not offer.
	Roots map[string]string `yaml:"roots,omitempty"`
	// Profiles compose into one assembled context.
	Profiles []string `yaml:"profiles,omitempty"`
	// Runtime is the agent's RUNTIME axis (host | container): where this
	// agent's engine process executes. Like Engine it is a cost/environment
	// call that travels with the binding. Empty inherits the project's
	// `runtime:` default and finally falls back to "host". One of the two
	// isolation axes a binding declares, with HomeMode the other — the
	// WORKSPACE axis (worktree vs shared dir) is a SESSION trait chosen at
	// invocation time (run/acp `--workspace`, an agent_run spawn's workspace
	// field, project `workspace:` default), never bound to the agent.
	// Resolution lives in operations.resolveAgentBinding.
	Runtime string `yaml:"runtime,omitempty"`
	// Permissions is the agent's permission block — the second safety axis
	// a binding declares alongside Runtime: the neutral fields, and one block
	// per engine. Each neutral field it leaves empty inherits the engine
	// label's, then the project's, then the engine's default; the engine
	// resolves its own block over the label's keys. `run --permissions`
	// overrides the mode.
	Permissions Permissions `yaml:"permissions,omitempty"`
	// MayDelegate names the roles (agent bindings) this agent may launch
	// with agent_run. Unset or empty permits any; a list permits exactly
	// those. Checked where agent_run is served, against the CALLER's own
	// binding.
	MayDelegate []string `yaml:"may_delegate,omitempty"`
	// Driving is the agent's per-turn execution axis: conversational (the
	// zero value/default — the engine process stays warm across turns, the
	// model today) or oneshot (a turn ends its engine process at the turn
	// boundary; the coordinator resumes by native session key on the next
	// mailbox delivery — the one-shot+resume-key model, see the
	// one-shot-resume plan). Slice 2 lands the axis + validation + per-engine
	// resume-capability gating (spawn.resolveResumeMode) ONLY: the one-shot
	// turn loop itself is Slice 4 (v0.8) — see the coordinator's gate for the
	// current-release-behavior decision on a resume-capable engine. An empty
	// string parses as DrivingConversational; any other value must be one of
	// DrivingModeNames() or ValidateDriving REJECTS the binding (a typo here
	// changes execution semantics, unlike Runtime/Permissions' advisory-only
	// unknown-value handling, so it does not get their lenient treatment).
	Driving DrivingMode `yaml:"driving,omitempty"`
	// HomeMode is this binding's ENGINE-HOME axis: the third isolation axis,
	// a peer of Runtime (which isolates the PROCESS) and the session's
	// workspace (which isolates the FILES). It decides WHICH HOME the engine
	// runs against — the directory holding its credentials, memory, plugins,
	// personal MCP registrations, global agents and steering: a
	// ctxloom-CONTROLLED, PER-SESSION home (paths.HarpSessionEngineHomes, under
	// the ctxloom home's sessions/<harp>/home/<leaf>) (HomeModeSession), or
	// the home its runtime gives it
	// (HomeModeHost — the engine's REAL host home, which ctxloom never
	// writes, or a container's own fresh $HOME). It is the single source of
	// truth for the session-home scoping rule (launch.SessionHome, applied by
	// the run's isolation.Environment), and a DECLARED value wins on every invocation path this
	// binding resolves through — a bare run under default_agent, `run
	// --agent`, a delegated child, a oneshot fan member alike. Invocation
	// never matters for a declared binding; only whether ANY binding is in
	// play at all does (a run with no agent binding — no --agent, no
	// default_agent — has no HomeMode to read and always keeps the real
	// host home).
	//
	// Empty (undeclared) DEFAULTS TO HomeModeSession (ruled 2026-09-21):
	// a run gets the controlled per-session home unless the binding SELECTS
	// the real one with "host" — and that selection is the UNSAFE one,
	// rendered as such in the plan and the launch banner, because it hands
	// the engine the human's own credentials, memory and registrations and
	// lets it write them back. Nothing reaches the real home by default; a
	// binding that wants it asks for it by name.
	//
	// The policy is ORTHOGONAL to the run's isolation cell: whether a run has
	// a controlled home is decided here alone, and which workspace or runtime
	// it chose decides only where the engine is told that home is (on the
	// host, the path itself; in a container, a mount target).
	//
	// This is the DECLARED value as written — a raw string, because a
	// hand-edited config.yaml can hold anything. ParseHomeMode turns it
	// into the EFFECTIVE HomeMode and refuses an unknown value, naming the
	// two valid ones — when WRITTEN (operations.SetAgent) and when RESOLVED
	// (operations.resolveAgentBinding, launch.Resolve) alike. Only a
	// --degraded launch proceeds past it, on HomeModeSession — never onto
	// the real home, which a typo must not select.
	HomeMode string `yaml:"engine_home,omitempty"`
	// EnvHost is whether this agent's engine inherits the launching
	// environment whole on the HOST runtime (podman's --env-host). Absent is
	// true; false curates it (EnvHost the type). A pointer, because an
	// explicit false must survive omitempty. A container forwards only what
	// it names anyway.
	EnvHost *bool `yaml:"env_host,omitempty"`
	// Env is the bare variable names whose host values pass through when
	// EnvHost is false (podman's -e NAME).
	Env []string `yaml:"env,omitempty"`
}

// HostEnv is the binding's env_host and env keys as the launch applies them.
func (a Agent) HostEnv() EnvHost {
	return EnvHost{Curated: a.EnvHost != nil && !*a.EnvHost, Env: slices.Clone(a.Env)}
}

// HomeMode is the EFFECTIVE engine-home policy a declaration parses to: one
// of the two constants below. The zero value is nobody's declaration and
// parses to the default (HomeModeSession) wherever it is consulted.
//
// Deliberately NOT named EngineHome: that name is the resolved PATH (the
// present package's Root, engine.HomeSpec and its kin). This is the policy
// that SELECTS that root, not the root.
type HomeMode string

// HomeModeHost and HomeModeSession are Agent.HomeMode's two accepted
// values. See that field's doc for the scoping rule they select between.
const (
	HomeModeHost    HomeMode = "host"
	HomeModeSession HomeMode = "session"
)

// HomeModeNames lists the accepted engine_home values, for flag help,
// shell completion, and error messages.
func HomeModeNames() []string {
	return []string{string(HomeModeHost), string(HomeModeSession)}
}

// ErrUnknownHomeMode is ParseHomeMode's refusal of a declared engine_home that
// is neither of HomeModeNames. The wrapping error names the rejected value and
// the known ones.
var ErrUnknownHomeMode = errors.New("unknown value")

// ParseHomeMode validates and normalizes a binding's DECLARED
// Agent.HomeMode into its always-non-empty EFFECTIVE value: the declared
// value when it is one of HomeModeNames, HomeModeSession when undeclared
// (empty), so the real home is reached only by an explicit "host". An
// unrecognized value is an error, returned WITH HomeModeSession: the value a
// --degraded launch (launch.Resolve) proceeds on.
//
// One function for every edge, deliberately — the SAME shape ValidateDriving
// has for the same reason — and one behaviour: the error REFUSES. The WRITE
// path (operations.SetAgent) refuses it so nothing is persisted; the RESOLVE
// paths (operations.resolveAgentBinding, launch.Resolve) refuse a hand-edited
// config.yaml the write path never saw, and launch.Resolve alone proceeds
// past it under --degraded.
func ParseHomeMode(declared string) (HomeMode, error) {
	switch HomeMode(declared) {
	case "":
		return HomeModeSession, nil
	case HomeModeHost, HomeModeSession:
		return HomeMode(declared), nil
	default:
		return HomeModeSession, fmt.Errorf("engine_home %q: %w (known: %s)",
			declared, ErrUnknownHomeMode, strings.Join(HomeModeNames(), ", "))
	}
}

// DrivingMode is Agent.Driving's enum: the per-turn execution axis a binding
// declares. See Agent.Driving's doc for the model; ValidateDriving is the
// single accessor both edges (operations.SetAgent at write,
// operations.resolveAgentBinding at resolve) validate through, so the accepted
// vocabulary lives in exactly one place.
type DrivingMode string

const (
	// DrivingConversational is the default (also the empty-string value): the
	// engine process stays warm across turns — today's only model.
	DrivingConversational DrivingMode = "conversational"
	// DrivingOneshot asks for the turn-boundary teardown+resume-by-key model.
	// Resolving it requires a resume-capable backend (spawn.resolveResumeMode)
	// and, in 0.7, additionally fails loud everywhere (Slice 4, the turn loop
	// that would actually honor it, is v0.8) — see that gate's doc for why an
	// accepted-but-inert value was rejected in favor of a hard error.
	DrivingOneshot DrivingMode = "oneshot"
)

// DrivingModeNames lists the accepted CLI/config values, for flag help,
// shell completion, and error messages.
func DrivingModeNames() []string {
	return []string{string(DrivingConversational), string(DrivingOneshot)}
}

// parseDrivingMode maps a config/CLI string to a DrivingMode. It is NOT
// lenient: driving controls whether a child's
// engine process survives past a turn boundary, so a typo silently resolving
// to the default would be a silent, behavior-changing no-op (the class of bug
// this project treats as its worst). Empty parses as DrivingConversational
// (the documented default); anything else must match exactly one of
// DrivingModeNames() or ok is false.
func parseDrivingMode(s string) (DrivingMode, bool) {
	switch DrivingMode(s) {
	case "":
		return DrivingConversational, true
	case DrivingConversational, DrivingOneshot:
		return DrivingMode(s), true
	default:
		return "", false
	}
}

// ValidateDriving rejects an unknown Driving string with a user-facing error
// naming the accepted vocabulary. The single validation body both
// operations.SetAgent (the CLI write path, so a typo is caught before it is
// ever persisted) and operations.resolveAgentBinding (resolve, which catches a
// hand-edited config.yaml the write path never saw) call.
func ValidateDriving(d DrivingMode) error {
	if _, ok := parseDrivingMode(string(d)); !ok {
		return fmt.Errorf("invalid driving %q (known: %s)", string(d), strings.Join(DrivingModeNames(), "|"))
	}
	return nil
}

// RetiredLLMKey is the pre-rename spelling of Agent.LLM.
//
// It is refused rather than ignored: internal/core/config decodes the `agents:` key
// leniently, so an untouched `engine:` would be dropped in silence and the
// binding would fall back to the profiles' llm — a different model, chosen by
// nobody, reported as success. config.findRetiredAgentKey is the refusal.
const RetiredLLMKey = "engine"

// ErrRetiredLLMKey names the current spelling, because a rename that leaves
// people guessing has moved the cost rather than paid it.
var ErrRetiredLLMKey = errors.New(
	"agent uses the retired key 'engine:'; it is now 'llm:' — it selects an llm.configs label " +
		"(engine + model), not an engine")

// RetiredCoordinatorKey is the REMOVED per-agent delegation-privilege flag.
//
// Unlike RetiredLLMKey this is a removal, not a rename: whether a run may
// delegate is now decided by its position in the tree (its depth against
// delegation.depth), not declared per binding. It is refused for the same
// reason all the same: internal/core/config decodes `agents:` leniently, so an
// untouched `coordinator: true` would be dropped in silence, and the binding
// that was written to delegate would quietly become one that cannot — reported
// as success. Real configs carry it, this repo's own among them.
const RetiredCoordinatorKey = "coordinator"

// ErrRetiredCoordinatorKey says what replaced the flag rather than only that
// it is gone, since "unknown key" leaves the reader to guess whether their
// delegation still works.
var ErrRetiredCoordinatorKey = errors.New(
	"agent uses the removed key 'coordinator:'; delegation privilege is no longer declared per " +
		"binding — a run may spawn while its depth is below delegation.depth (the session owner " +
		"is depth 0, its subagents depth 1), so raise delegation.depth to allow deeper trees")

// RetiredAuthKey is the REMOVED per-agent auth mode. A removal, not a move:
// every run ctxloom spawns authenticates with the engine's long-lived token,
// so an agent has no auth to choose; how the HUMAN's own session
// authenticates is the top-level `auth:`.
//
// REFUSED, NOT WARNED: this is a deliberate exception to the rule that an
// unknown config key warns and is ignored. A stale `auth:` on an agent,
// ignored, would silently change which credential that agent's runs use — a
// binding written to share the human's login would quietly run on the token
// — and a warning scrolls past where a refusal cannot.
const RetiredAuthKey = "auth"

// ErrRetiredAuthKey names both replacements: the token every agent runs on,
// and where the human's own login is selected.
var ErrRetiredAuthKey = errors.New(
	"agent uses the removed key 'auth:'; every agent ctxloom spawns authenticates with the engine's " +
		"long-lived token (claude: run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN), so delete " +
		"the key; to have your own `ctxloom run` share your login, set the top-level `auth: login`")

// Delegates reports whether this agent may launch role: any role when
// MayDelegate is unset or empty, else exactly the roles it lists.
func (a Agent) Delegates(role string) bool {
	return len(a.MayDelegate) == 0 || slices.Contains(a.MayDelegate, role)
}
