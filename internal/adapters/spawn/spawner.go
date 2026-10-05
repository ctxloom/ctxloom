// Package spawn (adapter) implements coord.Spawner over launch.Resolve and
// the runner starters: it SELECTS the binding, RESOLVES the launch and STARTS
// the runner — on the host or as a container's foreground process. It lives
// in the originator and is composed at cmd/*.
package spawn

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// StarterFunc is the runner-process seam: the runner-process seam, called
// once per spawn with the backend and the per-spawn runner env.
type StarterFunc func(backend string, runnerEnv map[string]string) func(context.Context) (*isolation.RunnerHandle, error)

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

// delegatedChildren is backend's engine.Definition.DelegatedChildren
// declaration: whether delegated children may run on it at all, and how a
// one-shot child resumes. Every child's engine control rides the StartRun
// path (spawn the runner process, await its dial-home, issue StartRun on its
// RunnerChannel), and the runner-side EngineHost drives every engine through
// its instance's driver, never a backend name — so admission is a REVIEW
// verdict the engine declares (the per-backend delta from StartRun was
// checked empty), not "has a structured driver", and a new engine is
// reviewed onto delegation explicitly rather than swept in. The second
// result is false for an unregistered backend or one that declares no
// delegated children; the string is its declared reason, when it gave one.
func delegatedChildren(reg engine.Registry, backend string) (engine.DelegatedChildren, bool, string) {
	e, ok := reg.Lookup(engine.Name(backend))
	if !ok {
		return engine.DelegatedChildren{}, false, ""
	}
	decl := e.Root().DelegatedChildren
	d, admitted := decl.Get()
	return d, admitted, decl.AbsentReason()
}

// delegatingEngines lists, sorted, the registered engines keep accepts among
// those that admit delegated children, for error messages that enumerate a
// gate's membership.
func delegatingEngines(reg engine.Registry, keep func(engine.DelegatedChildren) bool) string {
	names := reg.Names(func(d engine.Definition) bool {
		dc, ok := d.DelegatedChildren.Get()
		return ok && keep(dc)
	})
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return strings.Join(out, ", ")
}

// admit is Resolve's backend gate: checkStartRunAllowlist, nothing else. A
// Starter (Options.Starter) changes HOW an admitted backend's runner is
// stood up — in-process double instead of `ctxloom runner` — never WHETHER
// it is admitted.
func (s *spawner) admit(backend string) error {
	return checkStartRunAllowlist(s.app.Engines(), backend)
}

// checkStartRunAllowlist refuses a delegated spawn whose backend's engine
// does not admit delegated children: an engine that declares them absent, or
// a config-declared llm type that matches no engine, fails loud here at
// Resolve time (ctxloom never silently no-ops).
func checkStartRunAllowlist(reg engine.Registry, backend string) error {
	_, admitted, reason := delegatedChildren(reg, backend)
	if admitted {
		return nil
	}
	if reason != "" {
		reason = " (" + reason + ")"
	}
	return fmt.Errorf(
		"backend %q cannot run delegated children%s: delegated children run runner-side via StartRun, and only reviewed engines are admitted (engines: %s)",
		backend, reason, delegatingEngines(reg, func(engine.DelegatedChildren) bool { return true }))
}

// resolveResumeMode is the per-engine resume-capability gate: `driving:
// oneshot` requires an engine that declares ResumesByKey. A conversational
// (or empty/default) driving value always resolves to
// coord.ResumeModePersistent.
//
// An oneshot agent on an INCAPABLE backend FAILS LOUD here rather than
// silently downgrading to persistent — per isolation-must-not-negotiate,
// silently running a "oneshot" agent conversationally because the engine
// can't actually resume would be exactly the class of silent, behavior-
// changing divergence this project bans; the caller asked for one thing and
// would silently get another with no error raised. A resume-capable engine
// that does not admit delegated children is incapable here too: its
// declaration carries no ResumesByKey to read.
func resolveResumeMode(reg engine.Registry, driving agents.DrivingMode, backend string) (coord.ResumeMode, error) {
	if driving != agents.DrivingOneshot {
		return coord.ResumeModePersistent, nil
	}
	if d, admitted, _ := delegatedChildren(reg, backend); !admitted || !d.ResumesByKey {
		return coord.ResumeModePersistent, fmt.Errorf(
			"driving: oneshot requires a resume-capable engine; backend %q has no resume-by-key primitive (known resume-capable: %s)",
			backend, delegatingEngines(reg, func(d engine.DelegatedChildren) bool { return d.ResumesByKey }))
	}
	return coord.ResumeModeOneShot, nil
}

// spawnGeneration is the ONE Reload a spawn performs: an agent definition
// edited mid-session takes effect on the NEXT spawn and never mid-session.
// A reload that fails (a transient read problem, a concurrent partial write)
// must not break spawning, so it degrades — with a warning — to the
// generation already published, which is complete and consistent.
//
// The child decides with the same trust posture as the session that spawns
// it: a session that waived the signature check delegates waived (owner
// ruling 2026-10-02), and the child's launch hands its own hooks the waiver
// (launch.Resolve) and records it on the child's session (ResolveLaunch).
func (s *spawner) spawnGeneration(ctx context.Context) (*config.Snapshot, error) {
	snap, err := s.app.Reload(ctx)
	if err == nil {
		return snap, nil
	}
	s.rep.Warnf("agent_run: reload configuration for agent resolution: %v (using the published generation)", err)
	return s.app.Snapshot(ctx)
}

// createAgentFix is the one next command an agent_run on an undeclared name
// is told to run: a command the CLI has, so the model can act on it.
const createAgentFix = "create it with `ctxloom agent create <name> --profiles <profile>`"

func (s *spawner) Resolve(ctx context.Context, agentName string) (*coord.SpawnPlan, error) {
	snap, err := s.spawnGeneration(ctx)
	if err != nil {
		return nil, err
	}
	cfg := snap.Config
	binding, ok := cfg.Agent(agentName)
	if !ok {
		return nil, report.Errorf(createAgentFix, "agent_run: %w: %q", launch.ErrNoAgent, agentName)
	}
	// The binding's DECLARED engine, for the roster and the reach-back
	// endpoint: the label it names, else the project primary. The launch
	// resolves the same precedence and is authoritative once it exists.
	label := binding.LLM
	if label == "" {
		label = cfg.PrimaryLabel()
	}
	backend, _ := operations.ResolveBackend(s.app.Engines(), cfg, label)
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
	resumeMode, err := resolveSpawnResumeMode(s.app.Engines(), agentName, binding.Driving, backend)
	if err != nil {
		return nil, err
	}
	if err := s.admit(backend); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}

	plan := &coord.SpawnPlan{
		AgentName: agentName,
		Backend:   backend,
		Label:     label,
		Profiles:  binding.Profiles,
		Runtime:   runtime,
		// The roster's name for the posture the launch will resolve to, in
		// the engine's display words; the launch is authoritative once it
		// exists.
		Permission:  operations.EffectivePosture(s.app.Engines(), backend, binding.Permissions, labelPermissions(cfg, label)).Label,
		MayDelegate: slices.Clone(binding.MayDelegate),
		ResumeMode:  resumeMode,
		Snapshot:    snap,
	}
	// coord.Resolved once here (not per StartEngine call) so the enqueue journal
	// and the launch see the IDENTICAL composed set.
	plan.MCPServers = s.childMCPServers(plan)
	return plan, nil
}

// labelPermissions is the label's permissions block; none for a label the
// config does not declare.
func labelPermissions(cfg *config.Config, label string) agents.LabelPermissions {
	entry, _ := cfg.GetLLMEntry(label)
	return entry.Permissions
}

// resolveSpawnResumeMode is resolveResumeMode with the agent named. It FAILS
// LOUD (never silently downgrades to persistent) when `driving: oneshot`
// names a backend with no resume-by-key primitive. A one-shot child must
// ALSO pass the delegation gate (admit); both read the one declaration.
func resolveSpawnResumeMode(reg engine.Registry, agentName string, driving agents.DrivingMode, backend string) (coord.ResumeMode, error) {
	resumeMode, err := resolveResumeMode(reg, driving, backend)
	if err != nil {
		return 0, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}
	return resumeMode, nil
}

func (s *spawner) AssignSession(projectDir, backend string) (string, error) {
	cfg, err := s.app.Config(context.Background())
	if err != nil {
		return "", err
	}
	base, err := operations.OutputBase(cfg)
	if err != nil {
		return "", err
	}
	entry, err := operations.AssignSessionHarp(projectDir, backend, base)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *spawner) RecordEngineVersion(ctx context.Context, harp, backend string) {
	s.app.RecordSessionEngineVersion(ctx, harp, backend)
}

// startEngine is operations.StartEngine's production entry point, indirected
// so a test can observe the launch each spawn resolves without a real
// runner process.
var startEngine = operations.StartEngine

// ResolveLaunch resolves the child's launch (childSource) against the
// spawn's generation.
func (s *spawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	deps, err := operations.LaunchDepsFor(s.app.LaunchFacts(), plan.Snapshot)
	if err != nil {
		return coord.Resolved{}, err
	}
	s.stampChild(deps.Sessions, start.Identity.Harp, plan.Snapshot.Trust)
	l, err := launch.Resolve(ctx, deps.ForSession(start.Identity.Harp), childSource(plan, start, s.projectDir))
	if err != nil {
		return coord.Resolved{}, err
	}
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

// stampChild records on a delegated child's session what its mint knew: that
// it is an agent's, and whether it decides with the signature check waived —
// so a child that ran waived can be told apart later. A failed stamp warns:
// an unstamped session reads as a human's, which a sweep never purges.
func (s *spawner) stampChild(store sessions.Store, harp string, tr composite.Trust) {
	if err := store.StampMint(harp, sessions.MintStamp{Origin: sessions.OriginAgent, SigCheckDisabled: tr.SignatureCheckDisabled()}); err != nil {
		s.rep.Warnf("session %s: cannot record its origin and signature-check posture: %v", harp, err)
	}
}

// childSource is what a delegated child's launch is asked from: the plan's
// selection, the coordinator's identity and first turn, and the resume arm
// on a resume — never its permissions, which are its own
// binding's. A child
// defaults to its OWN worktree when neither the call nor the project chose a
// workspace: needing a private cwd is a property of how the parent fans,
// and the shared checkout is never the silent default for a child.
func childSource(plan *coord.SpawnPlan, start coord.SpawnStart, projectDir string) launch.Source {
	workspace := plan.Workspace
	if workspace == "" && plan.Snapshot.Config.GetWorkspace() == "" {
		workspace = launch.WorkspaceWorktree
	}
	src := launch.Source{
		Identity:  start.Identity,
		Agent:     plan.AgentName,
		Mode:      launch.StructuredMode(),
		Prompt:    start.Prompt,
		WorkDir:   projectDir,
		Workspace: workspace,
		DirtyTree: plan.DirtyTreeHandler,
	}
	if start.Resumed {
		src.Resume = launch.Resume{Ref: sessions.ResumeRef{Harp: start.Identity.Harp, NativeKey: start.ResumeKey}}
	}
	return src
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
	var starter func(context.Context) (*isolation.RunnerHandle, error)
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
	entries, err := operations.RecordedSessionEntries(harp)
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

func (s *spawner) BindNativeSession(harp, key string) {
	if err := operations.BindSession(harp, key, ""); err != nil {
		s.rep.Warnf("agent %s: bind native session key: %v", harp, err)
	}
}

func (s *spawner) NativeSession(harp string) string {
	entry, err := operations.GetSession(harp)
	if err != nil {
		s.rep.Warnf("agent resume %s: read session entry: %v (resuming without a native session key)", harp, err)
		return ""
	}
	if entry == nil {
		return ""
	}
	return entry.SessionID
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
// agent_send and no agent_report, so it launches, consumes its
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
	rep.Warnf("agent_run: agent %q composed no %q MCP server (ctxloom's own loadout server is withheld for this project — check `exclude_mcp` on its profiles, whether the item is rejected, and that companions are not disabled); the child launches WITHOUT agent_send/agent_report — it cannot report back or be steered",
		agentName, agent.MCPServerName)
}

// childVerbosity is the verbosity handed to the child's runner starter
// (the environment's Start): above 0 a container runner reports its
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
