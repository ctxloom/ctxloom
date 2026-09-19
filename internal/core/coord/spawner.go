package coord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
)

// SpawnPlan is a delegated child's launch as the coordinator holds it: the
// SELECTION made at the verb — the binding's name, the engine and label it
// declares, its composed MCP set, its resume mode — and, once StartEngine
// has run under the child's launch context, the RESOLVED Launch. Everything
// the journal and the roster read before the launch is here; everything the
// runner is handed comes off the Launch.
type SpawnPlan struct {
	AgentName string
	// Backend and Label are the binding's DECLARED engine, read at the verb
	// so the roster and the reach-back endpoint know the runtime before the
	// launch resolves; the Launch is authoritative once it exists.
	Backend  string
	Label    string
	Profiles []string
	Runtime  launch.RuntimeAxis
	// Workspace and DirtyTreeHandler are the caller's per-invocation
	// orchestration traits (agent_run's own fields), stamped by AgentRun
	// before the launch resolves; a binding carries no opinion on either.
	Workspace        launch.WorkspaceAxis
	DirtyTreeHandler launch.DirtyTreeHandler
	// Permission is the posture the plan was enqueued with: the binding's
	// declaration (empty when it declared none), or an owner run's floored
	// posture. The launch resolver decides the effective one.
	Permission string
	Degraded   []string
	// MCPServers is the child's composed MCP server set, resolved once at
	// the verb so a later config edit cannot retroactively change a live
	// run's privileges and so the journal, the launch and StartEngine read
	// one set.
	MCPServers []agent.ChatMCPServer
	// ResumeMode is the per-engine resume-capability gate's outcome:
	// ResumeModeOneShot only when the binding declared driving: oneshot AND
	// the engine has a resume-by-key primitive the turn loop is wired for.
	ResumeMode ResumeMode
	// Launch is the resolved launch, set by StartEngine: the cell, the
	// package, the plan, the endpoint, the permission floored once.
	Launch launch.Launch

	// cfg is the generation this spawn was selected from; StartEngine
	// resolves against the same generation, so one spawn observes one state.
	snap *config.Snapshot
}

// ResumeMode is a resolved agent's per-turn engine-lifecycle mode: whether
// its engine process persists across turns — the default, and what every
// conversational agent gets — or tears down at each turn boundary to be
// resumed by native session key on the next mailbox delivery.
type ResumeMode int

const (
	// ResumeModePersistent is the zero value: the engine process stays warm
	// across turns.
	ResumeModePersistent ResumeMode = iota
	// ResumeModeOneShot is the turn-boundary teardown+resume-by-key model,
	// LIVE for the wired backends. Reaching this value requires BOTH a
	// `driving: oneshot` agent declaration and a statically resume-capable
	// backend (resolveResumeMode); Resolve narrows it once more to the
	// backends whose turn loop is wired end to end (oneShotSupportedBackends)
	// and fails loud for any other resume-capable one.
	ResumeModeOneShot
)

// SpawnStart is what StartEngine resolves the child's launch from beyond
// the plan: the child's minted identity and, on a resume, the native key
// the journal holds.
type SpawnStart struct {
	Identity sessions.Identity
	// ResumeKey is the journaled native session id of the run being resumed;
	// empty on a fresh spawn. A resume reuses the harp's endpoint.
	ResumeKey string
	// Resumed says this is a resume even when no native key survived, so
	// the launch re-resolves the same harp.
	Resumed bool
}

// Spawner is the coordinator's launch seam: production selects, resolves
// and spawns real engines through the operations launch trunk; tests fake
// children without config or engines.
type Spawner interface {
	// Resolve SELECTS the binding by name against one fresh generation:
	// the binding must exist and its engine be admitted to delegation; the
	// composed MCP set and the resume mode are decided here. The launch
	// itself resolves in StartEngine, under the child's launch context.
	Resolve(ctx context.Context, agentName string) (*SpawnPlan, error)
	// AssignSession mints the child's harp (its address and continuation
	// token) in the host-side session accounting, recording the engine the
	// selection named (the launch's resolver confirms it), and does NOTHING
	// ELSE.
	//
	// It is on the pre-registration critical path: AgentRun cannot journal
	// run.enqueued, and therefore cannot show the caller that the child
	// exists, until it has an address for it. Anything slow that a
	// starting session also wants — the engine-version probe above all —
	// belongs in RecordEngineVersion, which runs AFTER the run is
	// registered. See task affected-yearly: a probe sitting here left a
	// spawn invisible for minutes, and callers reasonably double-spawned.
	AssignSession(projectDir, backend string) (string, error)
	// RecordEngineVersion probes the child engine's installed CLI and
	// records its version against harp, for the reader selection that
	// happens when this session's transcript is read back.
	//
	// Called from a tracked goroutine AFTER the run is registered, and
	// therefore explicitly NOT on the path that decides whether agent_run
	// returns. It reports nothing: every failure here is a diagnostic that
	// warns at its own level and costs the session nothing (see
	// operations.RecordSessionEngineVersion), exactly like MarkSessionEnded
	// below.
	RecordEngineVersion(ctx context.Context, harp, backend string)
	// StartEngine RESOLVES the child's launch (the one resolver: the cell
	// prepared, the permission floored, the endpoint minted or reused) and
	// spawns its runner process with the coordinator trio on the runner's
	// env, WITHOUT attaching. Engine control then arrives over the runner's
	// own RunnerChannel (StartRun), built from the returned EngineSpawn. On
	// success plan.Launch is set.
	StartEngine(ctx context.Context, plan *SpawnPlan, start SpawnStart, runnerEnv map[string]string) (*EngineSpawn, error)
	// ResumeContext composes the context for a RESUMED harp: the launch's
	// context plus the rendered recorded history when one is loadable.
	ResumeContext(ctx context.Context, contextText, harp string) string
	// MarkSessionEnded ends the harp's session: it stamps it ended in session
	// accounting AND (in production, via operations.EndSession) removes the
	// child's per-session engine-home instance, credential copy and all, from
	// the project tree. A test double that does neither is fine; a production
	// implementation that only stamps leaves a credential on disk per child.
	MarkSessionEnded(harp string)
}

// StarterFunc is Options.Starter's shape: the runner-process seam, called
// once per spawn with the backend and the per-spawn runner env.
type StarterFunc func(backend string, runnerEnv map[string]string) isolation.EngineStarter

// prodSpawner is the production Spawner over the operations launch trunk. It
// holds the process's App — the one config.Owner — and captures ONE
// generation per spawn (Resolve's Reload), which the plan then carries to
// StartEngine's resolution.
type prodSpawner struct {
	app        *operations.App
	projectDir string
	starter    StarterFunc // test seam; nil = production (the cell's transport starts the runner)
}

// newProdSpawner builds the production spawner over the process's App.
func newProdSpawner(app *operations.App, projectDir string, starter StarterFunc) *prodSpawner {
	return &prodSpawner{app: app, projectDir: projectDir, starter: starter}
}

// viaStartRunBackends is the delegation allowlist: the set of backend types
// that may run delegated children at all. Every child's engine control rides
// the StartRun path (spawn the runner process, await its dial-home, issue
// StartRun on its RunnerChannel). The runner-side EngineHost
// (internal/adapters/cli/llm_serve.go) gates on the agent.StructuredChat type
// assertion alone, never a backend name, so every member of this set gets
// the identical StartRun/adaptation/approval-forwarding/resume machinery;
// the per-backend deltas were only ever in model delivery.
//
// Any backend NOT in this set is refused at Resolve (checkStartRunAllowlist)
// — this gate is deliberately an allowlist of VERIFIED backends, not
// "implements StructuredChat", so a new backend must be reviewed onto
// StartRun explicitly rather than swept in.
//
// The bar for admitting one is a per-backend recon showing that delta is
// empty. What makes it empty generally: the runner-side standup
// (internal/adapters/cli's standUpRunner), the isolation starter
// (`ctxloom llm host <backend> --label ...`) and the HarnessSpec codec never
// name a backend at all, and a delegated child's context rides the FIRST
// TURN (runChildViaStartRun's JoinLeadBlocks into StartRun.input) rather than
// through any backend-specific config file a Setup step would have to write.
//
// TODO(slice 11b): these three tables are the last name-keyed capability
// declarations in core. They read Instance.Resume(key) — real or refused —
// once the instance half of the engine port lands; until then the engine's
// name is spelled here, and the no-engine-name-in-core gate allows it by
// that slice.
var viaStartRunBackends = map[string]bool{
	"claude-code": true,
	// mock is reviewed onto StartRun because the binary can HOST it: `ctxloom
	// llm host mock` stands up a real runner around the deterministic echo, so
	// a mock child is a driveable run, not a run nothing can answer. That is
	// what the acceptance journeys rely on when they delegate through a real
	// ctxloom binary. It is safe in a user's binary for the same reason it was
	// before the spool cutover: no credentials, no network, an echo.
	config.BackendMock: true,
}

// admit is Resolve's backend gate: checkStartRunAllowlist, nothing else. A
// Starter (Options.Starter) changes HOW an admitted backend's runner is
// stood up — in-process double instead of `ctxloom llm host` — never WHETHER
// it is admitted. An earlier shape special-cased mock here on the premise
// that it had no runner process of its own; that was false (the binary hosts
// it), so mock is on the allowlist and the special case is gone.
func (s *prodSpawner) admit(backend string) error {
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
//     from the first StartRunResult/init (see SpawnPlan.ResumeMode's doc);
//     this table is the STATIC half alone.
//   - a MIGRATED backend is not automatically resume-capable: the two tables
//     answer different questions, and one that neither consumes
//     ChatRequest.ResumeSessionID nor emits a native session-id Session event
//     stays FALSE here and re-primes from rendered history instead
//     (resumeChild's ResumeContext fallback), over StartRun all the same.
//   - mock (tests) and any unlisted/future backend: FALSE — an allowlist,
//     exactly like viaStartRunBackends, so a new backend is reviewed onto
//     resume explicitly rather than swept in by implementing StructuredChat.
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
// value always resolves to ResumeModePersistent — the identical behavior
// every agent gets today, byte-for-byte.
//
// An oneshot agent on an INCAPABLE backend FAILS LOUD here rather than
// silently downgrading to persistent — per isolation-must-not-negotiate,
// silently running a "oneshot" agent conversationally because the engine
// can't actually resume would be exactly the class of silent, behavior-
// changing divergence this project bans; the caller asked for one thing and
// would silently get another with no error raised.
//
// This function does NOT itself decide whether ResumeModeOneShot may be
// acted on in THIS release — that additional (and, right now, universal)
// gate lives in Resolve(), clearly separated so it can be deleted alone the
// day Slice 4 (the turn loop) lands, without touching this capability table.
func resolveResumeMode(driving agents.DrivingMode, backend string) (ResumeMode, error) {
	if driving != agents.DrivingOneshot {
		return ResumeModePersistent, nil
	}
	if !resumeCapableBackends[backend] {
		return ResumeModePersistent, fmt.Errorf(
			"driving: oneshot requires a resume-capable engine; backend %q has no resume-by-key primitive (known resume-capable: %s)",
			backend, resumeCapableBackendNames())
	}
	return ResumeModeOneShot, nil
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
func (s *prodSpawner) spawnGeneration(ctx context.Context) (*config.Snapshot, error) {
	snap, err := s.app.Reload(ctx)
	if err == nil {
		return snap, nil
	}
	clidiag.Warn("ctxloom", "agent_run: reload configuration for agent resolution: %v (using the published generation)", err)
	return s.app.Snapshot(ctx)
}

func (s *prodSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
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
	// end still fails loud here rather than resolving a ResumeModeOneShot
	// value the turn loop would silently run conversationally.
	if resumeMode == ResumeModeOneShot && !oneShotSupportedBackends[backend] {
		return nil, fmt.Errorf(
			"agent_run: agent %q: driving: oneshot is not yet available for backend %q in this release; it is resume-capable but ctxloom does not yet tear down/resume THIS engine at turn boundaries",
			agentName, backend)
	}
	if err := s.admit(backend); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}

	plan := &SpawnPlan{
		AgentName:  agentName,
		Backend:    backend,
		Label:      label,
		Profiles:   binding.Profiles,
		Runtime:    runtime,
		Permission: binding.Permissions,
		ResumeMode: resumeMode,
		snap:       snap,
	}
	// Resolved once here (not per StartEngine call) so the enqueue journal
	// and the launch see the IDENTICAL composed set.
	plan.MCPServers = s.childMCPServers(plan)
	return plan, nil
}

func (s *prodSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSessionHarp(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *prodSpawner) RecordEngineVersion(ctx context.Context, harp, backend string) {
	operations.RecordSessionEngineVersion(ctx, harp, backend)
}

// startEngine is operations.StartEngine's production entry point, indirected
// so a test can observe the launch each spawn resolves without a real
// runner process.
var startEngine = operations.StartEngine

// EngineSpawn is a StartEngine result: the spawned-but-not-chatting runner
// process plus the resolved launch the coordinator assembles the StartRun
// spec from.
type EngineSpawn struct {
	// Launch is the resolved launch the runner was started for.
	Launch launch.Launch
	// MCPServers is the composed managed set for the child session —
	// HarnessSpec.config["mcp_servers"].
	MCPServers []agent.ChatMCPServer
	// Kill tears the engine process and its cell down (idempotent).
	Kill func()
	// StderrTail reads the runner's bounded stderr tail without reaping —
	// the only death reason available when the whole runner dies without
	// emitting a FAILED RunCompleted. Nil-safe.
	StderrTail func() string
	// Wait blocks until the runner PROCESS exits, reporting why. Nil when
	// the spawner captures no process (test doubles).
	Wait func() error
}

// StartEngine resolves the child's launch against the spawn's generation
// and starts its runner. A delegated child defaults to its OWN worktree
// when neither the call nor the project chose a workspace: needing a
// private cwd is a property of how the parent fans, and the shared
// checkout is never the silent default for a child.
func (s *prodSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, start SpawnStart, runnerEnv map[string]string) (*EngineSpawn, error) {
	deps, err := operations.LaunchDepsFor(plan.snap, s.app.Strictness)
	if err != nil {
		return nil, err
	}
	workspace := plan.Workspace
	if workspace == "" && plan.snap.Config.GetWorkspace() == "" {
		workspace = launch.WorkspaceWorktree
	}
	src := launch.Source{
		Identity:  start.Identity,
		Agent:     plan.AgentName,
		Mode:      launch.StructuredMode(),
		WorkDir:   s.projectDir,
		Workspace: workspace,
		DirtyTree: plan.DirtyTreeHandler,
	}
	if start.Resumed {
		src.Resume = launch.Resume{Ref: sessions.ResumeRef{Harp: start.Identity.Harp, NativeKey: start.ResumeKey}}
	}
	l, err := launch.Resolve(ctx, deps, src)
	if err != nil {
		return nil, err
	}
	var starter isolation.EngineStarter
	if s.starter != nil {
		starter = s.starter(string(l.Engine), runnerEnv)
	}
	proc, err := startEngine(ctx, l, runnerEnv, childVerbosity(), starter)
	if err != nil {
		return nil, err
	}
	plan.Launch = l
	return &EngineSpawn{
		Launch:     l,
		MCPServers: plan.MCPServers,
		Kill:       proc.Kill,
		StderrTail: proc.StderrTail,
		Wait:       proc.Wait,
	}, nil
}

func (s *prodSpawner) ResumeContext(ctx context.Context, contextText, harp string) string {
	if entries, err := operations.RecordedSessionEntries(ctx, harp); err == nil {
		rendered := operations.RenderResumedTranscript(harp, entries)
		// RenderResumedTranscript legitimately returns "" for zero
		// substantive entries, and JoinLeadBlocks(contextText, "") yields
		// contextText UNCHANGED — indistinguishable, from the outside, from
		// "loaded the whole conversation". Only the error branch below used
		// to warn, so a resume that silently primed with no history at all
		// gave no signal the user could tell apart from a healthy resume.
		if rendered == "" {
			clidiag.Warn("ctxloom", "agent resume %s: no recorded history to prime (transcript rendered empty); resuming with the agent context only", harp)
		}
		contextText = operations.JoinLeadBlocks(contextText, rendered)
	} else {
		clidiag.Warn("ctxloom", "agent resume %s: no recorded history to prime (%v); resuming with the agent context only", harp, err)
	}
	return contextText
}

func (s *prodSpawner) MarkSessionEnded(harp string) {
	if err := operations.EndSession(harp, time.Now()); err != nil {
		clidiag.Warn("ctxloom", "agent %s: end session: %v", harp, err)
	}
}

// childMCPServers composes the child session's managed MCP set — the same
// sources Setup's settings write reconciles, scoped to the child's profile
// set (the chat substrate never runs Setup). Unlike the pre-coordinator
// orchestrator, NOTHING is stamped into the ctxloom entry's Env map: the
// child's ambient identity and coordinator reach-back ride the HARNESS
// process env only (the engine's MCP subprocesses inherit it), and the
// credential must never land in an MCP config structure.
func (s *prodSpawner) childMCPServers(plan *SpawnPlan) []agent.ChatMCPServer {
	// Composed ONCE, here at Resolve time, and never recomposed downstream:
	// the MCPServers field doc states a "resolved exactly once" invariant, and
	// recomposing would re-run ResolveBundleMCPServers and re-fire
	// WarnWithheld a second time. Nothing later needs to: the ctxloom entry's
	// command is the bare executable (agent.CtxloomCommand), which resolves on
	// PATH wherever the child runs — including inside a container, where the
	// isolation policy is not even known at this point.
	servers := agent.ComposeChatMCPServers(plan.snap.Config.ResolveBundleMCPServers(plan.Profiles), nil)
	operations.WarnWithheldBy(plan.snap.Config.ExecutableTrustGate())
	warnNoReachBack(plan.AgentName, servers)
	return servers
}

// warnNoReachBack reports a composed child MCP set with no ctxloom server in it.
//
// That set is the child's ONLY coordination surface: without it the child has no
// agent_send, no agent_recv and no agent_report, so it launches, consumes its
// budget, and can never answer its parent or be steered — the stranding shape
// spawnReachURL fails loud on when the fault is an unreachable endpoint. Here the
// cause is configuration (the builtin ctxloom bundle's `ctxloom` server
// withheld — a profile's `exclude_mcp: [ctxloom]`, or the item rejected — which
// composes a set carrying no ctxloom entry, nil when nothing else is registered
// either), so it warns rather than refusing: withholding it is a deliberate
// project choice and mirrors the documented degraded no-reach-back posture.
// What it must not be is SILENT.
func warnNoReachBack(agentName string, servers []agent.ChatMCPServer) {
	for _, srv := range servers {
		if srv.Name == agent.MCPServerName {
			return
		}
	}
	clidiag.Warn("ctxloom", "agent_run: agent %q composed no %q MCP server (the builtin ctxloom bundle's server is withheld for this project — check `exclude_mcp` on its profiles, and whether the item is rejected); the child launches WITHOUT agent_send/agent_recv/agent_report — it cannot report back or be steered",
		agentName, agent.MCPServerName)
}

// childVerbosity gates the child launch's plugin/adapter diagnostics. A dead
// child's only stderr trail (the go-plugin logger forwarding `llm serve` —
// and through it the ACP adapter's stderr) is DISCARDED at verbosity 0, and
// the coordinator often lives in a flagless `ctxloom mcp` process, so the
// knob is env-only: CTXLOOM_VERBOSE (the existing process-wide verbose
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
