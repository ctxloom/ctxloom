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

// IsNone reports whether the named isolation policy resolves to the host (none)
// policy — an empty name or "none". The fan-out uses it to skip building the
// shared executable trust gate for an all-none run (the default), keeping that
// path byte-identical to the pre-P3 behaviour.
func IsNone(name string) bool {
	return name == "" || name == None{}.Name()
}

// Resolve maps an isolation policy name to its implementation for a run of the
// named BACKEND. Empty and "none" resolve to None; "worktree" resolves to the
// per-agent worktree config-isolation policy (over the default git-binary seam;
// it degrades to None inside PrepareWorkspace when the tree is not a git repo or
// the worktree add fails); "container" resolves to the container policy bound to
// the detected runtime (docker/podman) and the BACKEND's container profile
// (image, auth, overlay set, local-build recipe) — or degrades to None with a
// warning when no runtime can launch. An unknown name likewise degrades to None
// (CLAUDE.md fault tolerance: a bad policy string, or an unavailable runtime,
// must never block the LLM).
//
// Note the container policy degrades in TWO places: here (no runtime at all) and
// again in PrepareWorkspace (runtime present but the image absent AND not locally
// buildable, no resolvable auth, or scratch cannot be created). Both surface a
// warning and fall back to None, so defaults.isolation:container is a safe
// default.
func Resolve(name, backend string) Policy {
	switch name {
	case "", None{}.Name():
		return None{}
	case Worktree{}.Name():
		// Config isolation, no runtime dependency — the git-repo check and the
		// worktree-add both degrade to None inside PrepareWorkspace (caller warns).
		return NewWorktree(nil)
	case Container{}.Name():
		rt := SelectRuntime("")
		if _, isHost := rt.(Host); isHost {
			clidiag.Warn("ctxloom", "container isolation requested but no container runtime is available; using none")
			return None{}
		}
		return NewContainerFor(rt, backend)
	default:
		clidiag.Warn("ctxloom", "unknown isolation policy %q; using none", name)
		return None{}
	}
}

// PrepareMember prepares a FAN-OUT member's workspace for the requested isolation
// name and the member's BACKEND (per-member engines — the backend picks the
// container profile), walking the §2b degrade chain until a policy prepares
// (None never fails). It returns the policy that succeeded and its prepared
// workspace.
//
// It differs from Resolve (the TOP-LEVEL case, which mounts the LIVE project) in
// that a fan-out member's workspace is ALWAYS a worktree when it is isolated —
// concurrent members can't share a cwd (§3). So "container" HERE means
// worktree-in-container (ContainerWorktree), degrading container→worktree→none;
// "worktree" degrades worktree→none; "none"/"" stay on the shared project dir.
// Each degrade warns (CLAUDE.md fault tolerance); the member is never blocked.
func PrepareMember(ctx context.Context, name, backend, projectDir, agentID string) (Policy, Workspace) {
	return prepareChain(ctx, memberChain(name, backend), projectDir, agentID)
}

// memberChain builds the ordered policy chain a fan-out member falls back through
// for the requested isolation name. The container branch probes the runtime ONCE
// (as Resolve does): with a real runtime the chain leads with worktree-in-container
// (over the member backend's container profile) then degrades to a bare worktree
// then none; with no runtime it skips straight to the worktree tier (config
// isolation without a container — NOT none, which would re-introduce the
// shared-cwd clobber §2b). "worktree" leads with the bare worktree; "none"/""
// and unknown resolve to none only.
func memberChain(name, backend string) []Policy {
	switch name {
	case "", None{}.Name():
		return []Policy{None{}}
	case Worktree{}.Name():
		return []Policy{NewWorktree(nil), None{}}
	case Container{}.Name(), ContainerWorktree{}.Name():
		rt := SelectRuntime("")
		if _, isHost := rt.(Host); isHost {
			clidiag.Warn("ctxloom", "container isolation requested for a fan-out member but no container runtime is available; using a worktree")
			return []Policy{NewWorktree(nil), None{}}
		}
		return []Policy{NewContainerWorktreeFor(rt, backend, nil), NewWorktree(nil), None{}}
	default:
		clidiag.Warn("ctxloom", "unknown isolation policy %q; using none", name)
		return []Policy{None{}}
	}
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
