// Package agents implements the LOCAL-ONLY agent entity: a named binding
// of an LLM engine to one or more composed profiles.
//
// "Agent" here always means this BINDING — not the running engine process
// (internal/shared/agent), not the container image an engine runs in
// (isolation's "agent image"), and not Claude Code's native sub-agents
// (.claude/agents/). When ambiguity threatens, say "agent (binding)".
//
// An agent is end-user/local configuration — defined SOLELY in the user's
// .ctxloom, via the `agents:` config key and/or .ctxloom/agents/<name>.yaml.
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
// This package owns only the entity type and the directory reader. Resolution
// (composing the profiles into one context and applying the engine override)
// lives in internal/operations, which has the profile loader and backend
// selection; the config-key source and the two-source merge live in
// internal/config, which owns config.yaml.
package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// SourceConfig is the Agent.Source sentinel for an agent declared under the
// `agents:` key of config.yaml (as opposed to a .ctxloom/agents/*.yaml
// file, whose Source is the file path).
const SourceConfig = "config"

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
	// Name is the binding's name. It is the map key (config source) or the
	// file's base name (directory source), never encoded in the body.
	Name string `yaml:"-"`
	// Source records where the definition came from: SourceConfig for the
	// config.yaml `agents:` key, otherwise the .yaml file path. Never
	// serialized — the agent-side mirror of profiles.Profile.Path.
	//
	// It is NOT diagnostic-only. It is the discriminator that tells the two
	// sources apart, and the write path branches on it: the config-key writer
	// warns that a file definition has been shadowed, and the config-key
	// remover REFUSES to delete a file-sourced agent. Ask that question through
	// FromConfig rather than comparing this string, so the sentinel stays this
	// package's to define.
	Source string `yaml:"-"`

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
	// DrivingModeNames() or ParseAgent/ValidateDriving REJECTS the file (a
	// typo here changes execution semantics, unlike Runtime/Permissions'
	// advisory-only unknown-value handling, so it does not get their lenient
	// treatment).
	Driving DrivingMode `yaml:"driving,omitempty"`
}

// FromConfig reports whether this binding came from config.yaml's `agents:`
// key rather than a .ctxloom/agents/<name>.yaml file. It is the one place the
// SourceConfig sentinel is interpreted: the config-key write path can only
// edit and delete what config.yaml owns, so it must be able to ask which
// source a binding has without reproducing the sentinel comparison.
func (a Agent) FromConfig() bool {
	return a.Source == SourceConfig
}

// DrivingMode is Agent.Driving's enum: the per-turn execution axis a binding
// declares. See Agent.Driving's doc for the model; ValidateDriving is the
// single accessor every load path (ParseAgent, operations.resolveAgentBinding,
// operations.SetAgent) validates through, so the accepted vocabulary lives in
// exactly one place.
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
// naming the accepted vocabulary. The single validation body ParseAgent (file
// load), operations.resolveAgentBinding (resolve, covers config-key-sourced
// agents that bypass ParseAgent entirely), and operations.SetAgent (the CLI
// write path, so a typo is caught before it is ever persisted) all call.
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

// ParseAgent unmarshals agent YAML into an Agent. Name and Source are
// not set (they are not encoded in the file); callers assign them.
func ParseAgent(data []byte) (*Agent, error) {
	var s Agent
	// Named before the decoder gets a chance to report it. KnownFields(true)
	// would reject the retired key anyway, but as "field engine not found in
	// type agents.Agent" — which says the key is wrong without saying what to
	// type instead, and this rename is the reason it is wrong.
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err == nil {
		if _, found := probe[RetiredLLMKey]; found {
			return nil, ErrRetiredLLMKey
		}
		if _, found := probe[RetiredCoordinatorKey]; found {
			return nil, ErrRetiredCoordinatorKey
		}
	}
	if err := yamlx.DecodeStrict(data, &s); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if err := ValidateDriving(s.Driving); err != nil {
		return nil, err
	}
	// A named agent binding "one or more composed profiles" (see
	// the package doc) that declares zero is not a degenerate-but-valid
	// binding, it is the signature of a mistyped key (`profils:` for
	// `profiles:`) or a zero-byte/comments-only file — reject it loudly
	// rather than emit a blank binding that composes nothing.
	if len(s.Profiles) == 0 {
		return nil, fmt.Errorf("agent declares no profiles (need at least one under 'profiles:')")
	}
	return &s, nil
}

// RetiredLLMKey is the pre-rename spelling of Agent.LLM.
//
// It is refused rather than ignored on both decode paths. A file-sourced agent
// meets KnownFields(true), but a CONFIG-KEY agent under `agents:` does not:
// internal/config decodes leniently, so an untouched `engine:` there would be
// dropped in silence and the binding would fall back to the profiles' llm —
// a different model, chosen by nobody, reported as success.
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

// Loader reads agent definitions from .ctxloom/agents directories. It is
// the directory-source half; the config-key source and the merge live in
// internal/config. Deliberately simpler than profiles.Loader: agents are pure
// local config with no remote seeding, schema upgrades, or ref canonicalization.
type Loader struct {
	dirs []string
	fs   afero.Fs
}

// NewLoader creates an agent directory loader. A nil fs means the real OS
// filesystem (the production default) — the same nil-fs convention
// GetAgentDirs uses, so discovery and reads agree on one filesystem.
func NewLoader(dirs []string, fs afero.Fs) *Loader {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	return &Loader{dirs: dirs, fs: fs}
}

// List returns every agent defined under the loader's directories, sorted by
// name. Per ctxloom's fault-tolerance philosophy a file that fails to parse is a
// stderr warning and is skipped — one bad definition never sinks the rest. The
// first directory wins on a name collision.
func (l *Loader) List() ([]*Agent, error) {
	var out []*Agent
	seen := make(map[string]bool)

	for _, dir := range l.dirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil {
			// A directory that exists but cannot be statted (permissions, a
			// dead mount) is skipped like an absent one, but it is NOT the
			// same thing: silence here makes "no agents configured" and
			// "your agents directory is unreadable" indistinguishable.
			clidiag.Warn("ctxloom", "skipping agents directory %s: %v", dir, err)
			continue
		}
		if !exists {
			continue
		}
		err = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// A DIRECTORY we cannot enumerate hides an unknown number of
				// agents — every definition beneath it is invisible, and
				// reporting that as "no agents here" is the silent-empty-scan
				// this codebase treats as a bug. afero.Walk passes the
				// directory's own info alongside the readDirNames error and a
				// nil info when it could not even stat the entry, so the two
				// cases are distinguishable: report the directory, warn-and-
				// skip the single entry (one stray file never sinks the rest,
				// per the doc above).
				if info != nil && info.IsDir() {
					return fmt.Errorf("unreadable agents directory %s: %w", path, err)
				}
				clidiag.Warn("ctxloom", "skipping unreadable agents entry %s: %v", path, err)
				return nil
			}
			if info.IsDir() {
				return nil
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			subName := filepath.ToSlash(strings.TrimSuffix(strings.TrimSuffix(rel, ".yaml"), ".yml"))
			if seen[subName] {
				return nil
			}
			sub, lerr := l.loadFile(path)
			if lerr != nil {
				// Degrade, but say so: a corrupt agent silently vanishing is
				// undiagnosable (CLAUDE.md fault tolerance).
				clidiag.Warn("ctxloom", "skipping agent %s: %v", path, lerr)
				return nil
			}
			// The name is claimed only by a definition that RESOLVED. A file
			// that failed to load has not defined this agent, so it must not
			// shadow a valid same-named definition in a later directory —
			// first-directory-wins ranks working definitions, not filenames.
			seen[subName] = true
			sub.Name = subName
			out = append(out, sub)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan agents directory %s: %w", dir, err)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (l *Loader) loadFile(path string) (*Agent, error) {
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, err
	}
	sub, err := ParseAgent(data)
	if err != nil {
		return nil, err
	}
	sub.Source = path
	return sub, nil
}

// GetAgentDirs returns the existing agent directories for the given ctxloom
// paths, resolved against fs. A nil fs means the real OS filesystem (the
// production default). Mirrors profiles.GetProfileDirs, including its reason for
// taking an fs: discovery must follow the same filesystem the loader reads, or
// an injected fs is silently ignored.
func GetAgentDirs(fs afero.Fs, scmPaths []string) []string {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	var dirs []string
	for _, scmPath := range scmPaths {
		dir := paths.AgentsPath(scmPath)
		isDir, err := afero.DirExists(fs, dir)
		if err != nil {
			// Same reason as Loader.List: an absent directory is the ordinary
			// case and stays quiet, but a stat FAILURE dropped in silence
			// hides an unreadable directory behind "no agents configured" one
			// layer before the loader is ever asked.
			clidiag.Warn("ctxloom", "skipping agents directory %s: %v", dir, err)
			continue
		}
		if isDir {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
