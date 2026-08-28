// Package isolation is the host-side seam that decides, per agent, HOW its
// plugin process is spawned and WHERE its workspace lives. Isolation wraps the
// PLUGIN (`ctxloom llm serve <backend>`, one subprocess per run/member) plus the
// workspace it runs in — NOT the engine: the plugin-internal engine-spawn
// (RunLaunchSpec, the chat transports) is untouched. The seam sits at the
// host-side pb.ClientFactory + RunOptions.WorkDir boundary, so a delegated
// fan-out (agent_run) can pick a policy per member orthogonally to the engine.
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
	"fmt"
	"os/exec"
	"strings"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// isolationFixIt is the fix-it hint attached to every requested-container
// degrade finding (ClassIsolation): the three ways to restore the boundary
// plus the escape hatch. Shared by the no-runtime site (chainFor) and the
// image/probe/auth site (prepareChain) so the abort listing reads the same
// regardless of which stage dropped the container.
const isolationFixIt = "install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox"

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

// MountPlan is how a policy maps an already-materialized workspace into the
// execution environment: the bind mounts the run is launched with, and the
// per-run env that accompanies them. It is a DESCRIPTION, not a resource —
// building one creates no container and starts nothing; the spawn renders it.
// Empty for the host policies (none/worktree), which execute in the workspace
// directly and so map nothing.
type MountPlan struct {
	// Mounts are the bind mounts layered on top of the workspace's own cwd
	// mount (credential mounts, scoped session state, config overlays, the
	// git common-dir mirror).
	Mounts []Mount
	// Env is the per-run env the mapping carries (scoped auth passthrough,
	// terminal description, scoped git identity).
	Env []string
}

// Policy is the isolation seam: it prepares a per-agent Workspace and spawns
// the plugin client for that workspace. All strategies (none | worktree |
// container) satisfy this one interface, so the fan-out picks a strategy per
// agent without engine-specific logic. The run's approval posture resolves
// wholly from config/CLI/agent (agent.PermissionMode), independent of which
// strategy is in play — an approvals axis on Policy was tried and deleted as
// dead: none of the three strategies' resolvers ever consulted it.
type Policy interface {
	// Name identifies the policy ("none" | "worktree" | "container"), for
	// diagnostics and config round-tripping.
	Name() string
	// ResolveWorkspace materializes the on-disk tree the run executes in: the
	// live project dir, or a per-agent checkout. projectDir is the host's live
	// project root; agentID scopes/names a per-agent workspace (a member
	// label). FILESYSTEM ONLY — it knows nothing about how the workspace will
	// later be mapped into an execution environment and builds no mounts, so a
	// caller may WRITE INTO the returned tree before Mount runs and the mapping
	// will see what it wrote. A policy that cannot materialize its workspace
	// warns and returns an error so the caller degrades down the chain; the run
	// always gets a workspace (None never fails). Dropping a requested CONTAINER
	// boundary is additionally a fatal finding (ClassIsolation) the choke owner
	// aborts on unless --degraded; a workspace-axis degrade (worktree→None)
	// stays a silent fallback.
	//
	// The container gate (runtime reachable / image present / engine auth
	// resolvable) runs HERE rather than in Mount, so a degrade is decided before
	// any base resource is created — the same order the single-call form had.
	ResolveWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error)
	// Mount maps an ALREADY-MATERIALIZED workspace into the execution
	// environment and returns the plan that mapping renders as. It must not
	// create, seed, or otherwise modify workspace CONTENT — whatever the tree
	// held when Mount was called is what the run sees. (The container policy
	// does pre-create the managed-config overlay MOUNTPOINTS inside the tree;
	// they are empty directories a bind mount needs to exist, never content.)
	// none/worktree run in the workspace directly and map nothing, so their
	// plan is empty — that is the whole answer, not a stub.
	Mount(ctx context.Context, ws Workspace) (MountPlan, error)
	// PrepareWorkspace is ResolveWorkspace followed immediately by Mount, with
	// no gap between them: the composed step for every caller that writes
	// nothing into the tree in between. A caller that DOES need to write there
	// calls the two halves itself — that gap is the reason they are separate.
	// A Mount failure tears the resolved workspace down before returning, so a
	// failed prepare never leaks a checkout or a scratch tree.
	PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error)
	// SpawnClient launches the plugin process for a prepared workspace and
	// returns its client. none/worktree → a bare self-invoked `ctxloom llm serve`
	// subprocess (the workspace is expressed purely via RunOptions.WorkDir);
	// container → docker/podman run with gRPC over a network transport.
	// spawnEnv is the PER-SPAWN env stamped onto the runner process (the
	// coordinator reach-back trio): host → cmd.Env entries; container →
	// bare-name `-e` forms with the values on the run-process env (never
	// `-e KEY=VAL` argv — /proc/<pid>/cmdline is world-readable — and never
	// the process-global launcher env, racy across concurrent spawns).
	SpawnClient(backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (pb.Client, error)
	// StartRunner launches the engine RUNNER process for a prepared workspace
	// with NO go-plugin transport — the StartRun spawn half (Phase
	// 1). Container → a docker/podman `run` of `ctxloom llm host <backend>`
	// with NO plugin socket mount, NO port publish, NO PLUGIN_*/magic-cookie
	// env (the session-state/auth/overlay mounts and the bare-name `-e`
	// spawn-env ARE preserved); host (none/worktree) → a bare self-invoked
	// `ctxloom llm host <backend>` under setsid. Readiness is NOT observed
	// here: the coordinator's awaitRunner (the runner's RunnerChannel Hello) is
	// the barrier that replaces go-plugin's eager handshake. The returned
	// handle's Kill tears the runner down (container: `rm -f` by Name under
	// containerRemoveTimeout + removeReportsGone; host: setsid session sweep);
	// Wait reaps the process, surfacing the captured stderr tail on failure.
	// spawnEnv rides the same channel SpawnClient uses (host → cmd.Env;
	// container → bare-name `-e` with values on the run-process env).
	StartRunner(ctx context.Context, backendName, label string, verbosity int, ws Workspace, spawnEnv map[string]string) (*RunnerHandle, error)
}

// RunnerHandle is a directly-launched engine-runner process (no go-plugin
// client). For a container runner Name is the container name — the durable
// teardown handle (`docker rm -f`); for a host runner it is "". Kill is
// idempotent; Wait reaps the run/serve process (its error carries the stderr
// tail on failure). It is the StartRun spawn-half's counterpart to the
// pb.Client SpawnClient returns for the legacy Chat path.
type RunnerHandle struct {
	Name string
	Kill func()
	Wait func() error
	// StderrTail reads the runner's bounded stderr tail WITHOUT waiting on
	// exit — the diagnostic a caller needs at the moment it declares a
	// launch failed or a runner lost, when there is by definition no exit
	// status to wrap. Wait's error already embeds the same tail for the
	// caller that does reap; this is the accessor for the (production)
	// caller that never does.
	//
	// Nil-safe via StderrTailOf: not every policy fills it, and a caller
	// asking a dead runner why it died must not itself panic.
	StderrTail func() string
}

// containerReadyPoll / containerReadyBound bound AwaitContainerRunning. The
// bound is a backstop, NOT the decision: the two real signals are the container
// being observed running (success) and the runner process exiting (failure). A
// run that neither starts nor exits within the bound is a wedged daemon, which
// is the only case the clock decides.
const (
	containerReadyPoll  = 20 * time.Millisecond
	containerReadyBound = 30 * time.Second
)

// AwaitContainerRunning blocks until h's container is OBSERVED running.
//
// The docker-exec interactive transport hands h.Name straight to a launcher
// that runs `exec -i <name>`. StartRunner returns as soon as the runtime CLI
// PROCESS is spawned — that says nothing about whether the daemon created the
// container, resolved its mounts, or started it. Without this barrier the exec
// was issued BEFORE the `run` (measured: 0.33ms earlier) and failed with a
// "No such container" that names nothing, while the real reason went to the
// runner's stderr and was discarded.
//
// Deliberately NOT inside StartRunner: that path is shared with the owned-run
// (delegated) transport, which does not exec into the container and works
// today. Only the arm that execs needs the barrier.
//
// A runtime with no Binary (Host, or a test fake) cannot be inspected, so this
// reports ready immediately rather than stalling a caller that has no daemon.
func AwaitContainerRunning(rt Runtime, h *RunnerHandle) error {
	if rt == nil || rt.Binary() == "" || h == nil || h.Name == "" {
		return nil
	}
	exited := make(chan error, 1)
	if wait := WaitOf(h); wait != nil {
		go func() { exited <- wait() }()
	}
	deadline := time.Now().Add(containerReadyBound)
	for {
		if containerObservedRunning(rt, h.Name) {
			return nil
		}
		select {
		case werr := <-exited:
			// The runner died before the container came up. Its stderr is the
			// only copy of the reason: the daemon writes it there and --rm then
			// destroys the container, so `logs` is already too late.
			if s := StderrTailOf(h); s != "" {
				return fmt.Errorf("runner container %q exited before it was running: %w (stderr: %s)", h.Name, werr, s)
			}
			return fmt.Errorf("runner container %q exited before it was running: %w", h.Name, werr)
		default:
		}
		if time.Now().After(deadline) {
			if s := StderrTailOf(h); s != "" {
				return fmt.Errorf("runner container %q was not running after %s (stderr: %s)", h.Name, containerReadyBound, s)
			}
			return fmt.Errorf("runner container %q was not running after %s", h.Name, containerReadyBound)
		}
		time.Sleep(containerReadyPoll)
	}
}

// containerObservedRunning reports whether name is running right now. Any error
// — the container not existing yet, an unreadable daemon — is "not yet"; the
// caller's other arms carry the real verdicts.
func containerObservedRunning(rt Runtime, name string) bool {
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, rt.Binary(),
		"container", "inspect", "-f", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// StderrTailOf reads h's bounded stderr tail, tolerating a nil handle or a
// policy that captures nothing. Callers are diagnosing a failure when they
// reach this, so it must never be the thing that fails.
func StderrTailOf(h *RunnerHandle) string {
	if h == nil || h.StderrTail == nil {
		return ""
	}
	return h.StderrTail()
}

// WaitOf returns h's process-exit waiter, or nil when there is no process to
// reap (a nil handle, or a policy that captures none). It returns the FUNCTION
// rather than calling it — Wait blocks until the runner exits, so a nil-safe
// wrapper that invoked it would block its caller for the runner's whole
// lifetime instead of handing over a signal the caller can select on.
//
// The nil return is meaningful, not merely defensive: a caller with no waiter
// genuinely cannot tell a dead runner from a slow one and must fall back to
// its own timeout. Callers therefore nil-check this rather than assuming a
// no-op waiter, which would look like "the runner is still alive" forever.
func WaitOf(h *RunnerHandle) func() error {
	if h == nil {
		return nil
	}
	return h.Wait
}

// EngineStarter is the StartRun spawn-half seam that replaces pb.ClientFactory
// on the delegated StartEngine path (Phase 1). It binds a policy +
// prepared workspace + backend/label/verbosity + runner env into a single
// launch closure; the go-plugin handshake the factory completed eagerly is
// gone (awaitRunner owns readiness). Defined here — not in operations — so
// isolation, which cannot import operations, can name it as
// StarterForWorkspace's return type; operations references it as
// isolation.EngineStarter.
type EngineStarter func(ctx context.Context) (*RunnerHandle, error)

// StarterForWorkspace binds a policy and a prepared workspace into an
// EngineStarter — the docker-direct / bare-host runner launch the migrated
// StartRun spawn half injects. It is the sibling of FactoryForWorkspace (which
// SURVIVES for the legacy Chat path and every host-local caller); the two
// differ only in what the launch produces (a RunnerHandle vs a pb.Client) and
// that this one carries NO plugin transport across the boundary. backendName/
// label/verbosity bind here (not at call time as FactoryForWorkspace's do)
// because EngineStarter's closure takes only ctx — the StartEngine caller has
// nothing but ctx to give it.
func StarterForWorkspace(p Policy, ws Workspace, backendName, label string, verbosity int, spawnEnv map[string]string) EngineStarter {
	return func(ctx context.Context) (*RunnerHandle, error) {
		return p.StartRunner(ctx, backendName, label, verbosity, ws, spawnEnv)
	}
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
func FactoryForWorkspace(p Policy, ws Workspace, spawnEnv map[string]string) pb.ClientFactory {
	return func(backendName, label string, verbosity int) (pb.Client, error) {
		return p.SpawnClient(backendName, label, verbosity, ws, spawnEnv)
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

// RuntimeAxis says where the agent's engine process executes. It is a type
// ALIAS of agent.RuntimeAxis, not a second declaration: the vocabulary is
// defined exactly once, in internal/shared/agent (the lower package this one
// already imports for other reasons — see ambient.go/auth.go — so there is no
// cycle to route around). isolation.RuntimeAxis IS agent.RuntimeAxis; nothing
// here can drift from it because there is nothing here to drift — the alias
// and the re-exported consts/functions below just carry this package's
// established names forward for its own callers.
type RuntimeAxis = agent.RuntimeAxis

const (
	// RuntimeHost runs the engine directly on the host (the default; also
	// the meaning of an empty value after defaulting).
	RuntimeHost = agent.RuntimeHost
	// RuntimeContainerRootless runs the engine inside a container on a
	// runtime that maps the container's root to the INVOKING HOST USER.
	RuntimeContainerRootless = agent.RuntimeContainerRootless
	// RuntimeContainerRootful runs the engine inside a container on a
	// runtime whose container-root is REAL root, with the image entrypoint
	// remapping to the launching uid/gid (identityEnvArgs).
	RuntimeContainerRootful = agent.RuntimeContainerRootful
)

// IsContainerRuntimeAxis reports whether v is one of the two CONTAINER runtime
// axis values — the "is a container boundary requested at all?" question,
// which is a DIFFERENT question from "which ownership mode?". Re-exports
// agent.IsContainerRuntimeAxis under this package's established name.
//
// There is deliberately no "any container" axis value. Rootless and rootful
// differ in UID mapping, so a workload can genuinely require one, and a config
// that cannot say which one silently gets whichever the host happens to offer.
// Every "did we keep the boundary?" check asks this predicate; every
// SELECTION asks for a specific value.
func IsContainerRuntimeAxis(v RuntimeAxis) bool {
	return agent.IsContainerRuntimeAxis(v)
}

// ParseWorkspaceAxis is the ONE conversion between the workspace-axis string
// vocabulary (config YAML, run/acp --workspace, an agent_run spawn's
// workspace field) and the typed WorkspaceAxis. Every boundary that receives
// a workspace string parses it exactly once, here — never a bare
// WorkspaceAxis(s) conversion, which compiles for any string and hands the
// axis a value nobody admitted.
//
// Empty passes through as "" (the zero value), meaning "this level said
// nothing": each caller's own layering decides what silence resolves to, and
// they do not agree — delegatedAxes defaults a delegated child to worktree
// while Axes.WantsWorktree reads everything that is not WorkspaceWorktree as
// the shared checkout. That disagreement is exactly why an unrecognized
// value cannot be treated as silence: `workspace: "wroktree"` would not
// merely fail to isolate a child, it would flip it from its own worktree
// into the PARENT'S LIVE CHECKOUT — strictly further from safety than the
// empty value it resembles, and past decideDirtyParentTree, which
// short-circuits on any axis that is not WorkspaceWorktree. So anything
// unrecognized is an error naming the bad value and the legal ones.
//
// This does NOT reclassify the workspace axis as a security boundary (see
// warnUnknownAxes for why it is not one, and how the runtime axis differs).
// It refuses TYPOS: a value the user typed that no code path can honor.
func ParseWorkspaceAxis(s string) (WorkspaceAxis, error) {
	switch WorkspaceAxis(s) {
	case "", WorkspaceShared, WorkspaceWorktree:
		return WorkspaceAxis(s), nil
	default:
		return "", fmt.Errorf("unknown workspace axis %q (known: %s)", s, strings.Join(WorkspaceNames(), "|"))
	}
}

// Axes is a fully-defaulted isolation request. The two axes are declared at
// DIFFERENT levels and meet only here: the runtime axis is an AGENT trait
// (`runtime:` on the binding — a cost/environment call, like engine), while
// the workspace axis is an ORCHESTRATION trait (the invocation decides —
// run/acp `--workspace`, an agent_run spawn's workspace field, the project
// default — because needing a private cwd is a property of how you fan, not
// of who the agent is). Empty axis
// values have already been resolved to defaults by the caller; unknown values
// are treated as the axis default by Resolve/chainFor, with a warning
// (CLAUDE.md fault tolerance).
//
//	{none, host}          → None
//	{worktree, host}      → Worktree
//	{none, container-*}     → Container{hostBase} (the LIVE project dir mounted in)
//	{worktree, container-*} → Container{worktreeBase} (name "container-worktree")
//
// Both container-* values map onto the same POLICY: ownership decides which
// RUNTIME may serve the request (SelectRuntime), not which policy realizes it.
type Axes struct {
	Workspace WorkspaceAxis
	Runtime   RuntimeAxis
}

// WantsWorktree reports the workspace axis asks for a worktree; anything else
// (empty, "none", unknown) is the shared project dir.
func (a Axes) WantsWorktree() bool { return a.Workspace == WorkspaceWorktree }

// WantsContainer reports the runtime axis asks for a container in EITHER
// ownership mode; anything else (empty, "host", unknown) is the host.
func (a Axes) WantsContainer() bool { return IsContainerRuntimeAxis(a.Runtime) }

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

// RuntimeNames returns the recognized runtime-axis values. Re-exports
// agent.RuntimeNames() under this package's established name.
func RuntimeNames() []string {
	return agent.RuntimeNames()
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

// warnUnknownAxes reports a broken/typo'd axis value. The two axes differ in
// severity because their defaults differ in blast radius:
//
//   - An unrecognized WORKSPACE value degrades to the shared project dir — a
//     convenience axis, never a security boundary (a lost worktree degrades
//     gracefully everywhere else too), so it stays a plain warn-and-continue.
//   - An unrecognized RUNTIME value degrades to the HOST. The user typed a
//     non-empty runtime, so they asked for SOMETHING other than the default;
//     silently landing UNSANDBOXED on the host when they may have meant
//     `container` is the exact silent security downgrade fail-loudly exists to
//     stop. It is a fatal ClassIsolation finding the choke owner aborts on
//     unless --degraded downgrades it back to the host degrade.
func warnUnknownAxes(a Axes) {
	if a.Workspace != "" && a.Workspace != WorkspaceShared && a.Workspace != WorkspaceWorktree {
		clidiag.Warn("ctxloom", "unknown workspace axis %q (known: %s); treating as %q", a.Workspace, strings.Join(WorkspaceNames(), "|"), WorkspaceShared)
	}
	if a.Runtime != "" && a.Runtime != RuntimeHost && !IsContainerRuntimeAxis(a.Runtime) {
		strictness.Fail(strictness.ClassIsolation,
			"set the runtime axis to one of "+strings.Join(RuntimeNames(), "|")+" (fix the config/flag typo), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox",
			"unknown runtime axis %q (known: %s); this run would land on the HOST without a container boundary (NOT sandboxed) — treating as %q", a.Runtime, strings.Join(RuntimeNames(), "|"), RuntimeHost)
	}
}

// ImageConfig carries the user's image configuration for containerized
// isolation: Image is the optional prebuilt agent-image override (config
// isolation_images), run AS-IS and never built; BaseContainerfile is the
// optional user base Containerfile (config isolation_base_containerfile) an
// on-the-fly local build layers the engine's agent stage onto instead of an
// auto-detected devcontainer / the embedded default base. AppRoot +
// NoDevcontainerBase + DevcontainerService drive the auto-detected project
// devcontainer base; Engines selects a COMPOSABLE backend's
// engine set. Zero value = the backend spec's defaults (devcontainer
// auto-detect ON, engines = every known official-installer fragment).
type ImageConfig struct {
	Image             string
	BaseContainerfile string
	// AppRoot is the project root devcontainer auto-detection resolves
	// .devcontainer/devcontainer.json (or .devcontainer.json) against; ""
	// disables auto-detection (same effect as NoDevcontainerBase).
	AppRoot string
	// NoDevcontainerBase opts out of devcontainer auto-detection (config
	// isolation_devcontainer_base: false / --no-devcontainer-base).
	NoDevcontainerBase bool
	// DevcontainerService names the docker-compose service to use as the base
	// when the detected devcontainer.json declares dockerComposeFile (config
	// isolation_devcontainer_service).
	DevcontainerService string
	// Engines names WHICH per-engine images to build (one image per engine);
	// it is NOT a composition set — an agent image carries exactly one engine.
	// Legacy doc below retained until the field is renamed.
	// Engines selects which engine fragments compose into a COMPOSABLE
	// backend's shared agent image (config isolation_engines); empty = every
	// engine with a known official-installer fragment (composableEngines()).
	Engines []string
}

// selectRuntimeProbe is chainFor's seam onto the host runtime probe
// (SelectRuntime), a package var so tests drive the no-runtime fatal path
// hermetically — SelectRuntime probes the REAL host (docker/podman CLIs +
// daemons), which a unit test must never depend on. Mirrors the sharedFSCheck
// seam in sharedfs.go.
//
// It carries the DEMANDED runtime axis, not just a runtime-name preference:
// selection must reject a runtime whose container ownership is not the one
// asked for, because handing back the other ownership mode is the silent
// substitution the two container values exist to prevent.
var selectRuntimeProbe = SelectRuntime

// chainFor builds the ordered degrade chain for the requested axes. The
// runtime probe runs ONCE; each degrade step drops exactly one axis:
//
//	{worktree, container} → Container{worktreeBase} → Worktree → None
//	{none,     container} → Container{hostBase} (live dir) → None
//	{worktree, host}      → Worktree → None
//	{none,     host}      → None
//
// A container tier never degrades INTO a worktree that wasn't requested, and
// a requested worktree is never dropped just because the container failed.
func chainFor(axes Axes, backend string, img ImageConfig) []Policy {
	warnUnknownAxes(axes)

	if axes.WantsContainer() {
		// The demanded OWNERSHIP rides into selection: a rootful request is
		// served only by a rootful runtime and vice versa. An ownership
		// mismatch comes back as Host{} — indistinguishable here from "no
		// runtime at all", and deliberately so: both mean this run cannot get
		// the boundary it asked for, and both take the SAME fatal path below.
		// Substituting the other ownership mode is never an option, in strict
		// mode or under --degraded.
		rt := selectRuntimeProbe("", axes.Runtime)
		if _, isHost := rt.(Host); !isHost {
			if axes.WantsWorktree() {
				return []Policy{NewContainerWorktreeFor(rt, backend, img, nil), NewWorktree(nil, backend), None{}}
			}
			return []Policy{containerFor(rt, backend, img), None{}}
		}
		// Runtime axis degrades alone: the workspace request below is untouched.
		// A container was EXPLICITLY requested (WantsContainer) but no runtime
		// providing the demanded ownership is reachable, so this run would land
		// UNSANDBOXED on the host — a fail-loudly finding (ClassIsolation) the
		// choke owner aborts on unless --degraded downgrades it back to the
		// warn-and-continue degrade. --degraded falls back to the HOST, never
		// to the other ownership mode: that would be the same silent
		// substitution wearing a flag.
		if axes.WantsWorktree() {
			strictness.Fail(strictness.ClassIsolation, isolationFixIt,
				"runtime: %s requested but no container runtime is available with that ownership; keeping the worktree on the host%s", axes.Runtime, noRuntimeHint())
		} else {
			strictness.Fail(strictness.ClassIsolation, isolationFixIt,
				"runtime: %s requested but no container runtime is available with that ownership; running on the host%s", axes.Runtime, noRuntimeHint())
		}
	}
	if axes.WantsWorktree() {
		// Workspace-only isolation, no runtime dependency — the git-repo check
		// and the worktree-add both degrade to None inside PrepareWorkspace
		// (prepareChain warns). This is the PURE host+worktree path:
		// NewWorktree carries backend so PrepareWorkspace can provision
		// whichever host isolation lever the backend registers — a scoped
		// config-home var (credentialSeedSpecs, auth.go) for claude/codex/
		// kiro. Reached both for a bare {worktree, host} request and for a
		// {worktree, container} request that just degraded to host above (the
		// container was dropped, worktree stays) — either way the agent ends
		// up on the HOST with only a worktree.
		return []Policy{NewWorktree(nil, backend), None{}}
	}
	return []Policy{None{}}
}

// PolicyNameContainer and PolicyNameContainerWorktree are the two
// container-backed policy identities: the ONE place these strings are written.
// The predicate every security-relevant "did we keep the boundary?" check
// funnels through (IsContainerPolicyName) and the bases that produce the names
// (hostBase.name / worktreeBase.name) must agree by construction — a drift
// between a policy's own name and the predicate silently reclassifies a
// container run as an unsandboxed one, which is the one mistake this predicate
// exists to prevent.
const (
	PolicyNameContainer         = "container"
	PolicyNameContainerWorktree = "container-worktree"
)

// IsContainerPolicyName reports whether a policy name denotes a container-backed
// policy (the two that provide a real container boundary). Used by prepareChain
// to detect a degrade that DROPS the container boundary (which warrants a
// prominent, security-framed warning rather than the generic degrade line), and
// by the run path to tell a satisfied container request (container OR
// container-worktree) from one that degraded to the host. Both container-backed
// policies are now Container (host vs worktree base), so this matches on the two
// base NAMES rather than distinct types.
func IsContainerPolicyName(name string) bool {
	return name == PolicyNameContainer || name == PolicyNameContainerWorktree
}

// Prepare prepares a workspace for the requested axes and the run's BACKEND
// (per-member engines — the backend picks the container spec, with the
// user's ImageConfig applied), walking chainFor's degrade chain until a policy
// prepares (None never fails). It returns the policy that succeeded and its
// prepared workspace. One mechanism serves the top-level session and every
// fan-out member alike: the workspace axis arrives from the SESSION
// (invocation flag / project default) and the runtime axis from the AGENT
// binding — this function just realizes their product. state is the run's
// session identity (SessionStateFromEnv over the run's env map): it scopes
// the container policies' durable state mounts and the worktree's ephemeral
// scratch home. Each degrade warns and the run always gets a workspace;
// dropping a requested CONTAINER boundary is additionally a fatal finding
// (ClassIsolation) the choke owner aborts on unless --degraded (a
// workspace-axis degrade stays a silent fallback).
func Prepare(ctx context.Context, axes Axes, backend string, img ImageConfig, projectDir, agentID string, state SessionState) (Policy, Workspace) {
	return prepareChain(ctx, withSessionState(chainFor(axes, backend, img), state), projectDir, agentID)
}

// withSessionState stamps the run's session identity onto every policy in the
// degrade chain that consumes it (the container policies' state mounts, the
// worktree's ephemeral scratch home). Applied AFTER chainFor so the chain
// construction — and Resolve, which only needs policy identity — stays
// state-free. Policies are value types; the stamped copies replace the
// originals in place.
func withSessionState(chain []Policy, state SessionState) []Policy {
	for i, p := range chain {
		switch v := p.(type) {
		case Container:
			// Double-stamp: Container.state scopes the durable state mounts
			// (sessionStateMounts), and the base's withState stamps a worktree
			// base's ephemeral checkout home. The nil-base guard is load-bearing —
			// tests construct bare Container{} — and hostBase.withState is a no-op.
			v.state = state
			if v.base != nil {
				v.base = v.base.withState(state)
			}
			chain[i] = v
		case Worktree:
			v.state = state
			chain[i] = v
		}
	}
	return chain
}

// prepareWorkspace is the one implementation of the composed resolve-then-mount
// step every Policy.PrepareWorkspace delegates to. It lives here, over the
// interface, rather than being written out on each policy: a composition of two
// interface methods is not per-implementation behaviour, and three copies of it
// would be three places for the unwind below to drift.
//
// The unwind is the part worth stating: once ResolveWorkspace returns, the
// workspace OWNS whatever was created for it (the container scratch tree, a
// freshly added checkout), so a Mount failure must Cleanup() rather than leave
// the caller a nil workspace and an orphaned resource.
func prepareWorkspace(ctx context.Context, p Policy, projectDir, agentID string) (Workspace, error) {
	ws, err := p.ResolveWorkspace(ctx, projectDir, agentID)
	if err != nil {
		return nil, err
	}
	if _, err := p.Mount(ctx, ws); err != nil {
		_ = ws.Cleanup()
		return nil, err
	}
	return ws, nil
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
		// Losing the container boundary is a security-relevant downgrade: the run
		// was to be sandboxed and now isn't. The chain only holds a container tier
		// when one was EXPLICITLY requested (chainFor builds it solely for
		// WantsContainer), so a container→non-container transition here is a
		// requested-container-unsatisfiable event: a fail-loudly finding
		// (ClassIsolation) naming the reason.
		//
		// WHICH reasons actually arrive here is NOT settled, and this comment
		// used to assert three of them. Measured 2026-08-24: an ABSENT IMAGE
		// does NOT reach this branch on the container-rootless path — the image
		// is content-addressed, so a fresh composition has none, and ctxloom
		// BUILDS it rather than failing prepare. Shared-fs-probe and
		// unresolvable-auth remain unverified in both directions; do not treat
		// either as demonstrated. Naming an unreached reason here is what let
		// the paired feature claim coverage it did not have (uninvited-maternity). The warning still streams to stderr in both modes
		// (strictness.Fail wraps clidiag.Warn), so a failed or denied container
		// start can't be mistaken for a normal host run; in strict mode the choke
		// owner additionally aborts on it before the unsandboxed engine launches,
		// while --degraded records the finding but acts on none of it, and the chain
		// falls back to the host as before. The runtime-unreachable reason is recorded earlier in chainFor;
		// the handshake-timeout reason surfaces as a fatal SpawnClient error at the
		// choke owner. The `continue` is unchanged — the chain still walks to None
		// so a degraded run gets a workspace.
		if IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next) {
			strictness.Fail(strictness.ClassIsolation, isolationFixIt,
				"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", agentID, err)
			continue
		}
		clidiag.Warn("ctxloom", "isolation %q unavailable for member %q (%v); degrading to %q", p.Name(), agentID, err, next)
	}
	ws, _ := None{}.PrepareWorkspace(ctx, projectDir, agentID)
	return None{}, ws
}
