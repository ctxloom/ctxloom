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
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// SpawnPlan is a resolved agent launch: everything the coordinator needs to
// enqueue, report, and (re)launch one child.
type SpawnPlan struct {
	AgentName string
	Backend   string
	Label     string
	Profiles  []string
	Runtime   agent.RuntimeAxis
	Context   string
	Perm      agent.PermissionMode
	// Workspace is GAP 2's per-call workspace-axis override (none|worktree;
	// empty = fall back to the project's cfg.Workspace default). Unlike
	// every other SpawnPlan field, it is NOT set by Resolve (agent
	// definitions carry no workspace opinion — see operations.memberAxes'
	// identical split between the fan's session-level workspace and a
	// member's agent-resolved runtime): AgentRun (children.go) stamps it
	// onto the plan from the caller's agent_run invocation, after Resolve
	// returns.
	Workspace string
	// DirtyTreeHandler is the identical per-call override for what a
	// worktree spawn does when the parent tree is dirty (the zero value =
	// fall back to the project's cfg.GetDirtyTreeHandler() default).
	// Stamped onto the plan the same way and at the same point as
	// Workspace, for the same reason: this is an ORCHESTRATION trait the
	// caller supplies per invocation, not something Resolve's pure
	// agent-definition resolution should carry.
	//
	// It is the TYPED value, parsed at the verb's edge (serveSpawnAgent for
	// the wire, the MCP tool handler for the native surface). The plan can
	// therefore only carry a handler the vocabulary admits; there is no
	// spelling on it for a later frame to re-interpret.
	DirtyTreeHandler operations.DirtyTreeHandler
	Degraded         []string
	// MCPServers is the child's fully composed MCP server set (Wave F1),
	// resolved once at Resolve time so a later config edit cannot
	// retroactively change a live run's privileges, and so that
	// resolving it exactly once means Launch/StartEngine and the enqueue
	// journal (children.go's enqueueRun) all read the identical set instead
	// of each recomposing it (which would also re-fire the executable trust
	// gate's withheld-item warning per call).
	MCPServers []agent.ChatMCPServer
	// ResumeMode is the per-engine resume-capability gate's outcome (one-shot
	// + resume-key plan, Slice 2 / Fork 3's STATIC half): ResumeModeOneShot
	// only when the resolved agent declared `driving: oneshot` AND the
	// backend has a cheap resume-by-key primitive (resumeCapableBackends);
	// ResumeModePersistent (the zero value) otherwise — including every
	// conversational agent. Resolved once in
	// Resolve() via resolveResumeMode, mirroring MCPServers above: a
	// later config edit must not retroactively change a
	// live run. Since Slice 4, Resolve() returns ResumeModeOneShot for the
	// WIRED backends (oneShotSupportedBackends) and the
	// coordinator's turn loop (children.go's oneShotReady/onTurnIdle) actually
	// tears the engine down and resumes it by key at each turn boundary. A
	// backend that is resume-capable but NOT yet wired end to end still fails
	// loud in Resolve() rather than resolving a value the turn loop would
	// silently run conversationally — see oneShotSupportedBackends' doc for
	// that residual v0.8 gate.
	ResumeMode ResumeMode

	resolved *operations.ResolvedAgent
	// cfg is the generation this spawn was resolved from: every later step
	// of the spawn (its MCP composition, its engine start) reads this value
	// and never the owner, so one spawn observes one state.
	cfg *config.Config
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
	// backend (resolveResumeMode); Resolve() narrows it once more to the
	// backends whose turn loop is wired end to end (oneShotSupportedBackends)
	// and fails loud for any other resume-capable one
	// rather than returning a mode the turn loop would not act on.
	ResumeModeOneShot
)

// Spawner is the coordinator's launch seam: production resolves and spawns
// real engines through the operations launch tail; tests fake children
// without config or engines.
type Spawner interface {
	// Resolve validates the agent name exactly as `run --agent` does,
	// including the D3 headless-safe permission gate, inside its own
	// serialized strictness window.
	Resolve(ctx context.Context, agentName string) (*SpawnPlan, error)
	// AssignSession mints the child's harp (its address and continuation
	// token) in the host-side session accounting, and does NOTHING ELSE.
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
	// StartEngine spawns the child's engine RUNNER process (isolation-
	// prepared, coordinator trio stamped via runnerEnv, env threaded into
	// the isolation session state) WITHOUT opening the go-plugin Chat
	// stream — the StartRun path's spawn half. Engine control then arrives
	// over the runner's own RunnerChannel (StartRun), built from the
	// returned EngineSpawn.
	StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error)
	// ResumeContext composes the context for a RESUMED harp: the plan
	// context plus the rendered recorded history when one is loadable.
	ResumeContext(ctx context.Context, plan *SpawnPlan, harp string) string
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

// prodSpawner is the production Spawner over the operations launch tail. It
// holds the process's App — the one config.Owner — and captures ONE
// generation per spawn (Resolve's Reload), which the plan then carries to
// every later step of that spawn.
type prodSpawner struct {
	app        *operations.App
	projectDir string
	starter    StarterFunc // test seam; nil = production (isolation binds the starter)
}

// newProdSpawner builds the production spawner over the process's App. The
// executable trust gate for the children's managed-MCP composition is the
// generation's own (config.Sources.TrustPorts), the same fail-closed gate
// the run/acp paths apply.
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
	var (
		cfg      *config.Config
		rs       *operations.ResolvedAgent
		perm     agent.PermissionMode
		degraded []string
	)
	if gerr := func() error {
		mark := strictness.Checkpoint()
		defer strictness.Close(mark)
		snap, err := s.spawnGeneration(ctx)
		if err != nil {
			return err
		}
		cfg = snap.Config
		rs, err = operations.ResolveAgent(ctx, cfg, agentName, "")
		if err != nil {
			return err
		}
		perm, degraded = headlessSafePermission(agentName, rs.Permissions)
		return strictness.FindingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}
	// Slice 2 — per-engine resume-capability gate (static half). FAILS LOUD
	// (never silently downgrades to persistent) when `driving: oneshot` names
	// a backend with no resume-by-key primitive.
	resumeMode, rmErr := resolveResumeMode(rs.Driving, rs.Backend)
	if rmErr != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, rmErr)
	}
	// Slice 4 landed the one-shot turn loop for oneShotSupportedBackends (the
	// migrated, live-loadSession-confirmed engines). A
	// backend that is statically resume-capable but NOT yet wired end to end
	// still fails loud here rather than resolving a ResumeModeOneShot value
	// the turn loop would silently run conversationally (ctxloom's banned
	// silent-no-op; see oneShotSupportedBackends' doc). A backend in neither
	// table never reaches this gate: resolveResumeMode already fails it loud
	// on the capability reason above. Every backend currently in
	// resumeCapableBackends is also in oneShotSupportedBackends today, so
	// this branch is a defensive residual, not a live gate — it stays wired
	// for the next resume-capable-but-unwired backend rather than being
	// deleted and re-added.
	if resumeMode == ResumeModeOneShot && !oneShotSupportedBackends[rs.Backend] {
		return nil, fmt.Errorf(
			"agent_run: agent %q: driving: oneshot is not yet available for backend %q in this release (its one-shot turn loop lands in v0.8); it is resume-capable but ctxloom does not yet tear down/resume THIS engine at turn boundaries",
			agentName, rs.Backend)
	}

	if err := s.admit(rs.Backend); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}

	plan := &SpawnPlan{
		AgentName:  agentName,
		Backend:    rs.Backend,
		Label:      rs.Label,
		Profiles:   rs.Profiles,
		Runtime:    rs.Runtime,
		Context:    rs.Context,
		Perm:       perm,
		Degraded:   degraded,
		ResumeMode: resumeMode,
		resolved:   rs,
		cfg:        cfg,
	}
	// F1: resolved once here (not per-Launch/StartEngine call) so the
	// enqueue journal, Launch, and StartEngine all see the IDENTICAL
	// composed set — see the MCPServers field comment above.
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

// prepareAgentChat is operations.PrepareAgentChat's production entry point,
// indirected so a test can observe the request each launch path builds without a
// real isolation prepare.
var prepareAgentChat = operations.PrepareAgentChat

// chatRequest builds StartEngine's AgentChatRequest: the resolved agent, the
// workspace/dirty-tree axes, the permission posture, the two env maps.
func (s *prodSpawner) chatRequest(plan *SpawnPlan, env, runnerEnv map[string]string) operations.AgentChatRequest {
	req := operations.AgentChatRequest{
		Resolved:         plan.resolved,
		WorkDir:          s.projectDir,
		Env:              env,
		RunnerEnv:        runnerEnv,
		Permissions:      plan.Perm,
		Verbosity:        childVerbosity(),
		Workspace:        plan.Workspace,
		DirtyTreeHandler: plan.DirtyTreeHandler,
	}
	if s.starter != nil {
		req.Starter = s.starter(plan.Backend, runnerEnv)
	}
	return req
}

// EngineSpawn is a StartEngine result: the spawned-but-not-chatting runner
// process plus everything the coordinator needs to assemble the StartRun
// HarnessSpec for it.
type EngineSpawn struct {
	// WorkDir is the isolation-resolved workspace (HarnessSpec.workspace).
	WorkDir string
	// Env is the harness env the legacy Chat path would have carried
	// (workspace env merged under the child's ambient identity) —
	// HarnessSpec.config["env"].
	Env map[string]string
	// Model is the RESOLVED model (post resolveChatModel — never an alias;
	// the C1 model-gate guarantee).
	Model string
	// MCPServers is the composed managed set for the child session —
	// HarnessSpec.config["mcp_servers"].
	MCPServers []agent.ChatMCPServer
	// Kill tears the engine process and its workspace down (idempotent).
	Kill func()
	// StderrTail reads the runner's bounded stderr tail without reaping —
	// the container's streamed stderr (and, teed through it, the engine
	// adapter's), the only death reason available when the whole runner dies
	// without emitting a FAILED RunCompleted (docker-stop / OOM = runner
	// loss). Nil-safe. See operations.AgentEngineProcess.StderrTail.
	StderrTail func() string
	// Wait blocks until the runner PROCESS exits, reporting why (the error
	// embeds the stderr tail). issueStartRun races it against the dial-home
	// wait so a runner that died at standup fails the spawn AT ONCE, with its
	// own dying words, instead of costing the parent the full
	// runnerAwaitTimeout in total silence. Nil when the spawner captures no
	// process (test doubles), which degrades to timeout-only detection.
	// See operations.AgentEngineProcess.Wait.
	Wait func() error
}

func (s *prodSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	prep, err := prepareAgentChat(ctx, plan.cfg, s.chatRequest(plan, env, runnerEnv))
	if err != nil {
		return nil, err
	}
	eng, err := prep.StartEngine(ctx)
	if err != nil {
		return nil, err
	}
	return &EngineSpawn{
		WorkDir:    eng.WorkDir,
		Env:        eng.Env,
		Model:      eng.Model,
		MCPServers: plan.MCPServers,
		Kill:       eng.Kill,
		StderrTail: eng.StderrTail,
		Wait:       eng.Wait,
	}, nil
}

func (s *prodSpawner) ResumeContext(ctx context.Context, plan *SpawnPlan, harp string) string {
	contextText := plan.Context
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
	servers := agent.ComposeChatMCPServers(plan.cfg.ResolveBundleMCPServers(plan.Profiles), nil)
	operations.WarnWithheldBy(plan.cfg.ExecutableTrustGate())
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

// headlessSafePermission enforces D3's structural floor: children never
// prompt the ENGINE inline, so the agent must DECLARE a headless-safe
// permission enum (bypass|plan) — an absent field is refused exactly like a
// non-headless-safe one, loudly. Under degraded mode the refusal becomes a
// warning and the child launches at the MOST RESTRICTIVE headless-safe
// posture: degraded never widens a child's permissions, it narrows them.
// The floor says whether the child may run headless AT ALL. Nothing above it
// answers the engine permission requests the child makes while doing so — the
// coordinator does not broker approvals (see doc.go's D3).
func headlessSafePermission(name, declared string) (agent.PermissionMode, []string) {
	if declared != "" {
		if mode, ok := agent.ParsePermissionMode(declared); ok && mode.SafeHeadless() {
			return mode, nil
		}
	}
	reason := "declares no permissions"
	if declared != "" {
		reason = fmt.Sprintf("declares permissions %q, which is not headless-safe", declared)
	}
	strictness.Fail(strictness.ClassConfig,
		fmt.Sprintf("set permissions: plan|bypass on agent %q (agents: in .ctxloom/config.yaml)", name),
		"agent_run: agent %q %s: a delegated child has no channel to surface a permission prompt (D3)", name, reason)
	if strictness.Degraded() {
		return agent.PermissionPlan, []string{fmt.Sprintf(
			"agent %q %s; degraded mode launches it at %q — the most restrictive headless-safe posture", name, reason, agent.PermissionPlan)}
	}
	return agent.PermissionPlan, nil // unreachable in strict mode: strictness.FindingsError refuses
}
