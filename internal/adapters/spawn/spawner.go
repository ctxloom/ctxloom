// Package spawn (adapter) implements coord.Spawner over launch.Resolve and
// the runner starters: it SELECTS the binding, RESOLVES the launch and STARTS
// the runner — on the host or as a container's foreground process. It lives
// in the originator and is composed at cmd/*.
package spawn

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// StarterFunc is the runner-process seam: the runner-process seam, called
// once per spawn with the backend and the per-spawn runner env.
type StarterFunc func(backend string, runnerEnv map[string]string) isolation.EngineStarter

// spawner is the production Spawner over the operations launch trunk. It
// holds the process's App — the one config.Owner — and captures ONE
// generation per spawn (Resolve's Reload), which the plan then carries to
// StartEngine's resolution.
type spawner struct {
	rep        report.Reporter
	app        *operations.App
	projectDir string
	starter    StarterFunc // test seam; nil = production (the cell's transport starts the runner)
}

// New builds the production coord.Spawner over the process's App. rep is
// the sink the composition root hands every long-lived component.
func New(rep report.Sink, app *operations.App, projectDir string, starter StarterFunc) coord.Spawner {
	return &spawner{rep: report.To(rep), app: app, projectDir: projectDir, starter: starter}
}

// viaStartRunBackends is the delegation allowlist: the set of backend types
// that may run delegated children at all. Every child's engine control rides
// the StartRun path (spawn the runner process, await its dial-home, issue
// StartRun on its RunnerChannel). The runner-side EngineHost drives every
// engine through its instance's driver (engine.Instance.Drivers), never a
// backend name, so every member of this set gets the identical
// StartRun/adaptation/resume machinery; the per-backend deltas are in model
// delivery.
//
// Any backend NOT in this set is refused at Resolve (checkStartRunAllowlist)
// — this gate is deliberately an allowlist of VERIFIED backends, not
// "has a structured driver", so a new backend must be reviewed onto
// StartRun explicitly rather than swept in.
//
// The bar for admitting one is a per-backend recon showing that delta is
// empty. What makes it empty generally: the runner-side standup
// (internal/adapters/cli's standUpRunner), the isolation starter
// (`ctxloom runner <backend>`) and the launch codec
// (coordgrpc.EncodeLaunch) never name a backend at all, and the runner
// delivers the package through the engine's own Setup (runner.Execute), the
// same writers every host launch goes through.
//
// TODO(slice 11b): these three tables are the last name-keyed capability
// declarations in core. They read Instance.Resume(key) — real or refused —
// once the instance half of the engine port lands; until then the engine's
// name is spelled here, and the no-engine-name-in-core gate allows it by
// that slice.
var viaStartRunBackends = map[string]bool{
	"claude-code": true,
	// mock is reviewed onto StartRun because the binary can HOST it: `ctxloom
	// runner mock` stands up a real runner around the deterministic echo, so
	// a mock child is a driveable run, not a run nothing can answer. That is
	// what the acceptance journeys rely on when they delegate through a real
	// ctxloom binary. It is safe in a user's binary for the same reason it was
	// before the spool cutover: no credentials, no network, an echo.
	config.BackendMock: true,
}

// admit is Resolve's backend gate: checkStartRunAllowlist, nothing else. A
// Starter (Options.Starter) changes HOW an admitted backend's runner is
// stood up — in-process double instead of `ctxloom runner` — never WHETHER
// it is admitted. An earlier shape special-cased mock here on the premise
// that it had no runner process of its own; that was false (the binary hosts
// it), so mock is on the allowlist and the special case is gone.
func (s *spawner) admit(backend string) error {
	return checkStartRunAllowlist(backend)
}

// checkStartRunAllowlist refuses a delegated spawn whose backend has not been
// reviewed onto the StartRun path: a newly registered backend, or a
// config-declared llm type that matches no backend, fails loud here at
// Resolve time (ctxloom never silently no-ops).
func checkStartRunAllowlist(backend string) error {
	if viaStartRunBackends[backend] {
		return nil
	}
	return fmt.Errorf(
		"backend %q cannot run delegated children: delegated children run runner-side via StartRun, and only reviewed backends are admitted (backends: %s)",
		backend, strings.Join(backendNames(viaStartRunBackends), ", "))
}

// resumeCapableBackends is the one-shot-resume plan's Slice 2 / Fork 3
// STATIC gating table: which backends have a cheap resume-by-key primitive
// at all, independent of viaStartRunBackends (that table is about WHICH
// wire path a child's Chat rides; this one is about whether ASKING an
// already-ended engine to continue its own native session is even possible).
//
//   - claude-code: resume by asking the engine to load its own prior
//     session — LIVE-gated a second time on
//     the adapter's advertised loadSession capability once Slice 4 records it
//     from the first StartRunResult/init (see coord.SpawnPlan.ResumeMode's doc);
//     this table is the STATIC half alone.
//   - a MIGRATED backend is not automatically resume-capable: the two tables
//     answer different questions, and one that neither consumes
//     engine.Turn.Resume nor emits a native session-id Session event
//     stays FALSE here and re-primes from rendered history instead
//     (the rendered-history lead, ResumeHistory), over StartRun all the same.
//   - mock (tests) and any unlisted/future backend: FALSE — an allowlist,
//     exactly like viaStartRunBackends, so a new backend is reviewed onto
//     resume explicitly rather than swept in by having a structured driver.
var resumeCapableBackends = map[string]bool{
	"claude-code": true,
}

// oneShotSupportedBackends is the set of backends whose driving:oneshot turn
// loop is wired END TO END in this release (one-shot-resume plan, Slice 4): the
// MIGRATED (viaStartRunBackends), resume-capable engines whose LIVE loadSession
// capability the coordinator confirms before tearing an engine down at a turn
// boundary (children.go's oneShotReady) and then resumes by native session key
// via StartRun{ResumeSessionId} → ACP session/load. That is the intersection of
// viaStartRunBackends and resumeCapableBackends.
//
// A backend in neither table never reaches the gate at all: resolveResumeMode
// already fails it loud on the capability reason.
var oneShotSupportedBackends = map[string]bool{
	"claude-code": true,
}

// resolveResumeMode is the per-engine resume-capability gate (Fork 3's
// STATIC half): `driving: oneshot` requires a backend with a cheap
// resume-by-key primitive. A conversational (or empty/default) driving
// value always resolves to coord.ResumeModePersistent — the identical behavior
// every agent gets today, byte-for-byte.
//
// An oneshot agent on an INCAPABLE backend FAILS LOUD here rather than
// silently downgrading to persistent — per isolation-must-not-negotiate,
// silently running a "oneshot" agent conversationally because the engine
// can't actually resume would be exactly the class of silent, behavior-
// changing divergence this project bans; the caller asked for one thing and
// would silently get another with no error raised.
//
// This function does NOT itself decide whether coord.ResumeModeOneShot may be
// acted on in THIS release — that additional (and, right now, universal)
// gate lives in Resolve(), clearly separated so it can be deleted alone the
// day Slice 4 (the turn loop) lands, without touching this capability table.
func resolveResumeMode(driving agents.DrivingMode, backend string) (coord.ResumeMode, error) {
	if driving != agents.DrivingOneshot {
		return coord.ResumeModePersistent, nil
	}
	if !resumeCapableBackends[backend] {
		return coord.ResumeModePersistent, fmt.Errorf(
			"driving: oneshot requires a resume-capable engine; backend %q has no resume-by-key primitive (known resume-capable: %s)",
			backend, resumeCapableBackendNames())
	}
	return coord.ResumeModeOneShot, nil
}

// resumeCapableBackendNames lists resumeCapableBackends' keys, sorted, for
// the resolveResumeMode error message.
func resumeCapableBackendNames() []string {
	return backendNames(resumeCapableBackends)
}

// backendNames lists a backend table's true-valued keys, sorted, for error
// messages that enumerate a gate's membership.
func backendNames(table map[string]bool) []string {
	names := make([]string, 0, len(table))
	for name, ok := range table {
		if ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// spawnGeneration is the ONE Reload a spawn performs: an agent definition
// edited mid-session takes effect on the NEXT spawn and never mid-session.
// A reload that fails (a transient read problem, a concurrent partial write)
// must not break spawning, so it degrades — with a warning — to the
// generation already published, which is complete and consistent.
func (s *spawner) spawnGeneration(ctx context.Context) (*config.Snapshot, error) {
	snap, err := s.app.Reload(ctx)
	if err == nil {
		return snap, nil
	}
	s.rep.Warnf("agent_run: reload configuration for agent resolution: %v (using the published generation)", err)
	return s.app.Snapshot(ctx)
}

func (s *spawner) Resolve(ctx context.Context, agentName string) (*coord.SpawnPlan, error) {
	snap, err := s.spawnGeneration(ctx)
	if err != nil {
		return nil, err
	}
	cfg := snap.Config
	binding, ok := cfg.Agent(agentName)
	if !ok {
		return nil, fmt.Errorf("agent_run: %w: %q (declare it with `ctxloom agent set %s`)", launch.ErrNoAgent, agentName, agentName)
	}
	// The binding's DECLARED engine, for the roster and the reach-back
	// endpoint: the label it names, else the project primary. The launch
	// resolves the same precedence and is authoritative once it exists.
	label := binding.LLM
	if label == "" {
		label = cfg.PrimaryLabel()
	}
	backend, _ := operations.ResolveBackend(cfg, label)
	rtStr := binding.Runtime
	if rtStr == "" {
		rtStr = cfg.GetRuntime()
	}
	runtime, err := launch.ParseRuntimeAxis(rtStr)
	if err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}
	if err := agents.ValidateDriving(binding.Driving); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}
	// The per-engine resume-capability gate. FAILS LOUD (never silently
	// downgrades to persistent) when `driving: oneshot` names a backend with
	// no resume-by-key primitive.
	resumeMode, rmErr := resolveResumeMode(binding.Driving, backend)
	if rmErr != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, rmErr)
	}
	// A backend that is statically resume-capable but NOT yet wired end to
	// end still fails loud here rather than resolving a coord.ResumeModeOneShot
	// value the turn loop would silently run conversationally.
	if resumeMode == coord.ResumeModeOneShot && !oneShotSupportedBackends[backend] {
		return nil, fmt.Errorf(
			"agent_run: agent %q: driving: oneshot is not yet available for backend %q in this release; it is resume-capable but ctxloom does not yet tear down/resume THIS engine at turn boundaries",
			agentName, backend)
	}
	if err := s.admit(backend); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}

	plan := &coord.SpawnPlan{
		AgentName:  agentName,
		Backend:    backend,
		Label:      label,
		Profiles:   binding.Profiles,
		Runtime:    runtime,
		Permission: binding.Permissions,
		ResumeMode: resumeMode,
		Snapshot:   snap,
	}
	// coord.Resolved once here (not per StartEngine call) so the enqueue journal
	// and the launch see the IDENTICAL composed set.
	plan.MCPServers = s.childMCPServers(plan)
	return plan, nil
}

func (s *spawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSessionHarp(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *spawner) RecordEngineVersion(ctx context.Context, harp, backend string) {
	operations.RecordSessionEngineVersion(ctx, harp, backend)
}

// startEngine is operations.StartEngine's production entry point, indirected
// so a test can observe the launch each spawn resolves without a real
// runner process.
var startEngine = operations.StartEngine

// ResolveLaunch resolves the child's launch against the spawn's generation.
// A delegated child defaults to its OWN worktree when neither the call nor
// the project chose a workspace: needing a private cwd is a property of how
// the parent fans, and the shared checkout is never the silent default for a
// child.
func (s *spawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	deps, err := operations.LaunchDepsFor(plan.Snapshot, s.app.Strictness)
	if err != nil {
		return coord.Resolved{}, err
	}
	workspace := plan.Workspace
	if workspace == "" && plan.Snapshot.Config.GetWorkspace() == "" {
		workspace = launch.WorkspaceWorktree
	}
	src := launch.Source{
		Identity:  start.Identity,
		Agent:     plan.AgentName,
		Mode:      launch.StructuredMode(),
		Prompt:    start.Prompt,
		WorkDir:   s.projectDir,
		Workspace: workspace,
		DirtyTree: plan.DirtyTreeHandler,
	}
	if start.Resumed || start.Rebind {
		src.Resume = launch.Resume{Ref: sessions.ResumeRef{Harp: start.Identity.Harp, NativeKey: start.ResumeKey}, RebindEndpoint: start.Rebind}
	}
	l, err := launch.Resolve(ctx, operations.ForSession(deps, start.Identity.Harp), src)
	if err != nil {
		return coord.Resolved{}, err
	}
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

// Start starts the runner for a resolved launch through StartRunner over the
// cell's transport, with the reach-back trio on the runner's env.
func (s *spawner) Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*coord.EngineSpawn, error) {
	h, err := StartRunner(ctx, cellRuntime{starter: s.starter}, l, reach)
	if err != nil {
		return nil, err
	}
	return &coord.EngineSpawn{Kill: h.Kill, StderrTail: h.StderrTail, Wait: h.Wait}, nil
}

// cellRuntime is Runtimes over the cell's transport: the isolation starter
// the launch's cell handle names (docker-direct for a container cell, the
// bare self-invoked runner for a host cell), or the test seam's. Its Start
// returns once the runner PROCESS is up: that is attach, after which the
// process is the run record's and the ctx says nothing about it.
type cellRuntime struct{ starter StarterFunc }

func (r cellRuntime) Start(ctx context.Context, l launch.Launch, env map[string]string) (coord.RunnerHandle, error) {
	var starter isolation.EngineStarter
	if r.starter != nil {
		starter = r.starter(string(l.Engine), env)
	}
	proc, err := startEngine(ctx, l, env, childVerbosity(), starter)
	if err != nil {
		return coord.RunnerHandle{}, err
	}
	return coord.RunnerHandle{Kill: proc.Kill, Wait: proc.Wait, StderrTail: proc.StderrTail}, nil
}


func (s *spawner) ResumeHistory(ctx context.Context, harp string) string {
	entries, err := operations.RecordedSessionEntries(ctx, harp)
	if err != nil {
		s.rep.Warnf("agent resume %s: no recorded history to prime (%v); resuming with the agent context only", harp, err)
		return ""
	}
	rendered := operations.RenderResumedTranscript(harp, entries)
	// RenderResumedTranscript legitimately returns "" for zero substantive
	// entries — indistinguishable, from the outside, from "loaded the whole
	// conversation" — so a resume that primes with no history says so.
	if rendered == "" {
		s.rep.Warnf("agent resume %s: no recorded history to prime (transcript rendered empty); resuming with the agent context only", harp)
	}
	return rendered
}

func (s *spawner) MarkSessionEnded(harp string) {
	if err := operations.EndSession(harp, time.Now()); err != nil {
		s.rep.Warnf("agent %s: end session: %v", harp, err)
	}
}

// childMCPServers composes the child session's managed MCP set — the same
// sources Setup's settings write reconciles, scoped to the child's profile
// set (the chat substrate never runs Setup). Unlike the pre-coordinator
// orchestrator, NOTHING is stamped into the ctxloom entry's Env map: the
// child's ambient identity and coordinator reach-back ride the HARNESS
// process env only (the engine's MCP subprocesses inherit it), and the
// credential must never land in an MCP config structure.
func (s *spawner) childMCPServers(plan *coord.SpawnPlan) []agent.ChatMCPServer {
	// Composed ONCE, here at Resolve time, and never recomposed downstream:
	// the MCPServers field doc states a "resolved exactly once" invariant, and
	// recomposing would re-run ResolveBundleMCPServers and re-fire
	// WarnWithheld a second time. Nothing later needs to: the ctxloom entry's
	// command is the bare executable (agent.CtxloomCommand), which resolves on
	// PATH wherever the child runs — including inside a container, where the
	// isolation policy is not even known at this point.
	servers := agent.ComposeChatMCPServers(plan.Snapshot.Config.ResolveBundleMCPServers(plan.Profiles), nil)
	operations.WarnWithheldBy(plan.Snapshot.Config.ExecutableTrustGate())
	warnNoReachBack(s.rep, plan.AgentName, servers)
	return servers
}

// warnNoReachBack reports a composed child MCP set with no ctxloom server in it.
//
// That set is the child's ONLY coordination surface: without it the child has no
// agent_send, no agent_recv and no agent_report, so it launches, consumes its
// budget, and can never answer its parent or be steered — the stranding shape
// spawnReachURL fails loud on when the fault is an unreachable endpoint. Here the
// cause is configuration (ctxloom's own companion loadout's `ctxloom` server
// withheld — a profile's `exclude_mcp: [ctxloom]`, the item rejected, or the
// self-probe disabled — which composes a set carrying no ctxloom entry, nil
// when nothing else is registered either), so it warns rather than refusing:
// withholding it is a deliberate project choice and mirrors the documented
// degraded no-reach-back posture. What it must not be is SILENT.
func warnNoReachBack(rep report.Reporter, agentName string, servers []agent.ChatMCPServer) {
	for _, srv := range servers {
		if srv.Name == agent.MCPServerName {
			return
		}
	}
	rep.Warnf("agent_run: agent %q composed no %q MCP server (ctxloom's own loadout server is withheld for this project — check `exclude_mcp` on its profiles, whether the item is rejected, and that companions are not disabled); the child launches WITHOUT agent_send/agent_recv/agent_report — it cannot report back or be steered",
		agentName, agent.MCPServerName)
}

// childVerbosity is the verbosity handed to the child's runner starter
// (isolation.StarterForWorkspace): above 0 a container runner reports its
// auth route, and a host runner reads the same variable itself. The
// coordinator often lives in a flagless `ctxloom mcp` process, so the knob is
// env-only: CTXLOOM_VERBOSE (the existing process-wide verbose
// switch) turns the trail on at trace. It is read through envswitch so this
// site cannot disagree with the binary's own reading of the same variable —
// a switch that is on for logging and off for child diagnostics is worse than
// one that is off everywhere. An unparseable value is reported once, by the
// binary's startup read, not again per spawn.
func childVerbosity() int {
	if on, _ := envswitch.On("CTXLOOM_VERBOSE"); on {
		return 3
	}
	return 0
}
