// Package isolation is the host-side seam that decides, per agent, HOW its
// runner process is started and WHERE its workspace lives. Isolation wraps
// the RUNNER (`ctxloom runner <engine>`, one process per run) plus the
// workspace it runs in — NOT the engine: the runner's own engine exec is
// untouched. The seam sits at the runner-start + workspace boundary, so a
// delegated fan-out (agent_run) can pick a policy per member orthogonally to
// the engine.
//
// A policy has two axes:
//   - a workspace it prepares (the child's cwd) and tears down, and
//   - how it starts the runner for that workspace (StartRunner, or
//     InteractiveRunner for the pty the originator holds).
//
// None (host) is the floor: the workspace is the live project directory,
// cleanup is a noop, and the runner is a bare self-invoked subprocess.
// Worktree gives each agent a git worktree (WIP-safe Cleanup); Container
// runs the runner as a container's foreground process.
package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// THE RULE, stated here because this is the file an author reaches for a
// fallback in: DEGRADING MAY REDUCE WHAT A RUN DELIVERS. IT MAY NEVER DAMAGE
// ANYTHING AND MAY NEVER GRANT A SECURITY BYPASS.
//
// The test is not "is this fault serious?" and not "does it fail silently?" —
// strictness.Fail always streams its warning, so nothing here is ever silent
// and that question separates nothing. The test is strictness.FailAlways's:
// DOES LAUNCHING CAUSE THE HARM? A profile that fails to parse is serious and
// still degradable — the user gets a working LLM with less context. A
// requested container boundary that cannot be provided is not, because the
// launch IS the exposure.
//
// So, concretely, in this package:
//   - Skipping optional work, losing context, or picking the conservative side
//     of an undecidable probe → strictness.Fail (degradable). dockerIsRootless
//     is the worked example: on an unreadable probe it assumes ROOTFUL, which
//     is the safe direction, and degrades.
//   - Dropping a REQUESTED container boundary, running an image that can start
//     as root, or consenting to elevated privilege → strictness.FailAlways
//     (non-degradable). --degraded must not reach these, and a new one must
//     not be added as a plain Fail. TestDegradedNeverBypassesIsolation in
//     isolation_degrade_guard_test.go fails if one is.
//   - Substituting a DECLARED BUILD BASE → also FailAlways, and this one does
//     NOT follow from the launch test, so do not try to re-derive it. Nothing
//     is exposed: the container still runs, still drops privileges, nothing is
//     lost. It refuses because ctxloom CANNOT READ THE CONTAINERFILE and so
//     cannot know whether what the project declared mattered; substituting it
//     silently is the program asserting knowledge it does not have. Ruled
//     2026-09-15; see recordBuildSourceFailure for the full reasoning and the
//     accepted cost.
//
// Do NOT branch on strictness.Degraded() to express any of this. The mode is
// consulted in exactly one place (strictness.Actionable); a site that tests it
// itself is how the two halves of this rule drifted apart in the first place —
// chainFor refused to substitute the other OWNERSHIP mode "in strict mode or
// under --degraded" while, ten lines on, --degraded dropped the whole
// container and ran on the host.

// isolationRemedy is the fix-it hint attached to every requested-container
// finding (ClassIsolation): how to restore the boundary, and how to ask for a
// host run ON PURPOSE. Shared by the no-runtime site (chainFor) and the
// image/probe/auth site (prepareChain) so the abort listing reads the same
// regardless of which stage dropped the container.
//
// It deliberately does NOT offer --degraded. These findings are raised
// non-degradably, so naming the flag would hand the user a remedy that does
// not work — which is worse than naming none. The remedy is to declare the
// host axis, because that is the request the user actually has to make.
const isolationRemedy = "install/build the agent image and start the container runtime (docker/podman), or ask for a host run deliberately with `runtime: host` (the agent's runtime trait via `ctxloom agent edit <agent> --runtime host`, or the project `runtime:` default)"

// workspace is the per-agent directory a run executes in (the child engine's
// cwd) plus its teardown. none → the live project dir (noop cleanup); worktree →
// a fresh per-agent git worktree (WIP-safe remove); container → the mounted
// workspace (stop + remove). Cleanup is called once, after the run's client is
// killed.
type workspace interface {
	// Dir is the workspace directory — the value the caller threads into the
	// member's RunOptions.WorkDir so the engine's cwd lands here.
	Dir() string
	// Cleanup releases the workspace. Safe to call exactly once after the run.
	Cleanup() error
}

// mountPlan is how a policy maps an already-materialized workspace into the
// execution environment: the bind mounts the run is launched with, and the
// per-run env that accompanies them. It is a DESCRIPTION, not a resource —
// building one creates no container and starts nothing; the spawn renders it.
// Empty for the host policies (none/worktree), which execute in the workspace
// directly and so map nothing.
type mountPlan struct {
	// Mounts are the bind mounts layered on top of the workspace's own cwd
	// mount (credential mounts, scoped session state, config overlays, the
	// git common-dir mirror).
	Mounts []mount
	// Env is the per-run env the mapping carries (scoped auth passthrough,
	// terminal description, scoped git identity).
	Env []string
}

// policy is the isolation seam: it prepares a per-agent workspace and starts
// the runner for that workspace. All strategies (none | worktree |
// container) satisfy this one interface, so the fan-out picks a strategy per
// agent without engine-specific logic. The run's approval posture resolves
// wholly from config/CLI/agent (agent.PermissionMode), independent of which
// strategy is in play — an approvals axis on policy was tried and deleted as
// dead: none of the three strategies' resolvers ever consulted it.
type policy interface {
	// Name identifies the policy ("none" | "worktree" | "container"), for
	// diagnostics and config round-tripping.
	Name() string
	// ResolveWorkspace materializes the on-disk tree the run executes in: the
	// live project dir, or a per-agent checkout. projectDir is the host's live
	// project root; agentID scopes/names a per-agent workspace (a member
	// label). FILESYSTEM ONLY — it knows nothing about how the workspace will
	// later be mapped into an execution environment and builds no mounts, so a
	// caller may WRITE INTO the returned tree before mount runs and the mapping
	// will see what it wrote. A policy that cannot materialize its workspace
	// warns and returns an error so the caller degrades down the chain; the run
	// always gets a workspace (None never fails). Dropping a requested CONTAINER
	// boundary is additionally a NON-DEGRADABLE finding (ClassIsolation) the
	// choke owner aborts on in BOTH modes — the workspace still resolves, but
	// the run does not proceed; a workspace-axis degrade (worktree→None) stays
	// a plain warn-and-continue fallback.
	//
	// The container gate (runtime reachable / image present / engine auth
	// resolvable) runs HERE rather than in mount, so a degrade is decided before
	// any base resource is created — the same order the single-call form had.
	resolveWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error)
	// mount maps an ALREADY-MATERIALIZED workspace into the execution
	// environment and returns the plan that mapping renders as. It must not
	// create, seed, or otherwise modify workspace CONTENT — whatever the tree
	// held when mount was called is what the run sees. (The container policy
	// does pre-create the managed-config overlay MOUNTPOINTS inside the tree;
	// they are empty directories a bind mount needs to exist, never content.)
	// none/worktree run in the workspace directly and map nothing, so their
	// plan is empty — that is the whole answer, not a stub.
	bind(ctx context.Context, ws workspace) (mountPlan, error)
	// PrepareWorkspace is ResolveWorkspace followed immediately by mount, with
	// no gap between them: the composed step for every caller that writes
	// nothing into the tree in between. A caller that DOES need to write there
	// calls the two halves itself — that gap is the reason they are separate.
	// A mount failure tears the resolved workspace down before returning, so a
	// failed prepare never leaks a checkout or a scratch tree.
	prepareWorkspace(ctx context.Context, projectDir, agentID string) (workspace, error)
	// StartRunner launches the engine RUNNER process for a prepared workspace
	// — the StartRun spawn half. Container → a docker/podman `run` of
	// `ctxloom runner <backend>` with NO port publish (the session-state/
	// auth/overlay mounts and the bare-name `-e` spawn-env ride it); host
	// (none/worktree) → a bare self-invoked `ctxloom runner <backend>` under
	// setsid. Readiness is NOT observed here: the coordinator's awaitRunner
	// (the runner's RunnerChannel Hello) is the barrier. The returned handle's
	// Kill tears the runner down (container: `rm -f` by Name under
	// containerRemoveTimeout + Runtime.removeOutcome; host: setsid session sweep);
	// Wait reaps the process, surfacing the captured stderr tail on failure.
	// spawnEnv crosses host → cmd.Env; container → bare-name `-e` with values
	// on the run-process env.
	startRunner(ctx context.Context, backendName, label string, verbosity int, ws workspace, spawnEnv map[string]string) (*RunnerHandle, error)
	// InteractiveRunner is the runner process of an INTERACTIVE launch, as a
	// command the originator starts on the pty it holds (adapters/hostpty,
	// adapters/attach): the self-exec'd `ctxloom runner <engine>` on a host
	// cell; `docker run -i -t … ctxloom runner <engine>` — the container's
	// foreground process — for a container cell, whose name is returned so
	// teardown can target it ("" on the host). spawnEnv rides as
	// StartRunner's does. Readiness is the coordinator's awaitRunner.
	interactiveRunner(ctx context.Context, backendName string, ws workspace, spawnEnv map[string]string) (*exec.Cmd, string, error)
	// relocator is this policy's stage 2: how the engine is shown the layout.
	relocator() relocator
	// environment is the Environment over a workspace this policy prepared,
	// holding the relocator's Placement and the mounts produced with it.
	environment(ws workspace, pl launch.Placement, roots []mount) (Environment, error)
	// preview is what a Preview of this policy probes: the listen its runner
	// would need and how it describes itself. It creates nothing.
	preview(ctx context.Context) (present.Listen, Description)
}

// envWorkspace is an OPTIONAL workspace capability: a workspace that
// PROVISIONED something of its own for the run — a per-agent toolchain
// scratch dir, a git identity for the checkout it created — exposes the env
// that points the engine at it (stage 1's layout.env). It is NOT the engine's
// config-home carrier: the home var comes from the session home, once, for
// every environment.
type envWorkspace interface {
	workspace
	// Env returns the env additions for what this workspace provisioned.
	Env() map[string]string
}

// RunnerTerm is the TERM an INTERACTIVE runner process runs under: `dumb`,
// deliberately. The runner is `ctxloom` on the terminal the originator
// holds, and ctxloom's package-init terminal-capability detection
// (lipgloss/termenv querying the background via OSC 11 + a DSR terminator)
// would otherwise fire and READ the response from that same stdin,
// swallowing the human's first keystrokes; `dumb` makes termenv skip the
// query entirely. The ENGINE keeps real color: the launch's engine env,
// laid over the runner's, carries the terminal the human is watching.
const RunnerTerm = "dumb"

// RunnerCommand is the self-exec'd host runner command: `ctxloom runner
// <engine>` with spawnEnv (the reach-back trio) laid over the process env,
// under RunnerTerm, with the admitted companions first on PATH
// (withPinnedPath).
func RunnerCommand(backendName string, spawnEnv map[string]string) *exec.Cmd {
	cmd := exec.Command(selfexec.Path(), "runner", backendName)
	cmd.Env = append(os.Environ(), "TERM="+RunnerTerm)
	cmd.Env = withPinnedPath(append(cmd.Env, envPairs(spawnEnv)...))
	return cmd
}

// envPairs renders spawnEnv as sorted KEY=VAL pairs.
func envPairs(env map[string]string) []string {
	kv := make([]string, 0, len(env))
	for k, v := range env {
		kv = append(kv, k+"="+v)
	}
	sort.Strings(kv)
	return kv
}

// RunnerHandle is a directly-launched runner process. For a container runner
// Name is the container name — the durable teardown handle (`docker rm -f`);
// for a host runner it is "". Kill is idempotent; Wait reaps the process (its
// error carries the stderr tail on failure).
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

// ErrRunnerExitedCleanly is the cause AwaitContainerRunning reports when the
// runner process exited 0 before its container was observed running: there is
// no wait error to wrap, and wrapping the nil printed "%!w(<nil>)".
var ErrRunnerExitedCleanly = errors.New("runner exited with status 0")

// AwaitContainerRunning blocks until h's container is OBSERVED running.
//
// startRunner returns as soon as the runtime CLI PROCESS is spawned — that
// says nothing about whether the daemon created the container, resolved its
// mounts, or started it. A caller that acted on h.Name before this barrier
// (an `exec -i <name>` was measured issuing 0.33ms BEFORE the `run`) failed
// with a "No such container" that names nothing, while the real reason went
// to the runner's stderr and was discarded. The container Environment's
// Start awaits it, so every container runner it hands back is running; the
// teardown's remove-before-create race waits on it too (removeLaunched).
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
			return exitedBeforeRunning(h, werr)
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

// exitedBeforeRunning is AwaitContainerRunning's verdict for a runner that
// died before its container came up. Its stderr is the only copy of the
// reason: the daemon writes it there and --rm then destroys the container, so
// `logs` is already too late.
func exitedBeforeRunning(h *RunnerHandle, werr error) error {
	if werr == nil {
		werr = ErrRunnerExitedCleanly
	}
	if s := StderrTailOf(h); s != "" {
		return fmt.Errorf("runner container %q exited before it was running: %w (stderr: %s)", h.Name, werr, s)
	}
	return fmt.Errorf("runner container %q exited before it was running: %w", h.Name, werr)
}

// containerObservedRunning reports whether name is running right now. Any error
// — the container not existing yet, an unreadable daemon — is "not yet"; the
// caller's other arms carry the real verdicts.
func containerObservedRunning(rt Runtime, name string) bool {
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, rt.Binary(), rt.inspectRunningArgs(name)...).Output()
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

// The isolation axes are launch's value types: WorkspaceAxis, RuntimeAxis
// and the Axes pair are declared once, in core/launch, and this package
// carries its established names forward for its own callers. The four
// combinations map onto the four policies:
//
//	{none, host}          → None
//	{worktree, host}      → Worktree
//	{none, container-*}     → Container{hostBase} (the LIVE project dir mounted in)
//	{worktree, container-*} → Container{worktreeBase} (name "container-worktree")
//
// Both container-* values map onto the same POLICY: ownership decides which
// RUNTIME may serve the request (SelectRuntime), not which policy realizes it.
type (
	WorkspaceAxis = launch.WorkspaceAxis
	RuntimeAxis   = launch.RuntimeAxis
	Axes          = launch.Axes
)

const (
	// WorkspaceShared is the shared live project directory (the default;
	// also the meaning of an empty value after defaulting).
	WorkspaceShared   = launch.WorkspaceNone
	WorkspaceWorktree = launch.WorkspaceWorktree

	RuntimeHost              = launch.RuntimeHost
	RuntimeContainerRootless = launch.RuntimeRootless
	RuntimeContainerRootful  = launch.RuntimeRootful
)

// IsContainerRuntimeAxis is launch.IsContainerRuntimeAxis under this
// package's established name.
func IsContainerRuntimeAxis(v RuntimeAxis) bool {
	return launch.IsContainerRuntimeAxis(v)
}

// ParseWorkspaceAxis is launch.ParseWorkspaceAxis: the ONE conversion between
// the workspace-axis string vocabulary and the typed axis.
func ParseWorkspaceAxis(s string) (WorkspaceAxis, error) {
	return launch.ParseWorkspaceAxis(s)
}

// WorkspaceNames is launch.WorkspaceNames; RuntimeNames is launch.RuntimeNames.
func WorkspaceNames() []string { return launch.WorkspaceNames() }

// RuntimeNames returns the recognized runtime-axis values.
func RuntimeNames() []string { return launch.RuntimeNames() }

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

// containerSelectionHint is the way into a container this host CAN give,
// appended to a refusal of the one that was asked for. It names every
// container ownership mode other than refused that the run-path probe
// (selectRuntimeProbe) can serve, with the explicit selection that opts into
// it — never a substitution made for the user: the two ownership modes differ
// in UID mapping, so choosing the other one is the user's decision, and
// selecting it is how they make it. With nothing reachable it says what to
// install or start.
func containerSelectionHint(refused RuntimeAxis) string {
	var reachable []string
	var pick RuntimeAxis
	for _, axis := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
		if axis == refused {
			continue
		}
		rt := selectRuntimeProbe("", axis)
		if _, isHost := rt.(Host); isHost {
			continue
		}
		reachable = append(reachable, fmt.Sprintf("%s (%s)", axis, rt.Name()))
		pick = axis
	}
	if len(reachable) == 0 {
		// The refused ownership itself is reachable (it failed to START):
		// nothing else to offer, and nothing to install.
		if _, isHost := selectRuntimeProbe("", refused).(Host); !isHost {
			return ""
		}
		return "; no container runtime is reachable on this host — install docker or podman and start it (its daemon, or the rootless service), then run again"
	}
	return fmt.Sprintf("; this host CAN give %s — select it explicitly with `ctxloom agent edit <agent> --runtime %s` (or `runtime: %s` on the binding or as the project default)",
		strings.Join(reachable, ", "), pick, pick)
}

// warnUnknownAxes reports a broken/typo'd axis value. The two axes differ in
// severity because their defaults differ in blast radius:
//
//   - An unrecognized WORKSPACE value degrades to the shared project dir — a
//     convenience axis, never a security boundary (a lost worktree degrades
//     gracefully everywhere else too), so it stays a plain warn-and-continue.
//   - An unrecognized RUNTIME value would degrade to the HOST. The user typed a
//     non-empty runtime, so they asked for SOMETHING other than the default;
//     landing UNSANDBOXED on the host when they may have meant `container` is
//     the exact security downgrade fail-loudly exists to stop. NON-DEGRADABLE:
//     a typo must not be able to remove the boundary, and --degraded is not a
//     way to spell one. The remedy names the known values AND `runtime: host`,
//     so a user who genuinely wants the host can say so in one edit.
//
// The asymmetry is the rule above in miniature: the workspace axis is a
// convenience and degrades, the runtime axis is a boundary and refuses.
func warnUnknownAxes(a Axes) {
	if a.Workspace != "" && a.Workspace != WorkspaceShared && a.Workspace != WorkspaceWorktree {
		clidiag.Warn("ctxloom", "unknown workspace axis %q (known: %s); treating as %q", a.Workspace, strings.Join(WorkspaceNames(), "|"), WorkspaceShared)
	}
	if _, err := launch.ParseRuntimeAxis(string(a.Runtime)); err != nil {
		strictness.FailAlways(report.KindIsolation,
			"set the runtime axis to one of "+strings.Join(RuntimeNames(), "|")+" (fix the config/flag typo), or `runtime: host` if this run really should have no sandbox",
			"%v; refusing to run: an unrecognised runtime would land this session on the HOST without a container boundary (NOT sandboxed), and a typo must not be able to drop it", err)
	}
}

// ImageConfig is launch.ImageConfig under this package's established name.
type ImageConfig = launch.ImageConfig

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
func chainFor(axes Axes, backend string, img ImageConfig) []policy {
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
				return []policy{NewContainerWorktreeFor(rt, backend, img, nil), NewWorktree(nil), None{}}
			}
			return []policy{containerFor(rt, backend, img), None{}}
		}
		// A container was EXPLICITLY requested (WantsContainer) but no runtime
		// providing the demanded ownership is reachable, so this run would land
		// UNSANDBOXED on the host. NON-DEGRADABLE: the launch IS the exposure.
		//
		// This is the site the whole audit turned on, and the inconsistency is
		// worth keeping written down. The ownership rule above has always said
		// substituting the other mode is "never an option, in strict mode or
		// under --degraded" — yet the code here then permitted the STRICTLY
		// LARGER substitution, dropping the container altogether and running on
		// the host, for no reason beyond a flag whose documented job is
		// suppressing STARTUP-FAULT fatality. One flag cannot mean "I accept a
		// thinner context" and "I accept no sandbox" at once. It now means only
		// the first.
		//
		// The chain below is still built and still returned: refusing is the
		// GATE's job (strictness.Actionable keeps a NonDegradable finding under
		// --degraded, so phaseGates.close and isolationGateErr both abort
		// pre-launch). Returning an error here instead would make the caller
		// degrade DOWN THE CHAIN — the exact host fallback being refused.
		if axes.WantsWorktree() {
			strictness.FailAlways(report.KindIsolation, isolationRemedy,
				"runtime: %s requested but no container runtime is available with that ownership; refusing to keep the worktree on the HOST without the container boundary that was asked for%s%s", axes.Runtime, containerSelectionHint(axes.Runtime), noRuntimeHint())
		} else {
			strictness.FailAlways(report.KindIsolation, isolationRemedy,
				"runtime: %s requested but no container runtime is available with that ownership; refusing to run on the HOST without the container boundary that was asked for%s%s", axes.Runtime, containerSelectionHint(axes.Runtime), noRuntimeHint())
		}
	}
	if axes.WantsWorktree() {
		// workspace-only isolation, no runtime dependency — the git-repo check
		// and the worktree-add both degrade to None inside PrepareWorkspace
		// (prepareChain warns). This is the PURE host+worktree path. Reached
		// both for a bare {worktree, host} request and for a
		// {worktree, container} request that just degraded to host above (the
		// container was dropped, worktree stays) — either way the agent ends
		// up on the HOST with only a worktree.
		return []policy{NewWorktree(nil), None{}}
	}
	return []policy{None{}}
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

// withSessionState stamps the run's session identity onto every policy in the
// degrade chain that consumes it (the container policies' state mounts, the
// worktree's ephemeral scratch home). Applied AFTER chainFor so the chain
// construction — and Resolve, which only needs policy identity — stays
// state-free. Policies are value types; the stamped copies replace the
// originals in place.
func withSessionState(chain []policy, state SessionState) []policy {
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

// resolveAndBind is the one implementation of the composed resolve-then-mount
// step every policy.PrepareWorkspace delegates to. It lives here, over the
// interface, rather than being written out on each policy: a composition of two
// interface methods is not per-implementation behaviour, and three copies of it
// would be three places for the unwind below to drift.
//
// The unwind is the part worth stating: once ResolveWorkspace returns, the
// workspace OWNS whatever was created for it (the container scratch tree, a
// freshly added checkout), so a mount failure must Cleanup() rather than leave
// the caller a nil workspace and an orphaned resource.
func resolveAndBind(ctx context.Context, p policy, projectDir, agentID string) (workspace, error) {
	ws, err := p.resolveWorkspace(ctx, projectDir, agentID)
	if err != nil {
		return nil, err
	}
	if _, err := p.bind(ctx, ws); err != nil {
		_ = ws.Cleanup()
		return nil, err
	}
	return ws, nil
}

// refuseLostContainer records a container that could not start as the
// non-degradable finding the run is refused on — for a run (prepareChain)
// and for its preview (Preview), through this one call. A refusal that names
// its own fix is more specific than the generic image/runtime remedy, and
// wins.
func refuseLostContainer(err error, agentID string, requested RuntimeAxis) {
	remedy, ok := clifmt.RemedyOf(err)
	if !ok {
		remedy = isolationRemedy
	}
	strictness.FailAlways(report.KindIsolation, remedy,
		"container isolation was requested but could not start — refusing to run %q on the HOST without the container boundary that was asked for (this session would NOT be sandboxed): %v%s", agentID, err, containerSelectionHint(requested))
}

// prepareChain tries each policy's PrepareWorkspace in order and returns the first
// that succeeds with its workspace, warning at each degrade. The chain always ends
// in None (which never fails), so a member always gets a workspace; the trailing
// fallback is defensive against an empty/all-failing chain.
func prepareChain(ctx context.Context, chain []policy, requested RuntimeAxis, projectDir, agentID string) (policy, workspace) {
	for i, p := range chain {
		ws, err := p.prepareWorkspace(ctx, projectDir, agentID)
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
		// (strictness.FailAlways wraps clidiag.Warn), so a failed or denied
		// container start can't be mistaken for a normal host run, and the choke
		// owner aborts on it before the unsandboxed engine launches in BOTH
		// modes. --degraded no longer walks this chain out to the host: a
		// requested boundary that cannot be provided is refused, because the
		// launch is the exposure. The runtime-unreachable reason is recorded
		// earlier in chainFor; a container that never reached running surfaces
		// at the owner run's starter (AwaitContainerRunning). The `continue` is
		// unchanged — the chain still walks to
		// None so the WORKSPACE resolution has an answer to return; what stops
		// the run is the non-degradable finding, not a missing workspace.
		if IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next) {
			refuseLostContainer(err, agentID, requested)
			continue
		}
		clidiag.Warn("ctxloom", "isolation %q unavailable for member %q (%v); degrading to %q", p.Name(), agentID, err, next)
	}
	ws, _ := None{}.prepareWorkspace(ctx, projectDir, agentID)
	return None{}, ws
}
