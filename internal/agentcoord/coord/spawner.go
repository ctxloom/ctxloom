package coord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	Runtime   string
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
	// worktree spawn does when the parent tree is dirty (commit|copy|
	// stale|fail; empty = fall back to the project's
	// cfg.GetDirtyTreeHandler() default). Stamped onto the plan the same
	// way and at the same point as Workspace, for the same reason: this is
	// an ORCHESTRATION trait the caller supplies per invocation, not
	// something Resolve's pure agent-definition resolution should carry.
	DirtyTreeHandler string
	// Ladder is the resolved (preset-or-declared, validated) escalation
	// ladder (Wave C2) this child's ApprovalRequests walk. Resolved once at
	// spawn time from the agent's declared escalation: (or the Perm preset)
	// so a later config edit cannot retroactively change a live run's
	// policy — enqueueRun journals it onto the run record.
	Ladder   Ladder
	Degraded []string
	// MCPServers is the child's fully composed MCP server set (Wave F1),
	// resolved once at Resolve time — the SAME rationale as Ladder: a later
	// config edit must not retroactively change a live run's privileges, and
	// resolving it exactly once means Launch/StartEngine and the enqueue
	// journal (children.go's enqueueRun) all read the identical set instead
	// of each recomposing it (which would also re-fire the executable trust
	// gate's withheld-item warning per call).
	MCPServers []agent.ChatMCPServer
	// ViaStartRun routes this child's engine control over the agentcoord
	// StartRun path (spawn the runner process, await its dial-home, issue
	// StartRun on its RunnerChannel) instead of the legacy go-plugin Chat
	// dial. Wave C1 landed claude-only; Wave C3 extends the gate to every
	// backend that rides the shared internal/acp driver (see
	// viaStartRunBackends) — codex, kiro, and the generic "acp" entry —
	// once its recon confirmed the runner-side EngineHost/adaptation path
	// (llm_serve.go) was ALREADY backend-agnostic (gated only on the
	// agent.StructuredChat type assertion, never a backend-name check): the
	// only real per-backend deltas lived in each backend's own chatACPConfig
	// (model delivery), not in the coordinator/runner machinery.
	ViaStartRun bool
	// ResumeMode is the per-engine resume-capability gate's outcome (one-shot
	// + resume-key plan, Slice 2 / Fork 3's STATIC half): ResumeModeOneShot
	// only when the resolved agent declared `driving: oneshot` AND the
	// backend has a cheap resume-by-key primitive (resumeCapableBackends);
	// ResumeModePersistent (the zero value) otherwise — including every
	// conversational agent. Resolved once in
	// Resolve() via resolveResumeMode, mirroring Ladder/MCPServers above: a
	// later config edit must not retroactively change a
	// live run. Since Slice 4, Resolve() returns ResumeModeOneShot for the
	// WIRED backends (oneShotSupportedBackends: claude-code, codex) and the
	// coordinator's turn loop (children.go's oneShotReady/onTurnIdle) actually
	// tears the engine down and resumes it by key at each turn boundary. A
	// backend that is resume-capable but NOT yet wired end to end still fails
	// loud in Resolve() rather than resolving a value the turn loop would
	// silently run conversationally — see oneShotSupportedBackends' doc for
	// that residual v0.8 gate.
	ResumeMode ResumeMode

	resolved *operations.ResolvedAgent
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
	// backends whose turn loop is wired end to end (oneShotSupportedBackends:
	// claude-code, codex) and fails loud for any other resume-capable one
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
	// token) in the host-side session accounting.
	AssignSession(projectDir, backend string) (string, error)
	// Launch starts the child engine with the composed context riding the
	// first turn, env as the ENGINE's extra environment (ambient identity),
	// and runnerEnv stamped per-spawn onto the RUNNER process (the
	// coordinator reach-back trio — the runner is the one credential
	// holder; the harness env never carries it). resumeSessionID, when
	// non-empty, asks the backend to resume its own native session (Slice 0)
	// instead of starting fresh — the LEGACY go-plugin dial's
	// counterpart to the migrated StartRun path's
	// StartRun{ResumeSessionId}. A fresh (non-resumed) launch passes "".
	Launch(ctx context.Context, plan *SpawnPlan, contextText, resumeSessionID string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error)
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

// prodSpawner is the production Spawner over the operations launch tail.
type prodSpawner struct {
	cfg        *config.Config
	projectDir string
	gate       *operations.ExecutableTrustGate
	factory    pb.ClientFactory // test seam; nil = default (isolation applies)
}

// newProdSpawner builds the production spawner, installing the executable
// trust gate for the children's managed-MCP composition (the same fail-closed
// gate the run/acp paths apply).
func newProdSpawner(cfg *config.Config, projectDir string, factory pb.ClientFactory) *prodSpawner {
	s := &prodSpawner{cfg: cfg, projectDir: projectDir, factory: factory}
	s.gate = operations.NewExecutableTrustGate(cfg)
	cfg.SetExecutableTrustGate(s.gate.Authorizer())
	return s
}

// viaStartRunBackends is the C3 spawn-cutover gate: the set of backend types
// whose delegated Chat implementation rides the shared internal/acp driver
// (claude/codex/kiro/opencode embed it via their own chatACPConfig; "acp" IS
// it directly — see internal/acp/registry.go's descriptor comment). C3 recon
// confirmed the runner-side EngineHost (internal/cli/llm_serve.go) already
// gates on the agent.StructuredChat type assertion alone, never a backend
// name, so every member of this set gets the identical StartRun/adaptation/
// approval-forwarding/resume machinery — only each backend's OWN
// chatACPConfig differs (model delivery: internal/claude, internal/codex,
// internal/kiro, internal/opencode, internal/acp's agent_engine default).
// Backends NOT in this set ride the legacy coordinator-driven chat path ONLY
// if they are in legacyChatBackends below; anything else is refused at Resolve
// (checkLegacyChatFreeze) — this gate is deliberately an
// allowlist of VERIFIED backends, not "implements StructuredChat", so a new
// backend must be reviewed onto StartRun explicitly rather than swept in.
//
// opencode joined in the spool cutover's S3b slice, the shrink the freeze
// below exists to permit. Its recon measured the SAME delta C3 measured for
// kiro and found it empty: internal/opencode's Chat is acp.NewChatDriver over
// `opencode acp` (a first-party ACP subcommand — an ACPNative transport, like
// kiro's `kiro-cli acp`, NOT a server the coordinator dials), wrapped in a
// transient opencode.json overlay that carries what `opencode acp` has no
// flag for (model, MCP set, the plan-mode read-only permission). That overlay
// is written by Chat itself from the ChatRequest it is handed, so it is
// identical whichever transport delivered the request; the runner-side
// standup (internal/cli's standUpRunner), the isolation starter
// (`ctxloom llm host opencode --label ...`, which applies the configured
// binary_path exactly as `llm serve` did) and the HarnessSpec codec never
// name a backend at all. The one Setup-time dependency in that overlay
// (b.pendingContext/Commands/Skills) is not a delta either: a delegated
// child's context rides the FIRST TURN on both paths — legacy via
// operations.leadContextIn, StartRun via runChildViaStartRun's JoinLeadBlocks
// into StartRun.input — never through opencode.json, which is exactly the
// ACP-hosted case assertSetupRan documents.
var viaStartRunBackends = map[string]bool{
	config.BackendClaudeCode: true,
	"codex":                  true,
	"kiro":                   true,
	"acp":                    true,
	"opencode":               true,
}

// legacyChatBackends is the RETIRE-FIRST freeze gate for the legacy
// coordinator-driven child path (children.go's driveChild loop: the go-plugin
// Chat dial for a StructuredChat backend outside viaStartRunBackends, and the
// per-turn oneshot fallback for a backend with no StructuredChat at all).
// That path is RETIRED: it is never ported to the file-spool messaging
// substrate that replaces the coordinator mailbox, so this set may only
// EMPTY — a member either migrates onto StartRun (viaStartRunBackends) or
// loses delegation when the mailbox machinery is deleted. Nothing is ever
// added: a backend in NEITHER table used to be swept silently onto the
// legacy loop and is now refused loudly at Resolve (checkLegacyChatFreeze).
//
//   - mock: the test backend, and — since S3b migrated opencode onto
//     StartRun — the table's SOLE remaining member. No production backend
//     rides the frozen path by backend identity any more.
//
// The frozen path's OTHER reachable arm — a degraded (no-reach-back) spawn
// of a viaStartRunBackends member, where StartRun is impossible because the
// runner could never dial home — is gated by runChild's `url == ""` check,
// not by this table; it is frozen under the same ruling.
var legacyChatBackends = map[string]bool{
	"mock": true,
}

// checkLegacyChatFreeze refuses a delegated spawn whose backend would land on
// the retired legacy chat path without being one of its frozen residents. A
// backend on the StartRun path (viaStartRunBackends) or in the frozen residue
// (legacyChatBackends) passes; anything else — a newly registered backend
// never reviewed onto StartRun, or a config-declared llm type that matches no
// backend — fails loud here at Resolve time instead of silently joining a
// path that is being removed (ctxloom never silently no-ops).
func checkLegacyChatFreeze(backend string) error {
	if viaStartRunBackends[backend] || legacyChatBackends[backend] {
		return nil
	}
	return fmt.Errorf(
		"backend %q cannot run delegated children: the legacy coordinator-driven chat path is retired and frozen — it admits no new backends; delegated children run runner-side via StartRun (backends: %s), and only the frozen legacy backends (%s) remain on the old path until they migrate or are removed",
		backend,
		strings.Join(backendNames(viaStartRunBackends), ", "),
		strings.Join(backendNames(legacyChatBackends), ", "))
}

// resumeCapableBackends is the one-shot-resume plan's Slice 2 / Fork 3
// STATIC gating table: which backends have a cheap resume-by-key primitive
// at all, independent of viaStartRunBackends (that table is about WHICH
// wire path a child's Chat rides; this one is about whether ASKING an
// already-ended engine to continue its own native session is even possible).
//
//   - claude-code / codex: resume via the shared ACP driver's
//     `session/load` (internal/acp/session.go) — LIVE-gated a second time on
//     the adapter's advertised loadSession capability once Slice 4 records it
//     from the first StartRunResult/init (see SpawnPlan.ResumeMode's doc);
//     this table is the STATIC half alone.
//   - opencode: FALSE, deliberately absent even though S3b migrated it onto
//     the StartRun path (viaStartRunBackends["opencode"] == true) — the two
//     tables answer different questions. It neither consumes
//     ChatRequest.ResumeSessionID nor emits a native session-id Session
//     event (internal/opencode/chat.go); its only resume surface is
//     read-only `opencode export`. No cheap resume-by-key primitive exists;
//     new backend work (v0.8+), not a config toggle. A resumed opencode child
//     therefore re-primes from rendered history (resumeChild's
//     ResumeContext fallback), over StartRun like every other migrated
//     backend.
//   - kiro: FALSE, deliberately absent even though it rides the migrated
//     StartRun path (viaStartRunBackends["kiro"] == true) — per
//     isolation-must-not-negotiate its GLOBAL sqlite makes resume IDENTITY
//     itself suspect (an isolation/container precondition, not a
//     coordinator-resolvable one). Deferred until that is proven, not a
//     permanent no.
//   - mock (tests) and any unlisted/future backend: FALSE — an allowlist,
//     exactly like viaStartRunBackends, so a new backend is reviewed onto
//     resume explicitly rather than swept in by implementing StructuredChat.
var resumeCapableBackends = map[string]bool{
	config.BackendClaudeCode: true,
	"codex":                  true,
}

// oneShotSupportedBackends is the set of backends whose driving:oneshot turn
// loop is wired END TO END in this release (one-shot-resume plan, Slice 4): the
// MIGRATED (viaStartRunBackends), resume-capable engines whose LIVE loadSession
// capability the coordinator confirms before tearing an engine down at a turn
// boundary (children.go's oneShotReady) and then resumes by native session key
// via StartRun{ResumeSessionId} → ACP session/load. That is the intersection of
// viaStartRunBackends and resumeCapableBackends: claude-code and codex.
//
// kiro/opencode never reach the gate at all: resolveResumeMode already fails
// them loud on the capability reason (neither is in resumeCapableBackends).
var oneShotSupportedBackends = map[string]bool{
	config.BackendClaudeCode: true,
	"codex":                  true,
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

// loadConfig is config.Load's production entry point, indirected so tests
// can inject a failing loader (config.Load itself is fault-tolerant per
// CLAUDE.md — a malformed/unreadable config.yaml degrades to Warnings, not
// an error — so resolveCfg's fallback branch below has no naturally
// occurring trigger through the real loader; this seam is what makes it
// independently testable, mirroring oneshot.go's prepareIsolation var swap).
var loadConfig = config.Load

// resolveCfg re-reads the project config from disk so a mid-session
// `ctxloom agent set` (or a brand-new agent) is visible to the VERY NEXT
// agent_run without a coordinator restart (GAP 1: the captured s.cfg is a
// startup snapshot otherwise). Pinned to s.cfg.AppPaths[0] — the .ctxloom
// dir this spawner's OWN config already resolved to at construction — so
// the reload never depends on this process's current working directory
// (mirrors loadConfigForDir's dir-pinning in internal/cli/acp_cmd.go).
//
// Scoped STRICTLY to agent-DEFINITION resolution: durable stores,
// credentials, the broker, and the run loop all keep using the startup
// s.cfg (childMCPServers, PrepareAgentChat's gate, etc.) — only the
// agent-def lookup below re-reads. A transient read failure (permission
// blip, a concurrent partial write) must not break spawning, so it falls
// back to the captured snapshot rather than erroring.
func (s *prodSpawner) resolveCfg() *config.Config {
	appPaths := s.cfg.GetAppPaths()
	if len(appPaths) == 0 || appPaths[0] == "" {
		return s.cfg
	}
	cfg, err := loadConfig(config.WithAppDir(appPaths[0]))
	if err != nil {
		clidiag.Warn("ctxloom", "agent_run: reload config for agent resolution: %v (using the startup snapshot)", err)
		return s.cfg
	}
	return cfg
}

func (s *prodSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
	var (
		rs       *operations.ResolvedAgent
		perm     agent.PermissionMode
		degraded []string
	)
	if gerr := func() error {
		mark := strictness.Checkpoint()
		defer strictness.Close(mark)
		var err error
		rs, err = operations.ResolveAgent(ctx, s.resolveCfg(), agentName, "")
		if err != nil {
			return err
		}
		perm, degraded = headlessSafePermission(agentName, rs.Permissions)
		return strictness.FindingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}
	ladder, err := buildLadder(agentName, rs.Escalation, perm)
	if err != nil {
		return nil, fmt.Errorf("agent_run: %w", err)
	}

	// Slice 2 — per-engine resume-capability gate (static half). FAILS LOUD
	// (never silently downgrades to persistent) when `driving: oneshot` names
	// a backend with no resume-by-key primitive.
	resumeMode, rmErr := resolveResumeMode(rs.Driving, rs.Backend)
	if rmErr != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, rmErr)
	}
	// Slice 4 landed the one-shot turn loop for oneShotSupportedBackends (the
	// migrated, live-loadSession-confirmed engines: claude-code, codex). A
	// backend that is statically resume-capable but NOT yet wired end to end
	// still fails loud here rather than resolving a ResumeModeOneShot value
	// the turn loop would silently run conversationally (ctxloom's banned
	// silent-no-op; see oneShotSupportedBackends' doc). opencode/kiro never
	// reach this gate: resolveResumeMode already fails them loud on the
	// capability reason above. Every backend currently in
	// resumeCapableBackends is also in oneShotSupportedBackends today, so
	// this branch is a defensive residual, not a live gate — it stays wired
	// for the next resume-capable-but-unwired backend rather than being
	// deleted and re-added.
	if resumeMode == ResumeModeOneShot && !oneShotSupportedBackends[rs.Backend] {
		return nil, fmt.Errorf(
			"agent_run: agent %q: driving: oneshot is not yet available for backend %q in this release (its one-shot turn loop lands in v0.8); it is resume-capable but ctxloom does not yet tear down/resume THIS engine at turn boundaries",
			agentName, rs.Backend)
	}

	// RETIRE-FIRST freeze gate (spool cutover): a backend outside BOTH
	// viaStartRunBackends and legacyChatBackends used to be swept silently
	// onto the legacy coordinator-driven chat loop (children.go's
	// driveChild). That loop is retired — never ported to the spool
	// substrate — so an unreviewed backend fails loud here instead of
	// quietly joining a path that is being removed.
	if err := checkLegacyChatFreeze(rs.Backend); err != nil {
		return nil, fmt.Errorf("agent_run: agent %q: %w", agentName, err)
	}

	plan := &SpawnPlan{
		AgentName: agentName,
		Backend:   rs.Backend,
		Label:     rs.Label,
		Profiles:  rs.Profiles,
		Runtime:   rs.Runtime,
		Context:   rs.Context,
		Perm:      perm,
		Ladder:    ladder,
		Degraded:  degraded,
		// C3: every backend whose delegated Chat rides the shared ACP
		// driver moves onto StartRun (see viaStartRunBackends).
		ViaStartRun: viaStartRunBackends[rs.Backend],
		ResumeMode:  resumeMode,
		resolved:    rs,
	}
	// F1: resolved once here (not per-Launch/StartEngine call) so the
	// enqueue journal, Launch, and StartEngine all see the IDENTICAL
	// composed set — see the MCPServers field comment above.
	plan.MCPServers = s.childMCPServers(plan)
	return plan, nil
}

func (s *prodSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

// prepareAgentChat is operations.PrepareAgentChat's production entry point,
// indirected so a test can observe the request each launch path builds without a
// real isolation prepare (mirroring loadConfig above).
var prepareAgentChat = operations.PrepareAgentChat

// chatRequest builds the AgentChatRequest fields BOTH launch paths share.
//
// It exists so a field cannot land on one path and be forgotten on the other:
// Launch and StartEngine differ ONLY in the three legacy-dial fields Launch adds
// after this returns, and the shared remainder — the resolved agent, the
// workspace/dirty-tree axes, the permission posture, the trust gate, the two env
// maps — is composed exactly once.
func (s *prodSpawner) chatRequest(plan *SpawnPlan, env, runnerEnv map[string]string) operations.AgentChatRequest {
	return operations.AgentChatRequest{
		Resolved:         plan.resolved,
		WorkDir:          s.projectDir,
		Env:              env,
		RunnerEnv:        runnerEnv,
		Permissions:      plan.Perm,
		Gate:             s.gate.Authorizer(),
		Verbosity:        childVerbosity(),
		Factory:          s.factory,
		Workspace:        plan.Workspace,
		DirtyTreeHandler: plan.DirtyTreeHandler,
	}
}

func (s *prodSpawner) Launch(ctx context.Context, plan *SpawnPlan, contextText, resumeSessionID string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error) {
	req := s.chatRequest(plan, env, runnerEnv)
	// The three fields ONLY the legacy go-plugin Chat dial consumes, verified
	// against PreparedAgentChat.StartEngine, which reads none of them: Context
	// rides the first turn (StartRun has no first turn to ride), MCPServers is
	// patched into the EngineSpawn from plan.MCPServers instead, and
	// ResumeSessionID's StartRun counterpart is HarnessSpec.resume_session_id.
	req.Context = contextText
	req.MCPServers = plan.MCPServers
	req.ResumeSessionID = resumeSessionID
	prep, err := prepareAgentChat(ctx, s.cfg, req)
	if err != nil {
		return nil, err
	}
	return prep.Start(ctx)
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
}

func (s *prodSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	prep, err := prepareAgentChat(ctx, s.cfg, s.chatRequest(plan, env, runnerEnv))
	if err != nil {
		return nil, err
	}
	eng, err := prep.StartEngine(ctx)
	if err != nil {
		return nil, err
	}
	return &EngineSpawn{
		WorkDir: eng.WorkDir,
		Env:     eng.Env,
		Model:   eng.Model,
		// plan.MCPServers was composed at Resolve time (before this
		// call's PrepareAgentChat resolved the real isolation.Policy), so its
		// auto-registered ctxloom entry may still carry the host self-exec
		// path even for a runtime:container child. prep.MCPCommandOverride()
		// carries the now-resolved override; patch it in here the same way
		// PrepareAgentChat already patches p.req.MCPServers for the Launch
		// (legacy Chat) path — see agent.PatchManagedCommand's doc for why
		// this patches rather than recomposes.
		MCPServers: agent.PatchManagedCommand(plan.MCPServers, prep.MCPCommandOverride()),
		Kill:       eng.Kill,
		StderrTail: eng.StderrTail,
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
	// ComposeChatMCPServers takes a command override so
	// a runtime:container agent's structured chat can emit the in-container
	// ctxloom path instead of the host's — but prodSpawner has no
	// isolation.Policy in scope here (Resolve time) to derive one from:
	// plan.Runtime only names the runtime, and the real policy is not
	// resolved until Launch/StartEngine call operations.PrepareAgentChat.
	// Composed here with override="" (the pre-policy host path) and PATCHED
	// later, once the policy is known: PrepareAgentChat patches its own
	// req.MCPServers copy for the Launch path, and StartEngine patches its
	// EngineSpawn.MCPServers via agent.PatchManagedCommand using
	// prep.MCPCommandOverride() — see both call sites below. Composing here
	// with override="" and patching downstream (rather than recomposing
	// once the policy is known) preserves the "resolved exactly once"
	// invariant the MCPServers field doc states: recomposing would re-run
	// AssembleManagedMCP/ResolveBundleMCPServers and re-fire
	// WarnWithheld a second time.
	servers := agent.ComposeChatMCPServers(plan.Backend, "",
		backends.AssembleManagedMCP(s.cfg, plan.Profiles),
		s.cfg.ResolveBundleMCPServers(plan.Profiles), nil)
	s.gate.WarnWithheld()
	warnNoReachBack(plan.AgentName, servers)
	return servers
}

// warnNoReachBack reports a composed child MCP set with no ctxloom server in it.
//
// That set is the child's ONLY coordination surface: without it the child has no
// agent_send, no agent_recv and no agent_report, so it launches, consumes its
// budget, and can never answer its parent or be steered — the stranding shape
// spawnReachURL fails loud on when the fault is an unreachable endpoint. Here the
// cause is configuration (`mcp.auto_register_ctxloom: false`, which composes a
// set carrying no ctxloom entry — nil when nothing else is registered either), so
// it warns rather than refusing: the setting is a deliberate project choice and
// mirrors the documented degraded no-reach-back posture. What it must not be is
// SILENT.
func warnNoReachBack(agentName string, servers []agent.ChatMCPServer) {
	for _, srv := range servers {
		if srv.Name == agent.MCPServerName {
			return
		}
	}
	clidiag.Warn("ctxloom", "agent_run: agent %q composed no %q MCP server (mcp.auto_register_ctxloom is off for this project); the child launches WITHOUT agent_send/agent_recv/agent_report — it cannot report back or be steered",
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
// This is a SEPARATE axis from the escalation ladder (ladder.go): the floor
// says whether the child may run headless AT ALL; the ladder — resolved
// from the SAME perm right below — says what happens to the approval
// requests it makes while doing so (see doc.go's "D3 EVOLUTION").
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
