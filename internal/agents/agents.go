// Package agents implements the LOCAL-ONLY agent entity: a named binding
// of an LLM engine to one or more composed profiles.
//
// "Agent" here always means this BINDING — not the running engine process
// (internal/shared/agent), not the container image an engine runs in
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
// lives in internal/operations, which has the profile loader and backend
// selection; the source itself lives in internal/config, which owns
// config.yaml.
package agents

import (
	"errors"
	"fmt"
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
	// profiles' llm. It is not an engine: a label names an engine AND a model
	// AND its credentials, and GLOSSARY.md reserves "engine" for what the
	// runner drives. `--llm` is the flag that sets it.
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
	// Validated against the engine's SupportedApproaches when it is WRITTEN
	// (Validate below), not at launch: system-prompt is claude-only, and a kiro
	// agent asking for it should learn so from the command that set it rather
	// than from a session that behaves unexpectedly later.
	Surfaces map[string]string `yaml:"surfaces,omitempty"`
	// Profiles compose into one assembled context.
	Profiles []string `yaml:"profiles,omitempty"`
	// Runtime is the agent's RUNTIME axis (host | container): where this
	// agent's engine process executes. Like Engine it is a cost/environment
	// call that travels with the binding. Empty inherits the project's
	// `runtime:` default and finally falls back to "host". Deliberately the
	// ONLY isolation dimension an agent declares — the WORKSPACE axis
	// (worktree vs shared dir) is a SESSION trait chosen at invocation time
	// (run/acp `--workspace`, an agent_run spawn's workspace field, project
	// `workspace:` default), never bound to the agent. Resolution lives in
	// operations.resolveAgentBinding.
	Runtime string `yaml:"runtime,omitempty"`
	// Permissions is the agent's launch-time permission posture
	// (default|acceptEdits|plan|bypass) — the second safety axis a binding
	// declares alongside Runtime. Empty inherits the engine label's configured
	// permissions and finally the built-in default. The `run --permissions` flag
	// overrides it.
	Permissions string `yaml:"permissions,omitempty"`
	// Escalation is the agent's approval-request policy: an
	// ORDERED ladder of rungs, each naming which ApprovalRequest kinds it
	// answers and how. Empty derives the ladder from Permissions — the
	// degenerate two-rung preset (bypass accepts everything; plan declines
	// mutating kinds and relays the rest to the parent). An explicit
	// Escalation overrides the preset entirely (no merge).
	Escalation []EscalationRung `yaml:"escalation,omitempty"`
	// Driving is the agent's per-turn execution axis: conversational (the
	// zero value/default — the engine process stays warm across turns, the
	// model today) or oneshot (a turn ends its engine process at the turn
	// boundary; the coordinator resumes by native session key on the next
	// mailbox delivery — the one-shot+resume-key model, see the
	// one-shot-resume plan). Slice 2 lands the axis + validation + per-engine
	// resume-capability gating (coord.resolveResumeMode) ONLY: the one-shot
	// turn loop itself is Slice 4 (v0.8) — see the coordinator's gate for the
	// current-release-behavior decision on a resume-capable engine. An empty
	// string parses as DrivingConversational; any other value must be one of
	// DrivingModeNames() or ValidateDriving REJECTS the binding (a typo here
	// changes execution semantics, unlike Runtime/Permissions' advisory-only
	// unknown-value handling, so it does not get their lenient treatment).
	Driving DrivingMode `yaml:"driving,omitempty"`
	// ConfigHome is this binding's per-engine config-home POLICY: whether an
	// in-tree (workspace: none) run gets a ctxloom-CONTROLLED, PER-SESSION
	// engine config home under .ctxloom/state/<harp>/home/<leaf>
	// (ConfigHomeProject) or the engine's REAL host home (ConfigHomeHost,
	// which ctxloom never writes). It is the single source of
	// truth for operations.InTreeAgentHomeEnv's scoping rule, and a DECLARED
	// value wins on every invocation path this binding resolves through — a
	// bare run under default_agent, `run --agent`, a delegated child, a
	// oneshot fan member alike. Invocation never matters for a declared
	// binding; only whether ANY binding is in play at all does (a run with no
	// agent binding — no --agent, no default_agent — has no ConfigHome to
	// read and always keeps the real host home).
	//
	// Empty (undeclared) DEFAULTS TO ConfigHomeHost: nothing gets a
	// controlled in-tree home until a binding explicitly opts in with
	// "project". An unconfigured binding therefore behaves exactly like no
	// binding at all on this one axis — the controlled-home behaviour is
	// strictly opt-in, never assumed.
	//
	// Only the IN-TREE axis (workspace: none) reads this. The worktree axis
	// already provisions a per-agent config home unconditionally, for every
	// run whether or not it is agent-bound, and container's fresh in-container
	// $HOME already is a controlled home — ConfigHome does not touch either.
	//
	// Validated against ConfigHomeNames when WRITTEN (operations.SetAgent,
	// same treatment as Surfaces — an unknown value is refused, naming the
	// two valid ones); a value that fails that same check at RESOLVE time (a
	// hand-edited config.yaml) warns and falls back to ConfigHomeHost rather
	// than blocking the launch (operations.ResolveConfigHome).
	ConfigHome string `yaml:"config_home,omitempty"`
}

// ConfigHomeProject and ConfigHomeHost are Agent.ConfigHome's two accepted
// values. See that field's doc for the scoping rule they select between.
const (
	ConfigHomeProject = "project"
	ConfigHomeHost    = "host"
)

// ConfigHomeNames lists the accepted config_home values, for flag help,
// shell completion, and error messages.
func ConfigHomeNames() []string {
	return []string{ConfigHomeProject, ConfigHomeHost}
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
	// Resolving it requires a resume-capable backend (coord.resolveResumeMode)
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

// parseDrivingMode maps a config/CLI string to a DrivingMode. Unlike
// ParsePermissionMode it is NOT lenient: driving controls whether a child's
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

// EscalationRung is one ordered entry of an agent's escalation ladder: which
// ApprovalRequest kinds it matches, the disposition, and — for a relaying
// disposition — how long to wait before falling through to the next
// matching rung. The ladder bottoms at CANCEL when no rung resolves a
// request — nobody decided, so the engine is told the request was cancelled
// rather than refused (coord.buildLadder is the validating parser; this type
// is the raw, hand-editable config shape).
type EscalationRung struct {
	// Kinds are ApprovalRequest.ApprovalKind names, short form, e.g.
	// "COMMAND_EXECUTION" | "FILE_CHANGE" | "TOOL_USE" |
	// "PERMISSION_ESCALATION" | "ARTIFACT_REVIEW" | "CUSTOM". Empty matches
	// every kind (a catch-all rung).
	Kinds []string `yaml:"kinds,omitempty"`
	// Action is the rung's disposition: auto_accept | auto_decline |
	// relay_to_role | surface_to_human.
	Action string `yaml:"action"`
	// Role is the relay_to_role/surface_to_human target. Only "parent" is
	// addressable in this window (flat-hub topology); empty defaults to
	// "parent" for both actions.
	Role string `yaml:"role,omitempty"`
	// Timeout bounds a relay_to_role/surface_to_human rung's wait, Go
	// duration syntax (e.g. "5m"). Empty uses the resolver's default.
	// REFUSED on auto_accept/auto_decline: they resolve the request
	// immediately, so nothing waits and a declared timeout can only mean the
	// operator expected behaviour the ladder does not have.
	Timeout string `yaml:"timeout,omitempty"`
}

// RetiredLLMKey is the pre-rename spelling of Agent.LLM.
//
// It is refused rather than ignored: internal/config decodes the `agents:` key
// leniently, so an untouched `engine:` would be dropped in silence and the
// binding would fall back to the profiles' llm — a different model, chosen by
// nobody, reported as success. config.findRetiredAgentKey is the refusal.
const RetiredLLMKey = "engine"

// ErrRetiredLLMKey names the current spelling, because a rename that leaves
// people guessing has moved the cost rather than paid it.
var ErrRetiredLLMKey = errors.New(
	"agent uses the retired key 'engine:'; it is now 'llm:' — it selects an llm.configs label " +
		"(engine + model + credentials), not an engine")

// RetiredCoordinatorKey is the REMOVED per-agent delegation-privilege flag.
//
// Unlike RetiredLLMKey this is a removal, not a rename: whether a run may
// delegate is now decided by its position in the tree (its depth against
// delegation.depth), not declared per binding. It is refused for the same
// reason all the same: internal/config decodes `agents:` leniently, so an
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
