// Package isolation is the host-side seam that decides, per agent, HOW its
// plugin process is spawned and WHERE its workspace lives. Isolation wraps the
// PLUGIN (`ctxloom llm serve <backend>`, one subprocess per run/member) plus the
// workspace it runs in — NOT the engine: the plugin-internal engine-spawn
// (RunLaunchSpec, the chat transports) is untouched. The seam sits at the
// host-side pb.ClientFactory + RunOptions.WorkDir boundary, so map/weave can pick
// a policy per member orthogonally to the engine.
//
// A Policy has two axes:
//   - a Workspace it prepares (the child's cwd) and tears down, and
//   - how it spawns the plugin client for that workspace.
//
// Phase 0 ships only the None (host) policy, which is behaviour-identical to
// today: the workspace is the live project directory, cleanup is a noop, and the
// plugin is a bare self-invoked subprocess. The interface is shaped so a future
// worktree policy (Workspace.Dir = a per-agent git worktree, WIP-safe Cleanup)
// and container policy (SpawnClient via docker/podman run + gRPC-over-network)
// drop in without interface changes.
package isolation

import (
	"context"
	"strings"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Approvals is the policy's approval-handling axis: whether the engine keeps its
// in-tool approval prompt or has it bypassed because a real isolation boundary
// contains the blast radius. Backends do NOT consume it yet — it is represented
// now so the policy carries the choice; Phase 1 wires it into launch flags.
type Approvals int

const (
	// ApprovalsPrompt keeps the engine's in-tool approval prompting — today's
	// behaviour: a trusted host session guarded cooperatively (ltk). The default.
	ApprovalsPrompt Approvals = iota
	// ApprovalsBypass drops the in-engine prompt because an out-of-engine
	// boundary (the container) is the safety net instead. Represented now,
	// consumed by backends in Phase 1.
	ApprovalsBypass
)

// String renders the approvals axis for diagnostics.
func (a Approvals) String() string {
	switch a {
	case ApprovalsBypass:
		return "bypass"
	default:
		return "prompt"
	}
}

// Workspace is the per-agent directory a run executes in (the child engine's
// cwd) plus its teardown. none → the live project dir (noop cleanup); worktree →
// a fresh per-agent git worktree (WIP-safe remove); container → the mounted
// workspace (stop + remove). Cleanup is called once, after the run's client is
// killed.
type Workspace interface {
	// Dir is the workspace directory — the value the caller threads into the
	// member's RunOptions.WorkDir so the engine's cwd lands here.
	Dir() string
	// Cleanup releases the workspace. Safe to call exactly once after the run.
	Cleanup() error
}

// Policy is the isolation seam: it prepares a per-agent Workspace, spawns the
// plugin client for that workspace, and declares its approvals axis. All
// strategies (none | worktree | container) satisfy this one interface, so the
// fan-out picks a strategy per agent without engine-specific logic.
type Policy interface {
	// Name identifies the policy ("none" | "worktree" | "container"), for
	// diagnostics and config round-tripping.
	Name() string
	// Approvals reports this policy's approval-handling axis (see Approvals).
	Approvals() Approvals
	// PrepareWorkspace provisions the workspace the run executes in. projectDir
	// is the host's live project root; agentID scopes/names a per-agent
	// workspace (a member label). Fault tolerance: a policy that cannot prepare
	// its workspace should warn and return an error so the caller degrades — it
	// must never block the LLM. None never fails.
	PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error)
	// SpawnClient launches the plugin process for a prepared workspace and
	// returns its client. none/worktree → a bare self-invoked `ctxloom llm serve`
	// subprocess (the workspace is expressed purely via RunOptions.WorkDir);
	// container → docker/podman run with gRPC over a network transport.
	SpawnClient(backendName, label string, verbosity int, ws Workspace) (pb.Client, error)
}

// EnvWorkspace is an OPTIONAL Workspace capability: a workspace whose isolation
// includes per-agent config-home env vars (CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME
// isolating each engine's GLOBAL config layer) exposes them here. The run threads
// them into the member's RunOptions.Env. None and Container do not implement it
// (None shares the host config; Container isolates via a fresh $HOME), so the
// caller uses WorkspaceEnv to read them only when present.
type EnvWorkspace interface {
	Workspace
	// Env returns the per-agent env additions for the member's engine process.
	Env() map[string]string
}

// WorkspaceEnv returns the per-agent config-home env additions when the workspace
// exposes them (worktree), or nil otherwise (none/container). The caller merges
// the result into the member's RunOptions.Env.
func WorkspaceEnv(ws Workspace) map[string]string {
	if e, ok := ws.(EnvWorkspace); ok {
		return e.Env()
	}
	return nil
}

// FactoryForWorkspace binds a policy and a prepared workspace into the
// pb.ClientFactory seam the fan-out injects (func(backend, label, verbosity) →
// Client). For the None policy the returned factory is behaviour-identical to
// pb.DefaultClientFactory — the same bare self-invoked subprocess.
func FactoryForWorkspace(p Policy, ws Workspace) pb.ClientFactory {
	return func(backendName, label string, verbosity int) (pb.Client, error) {
		return p.SpawnClient(backendName, label, verbosity, ws)
	}
}

// Isolated reports whether the policy provides a real per-agent workspace (a
// worktree or container) rather than sharing the host project dir (none). The
// fan-out uses it to decide whether to write per-member NATIVE config into the
// workspace cwd — safe only when that cwd is isolated; a none member shares the
// project dir and writing per-member config there would clobber the one shared
// surface.
func Isolated(p Policy) bool {
	return p.Name() != None{}.Name()
}

// The isolation request is two INDEPENDENT enum axes, never bound together:
// WHERE the agent's files live (workspace) and WHERE its engine process runs
// (runtime). The four combinations map onto the four policies, and
// degradation respects the axes: a runtime-axis failure (no container
// runtime, image unbuildable) drops ONLY the runtime dimension — the
// workspace dimension is preserved, never silently added or removed.

// WorkspaceAxis says where the agent's working directory lives.
type WorkspaceAxis string

const (
	// WorkspaceShared is the shared live project directory (the default;
	// also the meaning of an empty value after defaulting).
	WorkspaceShared WorkspaceAxis = "none"
	// WorkspaceWorktree gives the agent its own git worktree.
	WorkspaceWorktree WorkspaceAxis = "worktree"
)

// RuntimeAxis says where the agent's engine process executes.
type RuntimeAxis string

const (
	// RuntimeHost runs the engine directly on the host (the default; also
	// the meaning of an empty value after defaulting).
	RuntimeHost RuntimeAxis = "host"
	// RuntimeContainer runs the engine inside a container.
	RuntimeContainer RuntimeAxis = "container"
)

// Axes is a fully-defaulted isolation request. The two axes are declared at
// DIFFERENT levels and meet only here: the runtime axis is an AGENT trait
// (`runtime:` on the binding — a cost/environment call, like engine), while
// the workspace axis is an ORCHESTRATION trait (the invocation decides —
// map/weave `--workspace`, the project default — because needing a private
// cwd is a property of how you fan, not of who the agent is). Empty axis
// values have already been resolved to defaults by the caller; unknown values
// are treated as the axis default by Resolve/chainFor, with a warning
// (CLAUDE.md fault tolerance).
//
//	{none, host}          → None
//	{worktree, host}      → Worktree
//	{none, container}     → Container (the LIVE project dir mounted in)
//	{worktree, container} → ContainerWorktree
type Axes struct {
	Workspace WorkspaceAxis
	Runtime   RuntimeAxis
}

// WantsWorktree reports the workspace axis asks for a worktree; anything else
// (empty, "none", unknown) is the shared project dir.
func (a Axes) WantsWorktree() bool { return a.Workspace == WorkspaceWorktree }

// WantsContainer reports the runtime axis asks for a container; anything else
// (empty, "host", unknown) is the host.
func (a Axes) WantsContainer() bool { return a.Runtime == RuntimeContainer }

// Zero reports no isolation on either axis (shared project dir, host).
// Callers use it to skip isolation-only work (e.g. the fan-out's shared
// executable trust gate) on the default path.
func (a Axes) Zero() bool { return !a.WantsWorktree() && !a.WantsContainer() }

// WorkspaceNames returns the recognized workspace-axis values; RuntimeNames
// the runtime-axis values. Single source for writers (agent set validation,
// CLI completion) and the schema so they never drift from the axes here.
func WorkspaceNames() []string {
	return []string{string(WorkspaceShared), string(WorkspaceWorktree)}
}

// RuntimeNames returns the recognized runtime-axis values.
func RuntimeNames() []string {
	return []string{string(RuntimeHost), string(RuntimeContainer)}
}

// noRuntimeHint appends devcontainer-specific guidance to the no-runtime
// degrade warnings when this process itself runs inside a container — the
// exact situation where "no runtime" usually means "the dev container wasn't
// given one" rather than "docker isn't installed".
func noRuntimeHint() string {
	if InContainer() {
		return " (this process is inside a container without a nested runtime — enable the dev container docker-in-docker feature, or accept the host)"
	}
	return ""
}

// warnUnknownAxes emits one advisory per unrecognized axis value; the value
// then behaves as that axis's default (shared / host) rather than blocking.
func warnUnknownAxes(a Axes) {
	if a.Workspace != "" && a.Workspace != WorkspaceShared && a.Workspace != WorkspaceWorktree {
		clidiag.Warn("ctxloom", "unknown workspace axis %q (known: %s); treating as %q", a.Workspace, strings.Join(WorkspaceNames(), "|"), WorkspaceShared)
	}
	if a.Runtime != "" && a.Runtime != RuntimeHost && a.Runtime != RuntimeContainer {
		clidiag.Warn("ctxloom", "unknown runtime axis %q (known: %s); treating as %q", a.Runtime, strings.Join(RuntimeNames(), "|"), RuntimeHost)
	}
}

// ImageConfig carries the user's image configuration for containerized
// isolation: Image is the optional prebuilt agent-image override (config
// isolation_images), run AS-IS and never built; BaseContainerfile is the
// optional user base Containerfile (config isolation_base_containerfile) an
// on-the-fly local build layers the engine's agent stage onto instead of the
// embedded default base. Zero value = the backend profile's defaults.
type ImageConfig struct {
	Image             string
	BaseContainerfile string
}

// Resolve maps the requested axes to the LEAD policy for a run of the named
// BACKEND — the head of Chain, without preparing anything. Callers that need
// only the policy identity up front (e.g. run's approvals gating) use this;
// preparation with per-axis degradation goes through Prepare.
//
// The runtime axis degrades HERE when no container runtime is available:
// only the container dimension is dropped — a requested worktree stays a
// worktree (axes are independent; degradation never touches the other axis).
// The container policies degrade AGAIN in PrepareWorkspace (runtime present
// but the image absent and not buildable, no resolvable auth, scratch
// creation failure) — see Prepare's chain. Unknown axis values warn and act
// as that axis's default (CLAUDE.md fault tolerance: a bad axis value, or an
// unavailable runtime, must never block the LLM).
func Resolve(axes Axes, backend string, img ImageConfig) Policy {
	return chainFor(axes, backend, img)[0]
}

// chainFor builds the ordered degrade chain for the requested axes. The
// runtime probe runs ONCE; each degrade step drops exactly one axis:
//
//	{worktree, container} → ContainerWorktree → Worktree → None
//	{none,     container} → Container (live dir) → None
//	{worktree, host}      → Worktree → None
//	{none,     host}      → None
//
// A container tier never degrades INTO a worktree that wasn't requested, and
// a requested worktree is never dropped just because the container failed.
func chainFor(axes Axes, backend string, img ImageConfig) []Policy {
	warnUnknownAxes(axes)

	if axes.WantsContainer() {
		rt := SelectRuntime("")
		if _, isHost := rt.(Host); !isHost {
			if axes.WantsWorktree() {
				return []Policy{NewContainerWorktreeFor(rt, backend, img, nil), NewWorktree(nil), None{}}
			}
			return []Policy{containerFor(rt, backend, img), None{}}
		}
		// Runtime axis degrades alone: the workspace request below is untouched.
		if axes.WantsWorktree() {
			clidiag.Warn("ctxloom", "runtime: container requested but no container runtime is available; keeping the worktree on the host%s", noRuntimeHint())
		} else {
			clidiag.Warn("ctxloom", "runtime: container requested but no container runtime is available; running on the host%s", noRuntimeHint())
		}
	}
	if axes.WantsWorktree() {
		// Workspace-only isolation, no runtime dependency — the git-repo check
		// and the worktree-add both degrade to None inside PrepareWorkspace
		// (prepareChain warns).
		return []Policy{NewWorktree(nil), None{}}
	}
	return []Policy{None{}}
}

// Prepare prepares a workspace for the requested axes and the run's BACKEND
// (per-member engines — the backend picks the container profile, with the
// user's ImageConfig applied), walking chainFor's degrade chain until a policy
// prepares (None never fails). It returns the policy that succeeded and its
// prepared workspace. One mechanism serves the top-level session and every
// fan-out member alike: the workspace axis arrives from the SESSION
// (invocation flag / project default) and the runtime axis from the AGENT
// binding — this function just realizes their product. Each degrade warns
// (CLAUDE.md fault tolerance); the run is never blocked.
func Prepare(ctx context.Context, axes Axes, backend string, img ImageConfig, projectDir, agentID string) (Policy, Workspace) {
	return prepareChain(ctx, chainFor(axes, backend, img), projectDir, agentID)
}

// prepareChain tries each policy's PrepareWorkspace in order and returns the first
// that succeeds with its workspace, warning at each degrade. The chain always ends
// in None (which never fails), so a member always gets a workspace; the trailing
// fallback is defensive against an empty/all-failing chain.
func prepareChain(ctx context.Context, chain []Policy, projectDir, agentID string) (Policy, Workspace) {
	for i, p := range chain {
		ws, err := p.PrepareWorkspace(ctx, projectDir, agentID)
		if err == nil {
			return p, ws
		}
		next := None{}.Name()
		if i+1 < len(chain) {
			next = chain[i+1].Name()
		}
		clidiag.Warn("ctxloom", "isolation %q unavailable for member %q (%v); degrading to %q", p.Name(), agentID, err, next)
	}
	ws, _ := None{}.PrepareWorkspace(ctx, projectDir, agentID)
	return None{}, ws
}
