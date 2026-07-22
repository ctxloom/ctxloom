package operations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// AgentChatRequest launches a resolved agent as a delegated CHILD driven over
// the structured-chat substrate (agent_run): the same launch tail the fan
// uses, but multi-turn — the orchestrator delivers bus messages as turns.
type AgentChatRequest struct {
	Resolved *ResolvedAgent
	// Context overrides Resolved.Context for this launch (a resume primes it
	// with rendered history); empty uses Resolved.Context. It rides the first
	// turn as a lead block — the chat substrate never runs Setup.
	Context string
	WorkDir string
	// Env is the child engine's extra environment: the child's session harp
	// and project identity (ambient identity — never client-claimed).
	Env map[string]string
	// RunnerEnv is stamped per spawn onto the RUNNER process (`ctxloom llm
	// serve`): the coordinator reach-back trio the runner-terminated MCP
	// path consumes. host: cmd.Env on the subprocess; container: bare-name
	// `-e` forms with values on the run-process env. Never merged into the
	// engine env — the runner is the one credential holder.
	RunnerEnv map[string]string
	// Permissions is the already-gated headless-safe posture (D3: children
	// never prompt; the caller refused or downgraded anything else).
	Permissions agent.PermissionMode
	// Workspace is the caller's per-invocation workspace-axis override
	// (isolation.WorkspaceAxis values: "none"|"worktree"; GAP 2). It OVERRIDES
	// cfg.Workspace when set; empty falls back to cfg.Workspace — the same
	// session-level-orchestration-trait-vs-agent-trait split
	// operations.memberAxes already draws for map/weave's --workspace. If
	// BOTH this and cfg.Workspace are empty (no explicit choice anywhere),
	// PrepareAgentChat now defaults a delegated child to worktree rather
	// than the shared checkout — an explicit "none" at either level still
	// wins. See PrepareAgentChat's workspace-resolution comment for the
	// scope of what worktree isolation does and does not cover.
	Workspace string
	// MCPServers is the composed managed set for the child session (the
	// chat paths never run Setup, so the servers ride the session).
	MCPServers []agent.ChatMCPServer
	// Gate is the shared executable trust gate for per-turn managed assembly
	// on the oneshot fallback path.
	Gate      bundles.ContentGate
	Verbosity int
	// Factory overrides plugin construction (test seam). Exactly like the
	// fan: a non-nil Factory skips isolation entirely.
	Factory pb.ClientFactory
	// Git overrides the git DI seam the dirty-parent-tree spawn gate uses
	// (test seam, mirrors task_triggers.go's Git field); nil selects the
	// real binary (git.NewExec()).
	Git git.Git
}

// AgentChatLaunch is a launched child: turns go in on In (plain text — the
// launch prepends the lead context to the first turn itself), normalized
// events come out on Events (a Complete marks each turn boundary), and Errs
// carries the stream's terminal error. Close kills the engine and tears down
// the workspace; the orchestrator closes In (its exclusive writer) first.
type AgentChatLaunch struct {
	In     chan<- agent.ChatMessage
	Events <-chan agent.ChatEvent
	Errs   <-chan error
	// Oneshot marks the no-structured-chat fallback: each turn ran as an
	// independent oneshot and the child engine has no bus reach-back, so the
	// orchestrator bridges each turn's output to the parent's mailbox.
	Oneshot bool
	Close   func()
}

// PreparedAgentChat is the isolation-resolved half of a child launch. The
// split exists for strictness-window hygiene: Prepare runs the
// checkpoint→isolation.Prepare→gate window (so the caller can serialize it
// against its own windows), Start spawns the engine outside any lock.
type PreparedAgentChat struct {
	cfg         *config.Config
	req         AgentChatRequest
	contextText string
	axes        isolation.Axes
	oneshot     bool

	// chat-path only (nil/empty on the oneshot fallback, which prepares its
	// isolation per turn inside runResolvedAgent):
	factory      pb.ClientFactory
	workDir      string
	workspaceEnv map[string]string
	cleanup      func()
}

// PrepareAgentChat resolves how the child will run: the structured-chat path
// for backends with the capability (isolation prepared once, engine kept
// alive across turns), or the oneshot fallback for backends without it. On
// the chat path this runs the same fail-loudly member gate as the fan —
// checkpoint before isolation.Prepare, refuse when an explicitly-requested
// container degraded (ClassIsolation finding) unless degraded mode. No
// external serialization needed: strictness gives each goroutine's window its
// own findings log (bumpy-tree).
func PrepareAgentChat(ctx context.Context, cfg *config.Config, req AgentChatRequest) (*PreparedAgentChat, error) {
	rs := req.Resolved
	// GAP 2: the caller's per-agent_run workspace choice overrides the
	// project default; empty (no override supplied) falls back to
	// cfg.GetWorkspace().
	//
	// DELEGATED-CHILD DEFAULT (this is the one place that default is
	// decided — internal/cli/run.go's top-level `ctxloom run` axes are a
	// separate call site untouched by this function): when NEITHER the
	// caller NOR the project config says anything explicit, a delegated
	// child now defaults to its OWN worktree instead of inheriting the
	// none/shared-checkout default the top-level run still uses. An
	// explicit choice at either level — a per-call Workspace or a project
	// `workspace:` setting, "none" or "worktree" — always wins; this only
	// fills the silence.
	//
	// This is a WORKSPACE-axis (file-level) change only. It narrows a
	// delegated child's blast radius on the PROJECT CHECKOUT — it does NOT
	// isolate the engine's global config/credential/conversation store,
	// which some engines keep outside any per-agent config-home env lever
	// entirely (see EnvWorkspace's doc and the antigravity fail-loud gate
	// in isolation.go). Do not read a worktree default as "delegated
	// children are now sandboxed from the user's engine state" — they are
	// not.
	workspace := cfg.GetWorkspace()
	if req.Workspace != "" {
		workspace = req.Workspace
	} else if workspace == "" {
		workspace = string(isolation.WorkspaceWorktree)
	}
	p := &PreparedAgentChat{
		cfg:         cfg,
		req:         req,
		contextText: req.Context,
		axes: isolation.Axes{
			Workspace: isolation.WorkspaceAxis(workspace),
			Runtime:   isolation.RuntimeAxis(rs.Runtime),
		},
	}
	if p.contextText == "" {
		p.contextText = rs.Context
	}

	// DIRTY-PARENT-TREE SPAWN GATE (see checkParentTreeForWorktreeSpawn's
	// doc): a worktree checkout only ever sees COMMITTED state, so a
	// delegated child spawned into one while the parent tree carries
	// uncommitted changes would silently run against stale or missing
	// content. Gated here, once, for BOTH the structured-chat path and the
	// oneshot fallback below (both derive from this same axes resolution) —
	// this is a SPAWN-time refusal, not a per-turn one.
	if p.axes.Workspace == isolation.WorkspaceWorktree {
		gitClient := req.Git
		if gitClient == nil {
			gitClient = git.NewExec()
		}
		if err := checkParentTreeForWorktreeSpawn(ctx, gitClient, req.WorkDir, rs.Name); err != nil {
			return nil, err
		}
	}

	if _, ok := backends.Get(rs.Backend).(agent.StructuredChat); !ok {
		p.oneshot = true
		return p, nil
	}

	if err := resolveChatModel(rs); err != nil {
		return nil, err
	}

	p.factory = req.Factory
	p.workDir = req.WorkDir
	if p.factory == nil {
		mark := strictness.Checkpoint()
		policy, ws := prepareIsolation(ctx, p.axes, rs.Backend, IsolationImageConfig(cfg, rs.Backend), req.WorkDir, rs.Name, isolation.SessionStateFromEnv(req.Env))
		found := strictness.Since(mark)
		strictness.Close(mark)
		p.workDir = ws.Dir()
		p.workspaceEnv = isolation.WorkspaceEnv(ws)
		p.cleanup = func() { _ = ws.Cleanup() }
		if gerr := isolationGateErr(found); gerr != nil {
			p.Abort()
			return nil, gerr
		}
		p.factory = isolation.FactoryForWorkspace(policy, ws, req.RunnerEnv)
	}
	return p, nil
}

// resolveChatModel resolves a delegated child's model into the concrete,
// ACP/API-shaped id its Chat spawn requires, MUTATING rs.Model in place so
// Start's ChatRequest (and any future reader of the resolved agent) sees the
// resolved value with no further plumbing. Only claude-code needs this: its
// Chat spawn rides the ACP/API path (internal/claude.ResolveModel), which
// rejects both an unset model — silently inheriting the user's saved
// INTERACTIVE default (e.g. an alias like "fable") — and a bare interactive
// nickname; either dies at session/new with an opaque -32603. Other backends'
// models pass through untouched: their Chat spawns don't share claude's ACP
// nickname rejection.
//
// This IS the delegated child's backend config assembly step — PrepareAgentChat
// assembles the ChatRequest Start hands the spawned engine, and this runs
// before any of that is built. It deliberately lives here rather than in
// agentcoord/coord/spawner.go: an analogous fail-loud gate already lives
// there (headlessSafePermission, checkpointed inside Resolve), and this check
// is its natural sibling — but spawner.go is under concurrent development on
// a sibling slice (Wave B1.6), so the check sits one layer down instead,
// upstream in operations, gating the exact same spawn moment (Start calls
// straight through here with nothing in between).
//
// Wave C3 confirmed this stays claude-only: codex/kiro have no interactive-
// nickname table to mis-resolve (claude's is the ONE alias layer in this
// codebase), and both adapters accept a raw configured model string
// verbatim through their own delivery mechanism (codex: -c model=<value>;
// kiro: --model <value> — see internal/codex/chat.go, internal/kiro/chat.go)
// with no silent-fallback failure mode analogous to claude's opaque -32603.
// An empty model on either simply falls through to the adapter's own
// configured default rather than a wrong one.
func resolveChatModel(rs *ResolvedAgent) error {
	if rs.Backend != config.BackendClaudeCode {
		return nil
	}
	model, ok := claude.ResolveModel(rs.Model)
	if ok {
		rs.Model = model
		return nil
	}
	fixIt := fmt.Sprintf("pin model: on llm config label %q or agent %q in .ctxloom/config.yaml", rs.Label, rs.Name)
	strictness.Fail(strictness.ClassConfig, fixIt,
		"agent_run: agent %q (llm label %q) resolves no model the ACP/API path accepts (got %q): an empty model silently inherits your saved interactive default, and a bare interactive alias (e.g. \"fable\") is rejected at chat-open",
		rs.Name, rs.Label, rs.Model)
	if strictness.Degraded() {
		return nil // degraded: launch anyway with whatever was configured (rs.Model unchanged)
	}
	return fmt.Errorf("agent %q: no ACP-resolvable model for its delegated claude chat (llm label %q, got %q); %s", rs.Name, rs.Label, rs.Model, fixIt)
}

// maxDirtyFilesListed bounds how many uncommitted paths
// checkParentTreeForWorktreeSpawn's refusal names before collapsing the rest
// into "+N more". An agent worktree routinely carries dozens of modified
// delivered-surface files (regenerated docs, generated schemas) across a long
// coordinator session; a wall of paths would bury the two sentences that
// actually matter — what broke and how to fix it.
const maxDirtyFilesListed = 10

// dirtyWorktreeSpawnFixIt is this gate's strictness Finding.FixIt (the
// abort-listing's per-finding remedy line) — kept as the SAME sentence as the
// tail of the returned error's own text, so a --degraded run's finding
// listing and a strict run's refusal never describe two different ways out.
const dirtyWorktreeSpawnFixIt = `commit the changes, or pass workspace: "none" for this agent_run call to run it against the live checkout instead`

// checkParentTreeForWorktreeSpawn refuses a delegated spawn that would land
// in worktree isolation while the PARENT project tree (workDir — the
// coordinator's own checkout, req.WorkDir; never the child's future
// workspace) carries uncommitted changes.
//
// WHY THIS GATE EXISTS: worktree isolation runs `git worktree add --detach
// <ref>` (isolation.NewWorktree, internal/lm/isolation/worktree.go) — a
// checkout of COMMITTED state only, HEAD and everything reachable from it.
// A coordinator that drafts a file and then hands the work to a delegated
// child would otherwise get a child that silently runs against stale or
// missing content: exit 0, a plausible transcript, wrong bytes. That is this
// project's signature failure mode (see silent-no-op-failure-mode), and
// worktree-by-default would introduce it deliberately if nothing caught it
// here. A loud refusal beats a silently stale child.
//
// WHAT COUNTS AS DIRTY: git's own notion of it — `git status --porcelain`
// (git.Git.IsDirty / WorkingChanges), which already honors BOTH the tracked
// .gitignore and the repo's .git/info/exclude. This is deliberately NOT a
// bespoke ignore-pattern allowlist reimplementing "what counts as noise":
// this codebase's own per-agent worktree preparation already writes the
// delivered-surface noise that would otherwise make this gate unusable
// (.mcp.json, .claude/, .agents/, .codex/config.toml, .kiro/,
// .ctxloom/cache/) into the shared common-dir .git/info/exclude
// (gitignore.WorktreeArtifactPatterns, written by
// internal/lm/isolation/worktree.go's Worktree.excludeConfigFromMerge), and the tracked
// .gitignore separately covers .codex/* (config.toml excepted), .opencode/,
// and the generated living-docs journeys. Once a repo has prepared even ONE
// agent worktree, that noise is invisible to `git status --porcelain` for
// every tree sharing the repo's common dir — including the parent's, which
// is exactly the tree this gate inspects. Reusing git's own porcelain check
// (the SAME mechanism the worktree teardown already trusts to decide
// WIP-safety, git.Git.IsDirty's doc) means this gate's notion of "noise"
// can never drift from the isolation layer's own, and the fix for a new kind
// of noise is the existing, already-used lever (extend the ignore/exclude
// patterns) rather than a second allowlist to keep in sync.
//
// Tracked modifications always count — there is no noise case for those.
// Untracked files count too, but ONLY when git itself would not already
// call them ignored/excluded: an untracked file `git status --porcelain`
// still reports is exactly the case a worktree checkout would silently
// drop — the same content a bare `git add` would pick up.
//
// Best-effort on a git failure (no binary, workDir not a repo — some test
// doubles pass a bare temp dir): never blocks the spawn, matching how the
// isolation chain's OWN git checks degrade (chainFor's worktree branch
// degrades silently to None on a non-repo dir rather than failing the run).
//
// Respects --degraded like every other startup gate (isolationGateErr,
// chainFor's ClassIsolation findings, resolveChatModel above): the finding
// still records and streams as a warning, but the spawn proceeds.
func checkParentTreeForWorktreeSpawn(ctx context.Context, gitClient git.Git, workDir, agentName string) error {
	dirty, err := gitClient.IsDirty(ctx, workDir)
	if err != nil || !dirty {
		return nil
	}
	changes, cerr := gitClient.WorkingChanges(ctx, workDir, 0)
	var listed []string
	var more int
	if cerr == nil {
		listed = changes
		if len(listed) > maxDirtyFilesListed {
			more = len(listed) - maxDirtyFilesListed
			listed = listed[:maxDirtyFilesListed]
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: refusing to spawn agent %q into a worktree — %s has uncommitted changes a worktree checkout cannot see (git worktree add checks out committed state only: HEAD and everything reachable from it, nothing you haven't committed yet). These changes would be invisible to the child:\n", agentName, workDir)
	for _, c := range listed {
		fmt.Fprintf(&b, "  %s\n", c)
	}
	if more > 0 {
		fmt.Fprintf(&b, "  (+%d more)\n", more)
	}
	b.WriteString(dirtyWorktreeSpawnFixIt)
	msg := b.String()

	strictness.Fail(strictness.ClassIsolation, dirtyWorktreeSpawnFixIt, "%s", msg)
	if strictness.Degraded() {
		return nil // degraded: proceed on the worktree anyway, warning only
	}
	return errors.New(msg)
}

// Abort tears down a prepared-but-never-started launch's workspace.
func (p *PreparedAgentChat) Abort() {
	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// AgentEngineProcess is a spawned-but-not-chatting engine runner: the child's
// `llm serve` process (or container) is up, isolation-prepared, and — with the
// coordinator trio on its env — dialing home, but the go-plugin Chat stream
// was never opened. The StartRun path's spawn half: engine control arrives
// over the runner's own RunnerChannel; go-plugin here is only the process
// spawn/kill transport.
type AgentEngineProcess struct {
	// WorkDir is the isolation-resolved workspace directory (what Chat's
	// ChatRequest.WorkDir would have carried).
	WorkDir string
	// Env is the harness env the legacy Chat path would have sent: the
	// workspace env merged under the request's extra env (ambient identity).
	Env map[string]string
	// Model is the RESOLVED model (post resolveChatModel — never an alias).
	Model string
	// Kill tears the engine process and its workspace down (idempotent).
	Kill func()
}

// StartEngine spawns the engine runner process WITHOUT opening the go-plugin
// Chat stream — the StartRun cutover's spawn half (Wave C1). The factory call
// completes the go-plugin handshake eagerly, so a returned handle means the
// runner process is up (and, with the coordinator trio stamped on it, dialing
// home). Refused on the oneshot fallback: there is no persistent engine
// process to host a StartRun.
func (p *PreparedAgentChat) StartEngine(context.Context) (*AgentEngineProcess, error) {
	if p.oneshot {
		return nil, errors.New("delegate: this backend has no structured chat; the oneshot fallback hosts no engine process for StartRun")
	}
	rs := p.req.Resolved
	client, err := p.factory(rs.Backend, rs.Label, p.req.Verbosity)
	if err != nil {
		p.Abort()
		return nil, err
	}
	env := p.workspaceEnv
	if len(p.req.Env) > 0 {
		merged := make(map[string]string, len(env)+len(p.req.Env))
		maps.Copy(merged, env)
		maps.Copy(merged, p.req.Env)
		env = merged
	}
	var once sync.Once
	return &AgentEngineProcess{
		WorkDir: p.workDir,
		Env:     env,
		Model:   rs.Model,
		Kill: func() {
			once.Do(func() {
				client.Kill()
				p.Abort()
			})
		},
	}, nil
}

// Start spawns the child and opens its turn stream. ctx bounds the child's
// whole lifetime (the orchestrator's, not one tool call's).
//
// Wave C4 KILL-LIST VERIFICATION (R13-scoped): the branch below is the
// delegated-child go-plugin Chat dial. It was NOT deleted — grepping
// coord/children.go's two call sites (runChild, resumeChild) shows it is
// reached ONLY when `!(plan.ViaStartRun && url != "")`, i.e. exactly two
// documented, intentional cases: (a) a StructuredChat backend outside the
// coordinator's ViaStartRun allowlist (coord/spawner.go's
// viaStartRunBackends — today, no production backend; only test doubles),
// and (b) C1's documented degraded-mode no-reach-back spawn fallback (a
// StartRun-eligible backend launched with CTXLOOM_DEGRADED=1 and no
// coordinator endpoint reachable — the runner could never dial home, so
// StartRun is impossible and this is the only way the child launches at
// all). Both are real, reachable, and intentional — this is NOT the general
// delegated-child path anymore (claude/codex/kiro/acp with reach-back always
// ride StartRun), so it stays, narrowly scoped and documented as such.
func (p *PreparedAgentChat) Start(ctx context.Context) (*AgentChatLaunch, error) {
	if p.oneshot {
		return p.startOneshot(ctx), nil
	}

	rs := p.req.Resolved
	client, err := p.factory(rs.Backend, rs.Label, p.req.Verbosity)
	if err != nil {
		p.Abort()
		return nil, err
	}

	env := p.workspaceEnv
	if len(p.req.Env) > 0 {
		merged := make(map[string]string, len(env)+len(p.req.Env))
		maps.Copy(merged, env)
		maps.Copy(merged, p.req.Env)
		env = merged
	}
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir:     p.workDir,
		Model:       rs.Model,
		Env:         env,
		Permissions: p.req.Permissions,
		MCPServers:  p.req.MCPServers,
	})
	if err != nil {
		client.Kill()
		p.Abort()
		return nil, err
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	closeFn := func() {
		closeOnce.Do(func() {
			close(done)
			client.Kill()
			p.Abort()
		})
	}
	return &AgentChatLaunch{
		In:     leadContextIn(in, p.contextText, done),
		Events: events,
		Errs:   errs,
		Close:  closeFn,
	}, nil
}

// leadContextIn wraps a chat input channel so the composed agent context
// rides the FIRST turn as a lead block (the chat substrate never runs Setup,
// so this is the context delivery — same contract as the ACP first-turn lead
// block). done unblocks a pending forward when the launch is torn down.
func leadContextIn(in chan<- agent.ChatMessage, lead string, done <-chan struct{}) chan<- agent.ChatMessage {
	wrapped := make(chan agent.ChatMessage)
	go func() {
		defer close(in)
		first := true
		for {
			var msg agent.ChatMessage
			var ok bool
			select {
			case msg, ok = <-wrapped:
				if !ok {
					return
				}
			case <-done:
				return
			}
			if first && msg.Text != "" {
				if lead != "" {
					msg.Text = lead + "\n\n" + msg.Text
				}
				first = false
			}
			select {
			case in <- msg:
			case <-done:
				return
			}
		}
	}()
	return wrapped
}

// startOneshot adapts backends WITHOUT structured chat to the launch shape:
// each inbound turn runs as an independent oneshot through the fan's launch
// tail (runResolvedAgent — per-turn isolation window, headless permission
// floor), its captured stdout emitted as an assistant entry + turn Complete.
// There is no session continuity between turns beyond the composed context.
func (p *PreparedAgentChat) startOneshot(ctx context.Context) *AgentChatLaunch {
	rs := p.req.Resolved
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	turnCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(errs)
		defer close(events)
		for msg := range in {
			if msg.Text == "" {
				continue
			}
			res, err := runResolvedAgent(turnCtx, resolvedRunRequest{
				Context:        p.contextText,
				Task:           msg.Text,
				WorkDir:        p.req.WorkDir,
				Verbosity:      p.req.Verbosity,
				Label:          rs.Label,
				Backend:        rs.Backend,
				Model:          rs.Model,
				Permissions:    p.req.Permissions.String(),
				Axes:           p.axes,
				IsolationImage: IsolationImageConfig(p.cfg, rs.Backend),
				AgentID:        rs.Name,
				Profiles:       rs.Profiles,
				Gate:           p.req.Gate,
				Factory:        p.req.Factory,
				ExtraEnv:       p.req.Env,
			})
			if err != nil {
				events <- agent.ChatEvent{Entry: &agent.SessionEntry{
					Type: agent.EntryTypeSystem, Content: "delegated turn failed: " + err.Error(), IsError: true,
				}}
				events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "error"}}
				continue
			}
			events <- agent.ChatEvent{Entry: &agent.SessionEntry{
				Type: agent.EntryTypeAssistant, Content: res.Output,
			}}
			events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
		}
	}()
	return &AgentChatLaunch{In: in, Events: events, Errs: errs, Oneshot: true, Close: cancel}
}
