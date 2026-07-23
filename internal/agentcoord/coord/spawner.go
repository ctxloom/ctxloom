package coord

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// Coordinator mirrors the resolved agent's Coordinator flag: whether this
	// child is trusted with the coordinator-only MCP tools (agent_run/
	// roster/agent_stop/agent_fetch_artifact). False (the default, leaf) is
	// threaded to the runner via runnerEnv's coordinatorCapable param, never
	// the wire — see EnvAgentCoordinator (identity.go) and the gate in
	// internal/cli/mcp_runner.go.
	Coordinator bool

	resolved *operations.ResolvedAgent
}

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
	// non-empty, asks the backend to resume its own native session (Slice 0,
	// wooly-stove) instead of starting fresh — the LEGACY go-plugin dial's
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
	// MarkSessionEnded stamps the harp ended in session accounting.
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
	cfg.SetExecutableTrustGate(s.gate.Gate())
	return s
}

// viaStartRunBackends is the C3 spawn-cutover gate: the set of backend types
// whose delegated Chat implementation rides the shared internal/acp driver
// (claude/codex/kiro embed it via their own chatACPConfig; "acp" IS it
// directly — see internal/acp/registry.go's descriptor comment). C3 recon
// confirmed the runner-side EngineHost (internal/cli/llm_serve.go) already
// gates on the agent.StructuredChat type assertion alone, never a backend
// name, so every member of this set gets the identical StartRun/adaptation/
// approval-forwarding/resume machinery — only each backend's OWN
// chatACPConfig differs (model delivery: internal/claude, internal/codex,
// internal/kiro, internal/acp's agent_engine default). Backends NOT in this
// set (antigravity, mock, and any future non-ACP StructuredChat backend)
// stay on the legacy go-plugin Chat dial — this gate is deliberately an
// allowlist of VERIFIED backends, not "implements StructuredChat", so a new
// backend must be reviewed onto StartRun explicitly rather than swept in.
var viaStartRunBackends = map[string]bool{
	config.BackendClaudeCode: true,
	"codex":                  true,
	"kiro":                   true,
	"acp":                    true,
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
		Coordinator: rs.Coordinator,
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

func (s *prodSpawner) Launch(ctx context.Context, plan *SpawnPlan, contextText, resumeSessionID string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error) {
	prep, err := operations.PrepareAgentChat(ctx, s.cfg, operations.AgentChatRequest{
		Resolved:         plan.resolved,
		Context:          contextText,
		WorkDir:          s.projectDir,
		Env:              env,
		RunnerEnv:        runnerEnv,
		Permissions:      plan.Perm,
		MCPServers:       plan.MCPServers,
		Gate:             s.gate.Gate(),
		Verbosity:        childVerbosity(),
		Factory:          s.factory,
		Workspace:        plan.Workspace,
		DirtyTreeHandler: plan.DirtyTreeHandler,
		ResumeSessionID:  resumeSessionID,
	})
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
}

func (s *prodSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	prep, err := operations.PrepareAgentChat(ctx, s.cfg, operations.AgentChatRequest{
		Resolved:         plan.resolved,
		WorkDir:          s.projectDir,
		Env:              env,
		RunnerEnv:        runnerEnv,
		Permissions:      plan.Perm,
		Gate:             s.gate.Gate(),
		Verbosity:        childVerbosity(),
		Factory:          s.factory,
		Workspace:        plan.Workspace,
		DirtyTreeHandler: plan.DirtyTreeHandler,
	})
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
	}, nil
}

func (s *prodSpawner) ResumeContext(ctx context.Context, plan *SpawnPlan, harp string) string {
	contextText := plan.Context
	if entries, err := operations.RecordedSessionEntries(ctx, harp); err == nil {
		contextText = operations.JoinLeadBlocks(contextText, operations.RenderResumedTranscript(harp, entries))
	} else {
		clidiag.Warn("ctxloom", "agent resume %s: no recorded history to prime (%v); resuming with the agent context only", harp, err)
	}
	return contextText
}

func (s *prodSpawner) MarkSessionEnded(harp string) {
	if err := operations.MarkSessionEnded(harp, time.Now()); err != nil {
		clidiag.Warn("ctxloom", "agent %s: mark session ended: %v", harp, err)
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
	servers := agent.ComposeChatMCPServers(plan.Backend,
		backends.AssembleManagedMCP(s.cfg, plan.Profiles),
		s.cfg.ResolveBundleMCPServers(plan.Profiles), nil)
	s.gate.WarnWithheld()
	return servers
}

// mcpServerNames projects a composed MCP server set onto NAMES ONLY — never
// command, args, or env, which can carry a secret. This is the shape
// enqueueRun journals (children.go's runEnqueued.MCPServers) and the roster
// surfaces (consumer.go's ListRunsResult_RunInfo.McpServers): an operator
// auditing a live delegation must be able to see WHAT a child can reach
// without the journal itself becoming a place a credential could leak.
func mcpServerNames(servers []agent.ChatMCPServer) []string {
	if len(servers) == 0 {
		return nil
	}
	out := make([]string, 0, len(servers))
	for _, srv := range servers {
		out = append(out, srv.Name)
	}
	return out
}

// childVerbosity gates the child launch's plugin/adapter diagnostics. A dead
// child's only stderr trail (the go-plugin logger forwarding `llm serve` —
// and through it the ACP adapter's stderr) is DISCARDED at verbosity 0, and
// the coordinator often lives in a flagless `ctxloom mcp` process, so the
// knob is env-only: CTXLOOM_VERBOSE=1 (the existing process-wide verbose
// switch) turns the trail on at trace.
func childVerbosity() int {
	if os.Getenv("CTXLOOM_VERBOSE") == "1" {
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

// strictnessCheckpoint is a thin alias so tests in this package can open the
// same window the production resolve does without importing strictness.
func strictnessCheckpoint() strictness.Mark { return strictness.Checkpoint() }
