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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// config.yaml `agents:` key, otherwise the .yaml file path. Diagnostic
	// only (never serialized), the agent-side mirror of profiles.Profile.Path.
	Source string `yaml:"-"`

	// Engine is the LLM config label/backend; overrides the profiles' llm.
	Engine string `yaml:"engine,omitempty"`
	// Profiles compose into one assembled context.
	Profiles []string `yaml:"profiles,omitempty"`
	// Runtime is the agent's RUNTIME axis (host | container): where this
	// agent's engine process executes. Like Engine it is a cost/environment
	// call that travels with the binding. Empty inherits the project's
	// `runtime:` default and finally falls back to "host". Deliberately the
	// ONLY isolation dimension an agent declares — the WORKSPACE axis
	// (worktree vs shared dir) is a SESSION trait chosen at invocation time
	// (run/map/weave `--workspace`, project `workspace:` default), never
	// bound to the agent. Resolution lives in operations.resolveAgentBinding.
	Runtime string `yaml:"runtime,omitempty"`
	// Permissions is the agent's launch-time permission posture
	// (default|acceptEdits|plan|bypass) — the second safety axis a binding
	// declares alongside Runtime. Empty inherits the engine label's configured
	// permissions and finally the built-in default. The `run --permissions` flag
	// overrides it.
	Permissions string `yaml:"permissions,omitempty"`
	// Escalation is the agent's approval-request policy (Wave C2): an
	// ORDERED ladder of rungs, each naming which ApprovalRequest kinds it
	// answers and how. Empty derives the ladder from Permissions — the
	// degenerate two-rung preset (bypass accepts everything; plan declines
	// mutating kinds and relays the rest to the parent). An explicit
	// Escalation overrides the preset entirely (no merge).
	Escalation []EscalationRung `yaml:"escalation,omitempty"`
	// Coordinator marks this agent as trusted to hold the coordinator-only
	// MCP tools (agent_run, roster, agent_stop, agent_fetch_artifact) when it
	// runs as a DELEGATED child (a top-level `ctxloom run` is never gated by
	// this — see the runner's leaf computation in internal/cli/llm_serve.go).
	// Default false = LEAF: a leaf must not receive those four tools, only
	// agent_send/agent_recv/agent_report (parent reporting) — handing a leaf
	// an agent_recv inbox plus a roster lets it infer it has children and
	// stall waiting for notifications that never arrive. Set true only for an
	// agent that itself spawns/manages children (e.g. a coordinator-ensemble
	// or weave role).
	Coordinator bool `yaml:"coordinator,omitempty"`
}

// EscalationRung is one ordered entry of an agent's escalation ladder: which
// ApprovalRequest kinds it matches, the disposition, and — for a relaying
// disposition — how long to wait before falling through to the next
// matching rung. The ladder bottoms at DECLINE when no rung resolves a
// request (coord.buildLadder is the validating parser; this type is the
// raw, hand-editable config shape).
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
	// Ignored by auto_accept/auto_decline (they resolve immediately).
	Timeout string `yaml:"timeout,omitempty"`
}

// ParseAgent unmarshals agent YAML into an Agent. Name and Source are
// not set (they are not encoded in the file); callers assign them.
func ParseAgent(data []byte) (*Agent, error) {
	var s Agent
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	return &s, nil
}

// Loader reads agent definitions from .ctxloom/agents directories. It is
// the directory-source half; the config-key source and the merge live in
// internal/config. Deliberately simpler than profiles.Loader: agents are pure
// local config with no remote seeding, schema upgrades, or ref canonicalization.
type Loader struct {
	dirs []string
	fs   afero.Fs
}

// LoaderOption configures a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) { l.fs = fs }
}

// NewLoader creates an agent directory loader.
func NewLoader(dirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{dirs: dirs, fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(l)
	}
	return l
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
		if err != nil || !exists {
			continue
		}
		err = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil // skip unreadable entries / directories
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
			seen[subName] = true
			sub, lerr := l.loadFile(path)
			if lerr != nil {
				// Degrade, but say so: a corrupt agent silently vanishing is
				// undiagnosable (CLAUDE.md fault tolerance).
				clidiag.Warn("ctxloom", "skipping agent %s: %v", path, lerr)
				return nil
			}
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
		if isDir, err := afero.DirExists(fs, dir); err == nil && isDir {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
