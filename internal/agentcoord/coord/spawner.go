package coord

import (
	"context"
	"fmt"
	"os"
	"sync"
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
	Degraded  []string
	// ViaStartRun routes this child's engine control over the agentcoord
	// StartRun path (spawn the runner process, await its dial-home, issue
	// StartRun on its RunnerChannel) instead of the legacy go-plugin Chat
	// dial. Wave C1 scope gate: CLAUDE ONLY — codex/kiro also implement
	// StructuredChat but stay on the legacy path until C3 verifies them
	// per-backend.
	ViaStartRun bool

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
	// holder; the harness env never carries it).
	Launch(ctx context.Context, plan *SpawnPlan, contextText string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error)
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

// spawnGateMu serializes each spawn's strictness window (Checkpoint →
// resolve/D3 → findings) AND each child's isolation-prepare window: strictness
// Marks only isolate SEQUENTIAL windows in one process (see the run/acp gate
// mutexes), so an unserialized window interleaving with a concurrent spawn's
// would land its findings inside — and wrongly refuse — that spawn. The lock
// never covers the engine spawn itself.
var spawnGateMu sync.Mutex

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

func (s *prodSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
	var (
		rs       *operations.ResolvedAgent
		perm     agent.PermissionMode
		degraded []string
	)
	if gerr := func() error {
		spawnGateMu.Lock()
		defer spawnGateMu.Unlock()
		mark := strictness.Checkpoint()
		var err error
		rs, err = operations.ResolveAgent(ctx, s.cfg, agentName, "")
		if err != nil {
			return err
		}
		perm, degraded = headlessSafePermission(agentName, rs.Permissions)
		return findingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}
	return &SpawnPlan{
		AgentName: agentName,
		Backend:   rs.Backend,
		Label:     rs.Label,
		Profiles:  rs.Profiles,
		Runtime:   rs.Runtime,
		Context:   rs.Context,
		Perm:      perm,
		Degraded:  degraded,
		// C1 scope gate: delegated CLAUDE children ride StartRun; every
		// other backend keeps the legacy go-plugin Chat dial until C3
		// verifies it per-backend (claude first — the most-exercised
		// driver; blanket-routing codex/kiro would sweep them in
		// unverified).
		ViaStartRun: rs.Backend == config.BackendClaudeCode,
		resolved:    rs,
	}, nil
}

func (s *prodSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *prodSpawner) Launch(ctx context.Context, plan *SpawnPlan, contextText string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error) {
	mcpServers := s.childMCPServers(plan)
	spawnGateMu.Lock()
	prep, err := operations.PrepareAgentChat(ctx, s.cfg, operations.AgentChatRequest{
		Resolved:    plan.resolved,
		Context:     contextText,
		WorkDir:     s.projectDir,
		Env:         env,
		RunnerEnv:   runnerEnv,
		Permissions: plan.Perm,
		MCPServers:  mcpServers,
		Gate:        s.gate.Gate(),
		Verbosity:   childVerbosity(),
		Factory:     s.factory,
	})
	spawnGateMu.Unlock()
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
	mcpServers := s.childMCPServers(plan)
	spawnGateMu.Lock()
	prep, err := operations.PrepareAgentChat(ctx, s.cfg, operations.AgentChatRequest{
		Resolved:    plan.resolved,
		WorkDir:     s.projectDir,
		Env:         env,
		RunnerEnv:   runnerEnv,
		Permissions: plan.Perm,
		Gate:        s.gate.Gate(),
		Verbosity:   childVerbosity(),
		Factory:     s.factory,
	})
	spawnGateMu.Unlock()
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
		MCPServers: mcpServers,
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

// headlessSafePermission enforces D3: children never prompt, so the agent
// must DECLARE a headless-safe permission enum — an absent field is refused
// exactly like a non-headless-safe one, loudly. Under degraded mode the
// refusal becomes a warning and the child launches at the MOST RESTRICTIVE
// headless-safe posture: degraded never widens a child's permissions, it
// narrows them.
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
	return agent.PermissionPlan, nil // unreachable in strict mode: findingsError refuses
}

// strictnessCheckpoint is a thin alias so tests in this package can open the
// same window the production resolve does without importing strictness.
func strictnessCheckpoint() strictness.Mark { return strictness.Checkpoint() }

// findingsError renders the strictness findings since mark as an error (the
// per-call variant for servers that must keep running — same contract as the
// cli's findingsError).
func findingsError(mark strictness.Mark) error {
	found := strictness.Since(mark)
	if len(found) == 0 || strictness.Degraded() {
		return nil
	}
	msg := "fatal startup findings:"
	for _, f := range found {
		msg += "\n  - " + f.Message
		if f.FixIt != "" {
			msg += " (fix: " + f.FixIt + ")"
		}
	}
	return fmt.Errorf("%s", msg)
}
