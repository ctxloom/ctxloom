package operations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// ResumeSessionID, when set, asks the backend to resume its own NATIVE
	// session (agent.ChatRequest.ResumeSessionID — claude --resume, codex
	// thread/resume, ACP session/load)
	// instead of starting fresh. Only meaningful on the legacy go-plugin
	// Chat dial (Start, below) — the migrated StartRun path resumes via its
	// own HarnessSpec/StartRun{ResumeSessionId} route (children.go's
	// runChildViaStartRun) and never reads this field. Empty means "no
	// captured native id" (fresh session, or the backend doesn't emit one
	// yet — see internal/opencode) — Start leaves ChatRequest.
	// ResumeSessionID empty exactly as before this field existed, so a
	// caller that never sets it observes no behavior change.
	ResumeSessionID string
	// Workspace is the caller's per-invocation workspace-axis override
	// (isolation.WorkspaceAxis values: "none"|"worktree"; GAP 2). It OVERRIDES
	// cfg.Workspace when set; empty falls back to cfg.Workspace — the same
	// session-level-orchestration-trait-vs-agent-trait split the workspace axis
	// draws everywhere it's resolved. If BOTH this and cfg.Workspace are empty
	// (no explicit choice anywhere),
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
	Gate      bundles.Authorizer
	Verbosity int
	// Factory overrides plugin construction (test seam). Exactly like the
	// fan: a non-nil Factory skips isolation entirely.
	Factory pb.ClientFactory
	// Starter overrides the StartRun spawn-half construction (test seam,
	// parallel to Factory): a non-nil Starter lets a spawner test fake
	// StartEngine's docker-direct/bare-host runner launch without a real
	// container. Empty on the production path, where PrepareAgentChat binds
	// the resolved policy's isolation.StarterForWorkspace instead.
	Starter isolation.EngineStarter
	// Git overrides the git DI seam the dirty-parent-tree spawn gate uses
	// (test seam, mirrors task_triggers.go's Git field); nil selects the
	// real binary (git.NewExec()).
	Git git.Git
	// DirtyTreeHandler is the caller's per-call override of what happens
	// when this spawn resolves to worktree isolation while the parent tree
	// is dirty (see handleDirtyParentTree). It arrives ALREADY PARSED: the
	// surface that accepted the caller's spelling (coord's serveSpawnAgent
	// for the wire, the MCP tool handler for the native one) converted it
	// through ParseDirtyTreeHandler, so an unrecognized value is refused
	// where the caller can still see their own typo — never carried this
	// far as a string for some later frame to interpret.
	// The zero value defers to cfg.GetDirtyTreeHandler(), then to the
	// built-in default ("commit") — the identical three-tier precedence
	// Workspace above uses. Mirrors agent_run's "dirty_tree_handler"
	// parameter.
	//
	// Deliberately NOT where dirty_tree_commit_ack lives: that is a
	// per-PROJECT, human-only config acknowledgement (never a per-call
	// field) — see config.Config.dirtyTreeCommitAck's doc for why. This
	// field only ever selects WHICH handler runs, never authorizes the
	// commit handler's mutation.
	DirtyTreeHandler DirtyTreeHandler
	// ChatDialTimeout bounds Start's legacy go-plugin Chat dial (client.Chat,
	// below): the ONLY per-attempt budget on this path today besides plain ctx
	// cancellation. Zero (the normal case — no production caller sets this)
	// uses the package default, defaultChatDialTimeout; this field exists so a
	// test can inject a short budget instead of waiting out the real one. See
	// Start's doc for why the bound races the call rather than deriving a
	// child context from ctx: client.Chat's ctx parameter governs the WHOLE
	// returned stream's lifetime (a successful dial must keep using the
	// caller's own ctx, unbounded, for the life of the conversation), so a
	// context.WithTimeout wrapper cannot be cancelled once the dial succeeds
	// without also killing the stream it just opened.
	ChatDialTimeout time.Duration
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
	factory      pb.ClientFactory        // legacy go-plugin Chat spawn (Start)
	starter      isolation.EngineStarter // StartRun spawn half (StartEngine)
	workDir      string
	workspaceEnv map[string]string
	cleanup      func()
	// chatDialTimeout is req.ChatDialTimeout resolved against
	// defaultChatDialTimeout (never zero) — see AgentChatRequest.ChatDialTimeout
	// and Start's doc.
	chatDialTimeout time.Duration
	// mcpCommandOverride is MCPCommandOverrideForPolicy's result for the
	// resolved policy — "" for none/worktree, the in-container
	// ctxloom binary path for a container policy. It is computed here,
	// AFTER req.MCPServers was already frozen by the caller (coord/
	// spawner.go's childMCPServers composes plan.MCPServers once at Resolve
	// time, before Launch/StartEngine ever call PrepareAgentChat and learn
	// the real policy), so Start applies it to req.MCPServers directly
	// below; StartEngine's caller has no equivalent hook into req, so it
	// reads this field back via MCPCommandOverride() and patches its own
	// copy (plan.MCPServers) the same way.
	mcpCommandOverride string
}

// MCPCommandOverride reports the resolved policy's MCP command override
// (see mcpCommandOverride's doc) — "" when none applies. StartEngine's
// caller (coord/spawner.go) needs this because it builds its EngineSpawn's
// MCPServers from plan.MCPServers directly rather than through Start, which
// already applies the patch internally.
func (p *PreparedAgentChat) MCPCommandOverride() string { return p.mcpCommandOverride }

// PrepareAgentChat resolves how the child will run: the structured-chat path
// for backends with the capability (isolation prepared once, engine kept
// alive across turns), or the oneshot fallback for backends without it. On
// the chat path this runs the same fail-loudly member gate as the fan —
// checkpoint before isolation.Prepare, refuse when an explicitly-requested
// container degraded (ClassIsolation finding) unless degraded mode. No
// external serialization needed: strictness gives each goroutine's window its
// own findings log.
func PrepareAgentChat(ctx context.Context, cfg *config.Config, req AgentChatRequest) (*PreparedAgentChat, error) {
	rs := req.Resolved
	axes, err := delegatedAxes(cfg, req)
	if err != nil {
		return nil, err
	}
	p := &PreparedAgentChat{
		cfg:             cfg,
		req:             req,
		contextText:     req.Context,
		axes:            axes,
		chatDialTimeout: resolveChatDialTimeout(req),
	}
	if p.contextText == "" {
		p.contextText = rs.Context
	}
	warnOnEmptyLeadContext(rs, p.contextText)

	// DIRTY-PARENT-TREE SPAWN HANDLING (see handleDirtyParentTree's doc): a
	// worktree checkout only ever sees COMMITTED state, so a delegated child
	// spawned into one while the parent tree carries uncommitted changes
	// needs an explicit decision — commit, copy, proceed stale, or refuse.
	// Decided here, once, for BOTH the structured-chat path and the oneshot
	// fallback below (both derive from this same axes resolution) — this is
	// a SPAWN-time decision, not a per-turn one. "copy" defers actual file
	// reproduction until the worktree exists (pendingCopy, applied below,
	// chat path only — see its doc).
	gitClient, pendingCopy, err := decideDirtyParentTree(ctx, cfg, req, p.axes)
	if err != nil {
		return nil, err
	}

	if _, ok := backends.Get(rs.Backend).(agent.StructuredChat); !ok {
		if pendingCopy != nil {
			return nil, fmt.Errorf(`dirty_tree_handler "copy": agent %q has no structured-chat backend (it runs the per-turn oneshot fallback, which prepares and tears down a fresh worktree every turn) — copy's one-time file reproduction has nowhere durable to land; pass dirty_tree_handler: "stale" or "fail" for this call instead`, rs.Name)
		}
		p.oneshot = true
		return p, nil
	}

	if err := resolveChatModel(rs); err != nil {
		return nil, err
	}

	p.factory = req.Factory
	p.starter = req.Starter
	p.workDir = req.WorkDir
	if p.factory == nil {
		if gerr := p.bindIsolatedSpawn(ctx, cfg); gerr != nil {
			return nil, gerr
		}
	}
	if pendingCopy != nil {
		if err := applyCopySnapshot(ctx, gitClient, p.workDir, pendingCopy); err != nil {
			p.Abort()
			return nil, err
		}
	}
	return p, nil
}

// warnOnEmptyLeadContext is the delegated child's zero-context floor. Both
// launch paths funnel through PrepareAgentChat, and both deliver whatever it
// resolved: the legacy Chat dial prepends it to the first turn (leadContextIn,
// which prepends "" without comment), StartRun joins it ahead of the prompt.
// Neither can tell "this agent composes nothing" from "ctxloom is working" —
// the child simply runs with no ctxloom bytes at all while the spawn reports
// success, which is this codebase's signature failure mode.
//
// Fault-tolerant by design (warn, never refuse): a legitimately context-free
// agent must still launch. resolveAgentBinding already warns for an agent
// declaring NO profiles; this covers the case it cannot see — profiles
// declared, assembly SUCCEEDED, and the composed result is empty. Once per
// distinct message, so a fan of ten children says it once.
func warnOnEmptyLeadContext(rs *ResolvedAgent, lead string) {
	if strings.TrimSpace(lead) != "" {
		return
	}
	profiles := "none declared"
	if len(rs.Profiles) > 0 {
		profiles = strings.Join(rs.Profiles, ", ")
	}
	clidiag.WarnOnce("ctxloom", "agent_run: agent %q launches with ZERO bytes of ctxloom context (profiles: %s) — the child runs with no composed context at all; check the agent's profiles with `ctxloom agent show %s`",
		rs.Name, profiles, rs.Name)
}

// delegatedAxes resolves the isolation axes one delegated child launches on.
//
// GAP 2: the caller's per-agent_run workspace choice overrides the project
// default; empty (no override supplied) falls back to cfg.GetWorkspace().
//
// DELEGATED-CHILD DEFAULT (this is the one place that default is decided —
// internal/cli/run.go's top-level `ctxloom run` axes are a separate call site
// untouched by this function): when NEITHER the caller NOR the project config
// says anything explicit, a delegated child defaults to its OWN worktree
// instead of inheriting the none/shared-checkout default the top-level run
// still uses. An explicit choice at either level — a per-call Workspace or a
// project `workspace:` setting, "none" or "worktree" — always wins; this only
// fills the silence.
//
// This is a WORKSPACE-axis (file-level) default only. It narrows a delegated
// child's blast radius on the PROJECT CHECKOUT — it does NOT isolate the
// engine's global config/credential/conversation store, which some engines
// keep outside any per-agent config-home env lever entirely (see
// EnvWorkspace's doc). Do not read a worktree default as "delegated children are now sandboxed from
// the user's engine state" — they are not.
//
// The RUNTIME axis carries the agent's own resolved choice through untouched:
// it is an agent trait, not an invocation one.
func delegatedAxes(cfg *config.Config, req AgentChatRequest) (isolation.Axes, error) {
	raw := cfg.GetWorkspace()
	if req.Workspace != "" {
		raw = req.Workspace
	} else if raw == "" {
		raw = string(isolation.WorkspaceWorktree)
	}
	// An unrecognized spelling REFUSES rather than degrading. This function
	// is the one that defaults a delegated child to its own worktree, so a
	// typo here does not merely fail to isolate: an unparsed value reads as
	// the shared checkout downstream, which drops the child into the
	// PARENT'S LIVE TREE — further from the default than saying nothing at
	// all, and past decideDirtyParentTree, which never runs on an axis that
	// is not WorkspaceWorktree.
	workspace, err := isolation.ParseWorkspaceAxis(raw)
	if err != nil {
		return isolation.Axes{}, fmt.Errorf("agent_run: %w", err)
	}
	// The runtime axis arrives ALREADY typed: ResolvedAgent.Runtime is what
	// resolveAgentBinding produced from agent.ParseRuntimeAxis, so a typo on
	// either of its two sources (the binding, the project default) is refused
	// before a child is ever prepared. Carrying the typed value through — not
	// re-converting it — keeps that the axis's only door.
	return isolation.Axes{
		Workspace: workspace,
		Runtime:   req.Resolved.Runtime,
	}, nil
}

// resolveChatDialTimeout applies the package default to an unset per-request
// budget — see AgentChatRequest.ChatDialTimeout and Start's doc. Never zero.
func resolveChatDialTimeout(req AgentChatRequest) time.Duration {
	if req.ChatDialTimeout <= 0 {
		return defaultChatDialTimeout
	}
	return req.ChatDialTimeout
}

// decideDirtyParentTree runs the DIRTY-PARENT-TREE SPAWN HANDLING decision
// (see handleDirtyParentTree's doc): a worktree checkout only ever sees
// COMMITTED state, so a delegated child spawned into one while the parent tree
// carries uncommitted changes needs an explicit decision — commit, copy,
// proceed stale, or refuse. Decided ONCE per spawn, for BOTH the
// structured-chat path and the oneshot fallback (both derive from the same
// axes resolution) — never per turn. "copy" defers actual file reproduction
// until the worktree exists, so it returns the captured snapshot plus the git
// client that captured it, for applyCopySnapshot to use later.
func decideDirtyParentTree(ctx context.Context, cfg *config.Config, req AgentChatRequest, axes isolation.Axes) (git.Git, *copySnapshot, error) {
	if axes.Workspace != isolation.WorkspaceWorktree {
		return nil, nil, nil
	}
	gitClient := req.Git
	if gitClient == nil {
		gitClient = git.NewExec()
	}
	handler, err := resolveDirtyTreeHandler(cfg, req.DirtyTreeHandler)
	if err != nil {
		return nil, nil, err
	}
	outcome, err := handleDirtyParentTree(ctx, cfg, gitClient, req.WorkDir, req.Resolved.Name, handler)
	if err != nil {
		return nil, nil, err
	}
	return gitClient, outcome.copy, nil
}

// bindIsolatedSpawn runs the checkpoint→isolation.Prepare→gate window and
// binds everything the resolved policy decides: the workspace directory and
// its env, the teardown, BOTH spawn halves, and the MCP command override. It
// runs only when no caller-supplied Factory has already replaced isolation
// wholesale. A gate finding tears the prepared workspace back down before
// returning.
func (p *PreparedAgentChat) bindIsolatedSpawn(ctx context.Context, cfg *config.Config) error {
	rs := p.req.Resolved
	mark := strictness.Checkpoint()
	policy, ws := prepareIsolation(ctx, p.axes, rs.Backend, IsolationImageConfig(cfg, rs.Backend), p.req.WorkDir, rs.Name, isolation.SessionStateFromEnv(p.req.Env))
	p.workspaceEnv = isolation.WorkspaceEnv(ws)
	// A delegated child is ALWAYS an agent run (p.req.Resolved IS the
	// binding), so on the none axis its EFFECTIVE config_home decides: only
	// rs.ConfigHome == "project" gets a project-scoped controlled config home
	// rather than the human's own ~/.claude / ~/.kiro — see
	// InTreeAgentHomeEnv. Resolved inside the checkpoint window so its
	// fail-loud finding lands in this spawn's own gate below.
	p.workspaceEnv = mergeInTreeAgentHome(p.workspaceEnv, InTreeAgentHome{
		Backend: rs.Backend,
		WorkDir: p.req.WorkDir,
		// The SESSION's harp, not this child's agent name: a delegated child
		// runs inside its parent's session and deliberately shares its config
		// -home instance. It rides p.req.Env under agent.SessionHarpEnv — the
		// same map isolation.SessionStateFromEnv reads two lines above.
		Harp:       p.req.Env[agent.SessionHarpEnv],
		ConfigHome: rs.ConfigHome,
		Policy:     policy,
		Env:        mergedEnvView(p.workspaceEnv, p.req.Env),
	})
	found := strictness.Since(mark)
	strictness.Close(mark)
	p.workDir = ws.Dir()
	p.cleanup = func() { _ = ws.Cleanup() }
	if gerr := isolationGateErr(found); gerr != nil {
		p.Abort()
		return gerr
	}
	p.factory = isolation.FactoryForWorkspace(policy, ws, p.req.RunnerEnv)
	// The StartRun spawn half (StartEngine) rides the docker-direct /
	// bare-host runner starter, NOT the go-plugin factory above (which stays
	// for the legacy Chat path, Start). A test-supplied req.Starter wins,
	// exactly as req.Factory does for the Chat path.
	if p.starter == nil {
		p.starter = isolation.StarterForWorkspace(policy, ws, rs.Backend, rs.Label, p.req.Verbosity, p.req.RunnerEnv)
	}
	// plan.MCPServers (req.MCPServers) was composed by the caller
	// BEFORE this policy was known — coord/spawner.go's childMCPServers
	// resolves it once at Resolve time, and the top-level `ctxloom run` path
	// stamps its own override earlier still (cli/run.go's
	// MCPCommandOverrideForPolicy call). Patch the auto-registered ctxloom
	// entry's Command now that the real policy is in hand, so a
	// runtime:container child's structured chat gets the in-container path
	// instead of the host self-exec path baked in at composition time.
	// p.mcpCommandOverride is also exposed via MCPCommandOverride() for
	// StartEngine's caller (coord/spawner.go), which builds its own
	// MCPServers copy outside of Start/p.req.
	p.mcpCommandOverride = MCPCommandOverrideForPolicy(policy)
	if p.mcpCommandOverride != "" {
		p.req.MCPServers = agent.PatchManagedCommand(p.req.MCPServers, p.mcpCommandOverride)
	}
	return nil
}

// resolveChatModel resolves a delegated child's model into the concrete,
// ACP/API-shaped id its Chat spawn requires, MUTATING rs.Model in place so
// Start's ChatRequest (and any future reader of the resolved agent) sees the
// resolved value with no further plumbing. Only claude-code needs this today:
// its Chat spawn rides the ACP/API path (internal/claude.ResolveModel, wired
// as claude-code's descriptor-level resolveModel hook — see
// backends.ResolveModelFor), which rejects both an unset model — silently
// inheriting the user's saved INTERACTIVE default (e.g. an alias like
// "fable") — and a bare interactive nickname; either dies at session/new with
// an opaque -32603. Other backends' models pass through untouched via
// ResolveModelFor's nil-hook default: their Chat spawns don't share claude's
// ACP nickname rejection.
//
// This used to branch on `rs.Backend != config.BackendClaudeCode` and call
// claude.ResolveModel directly — operations (the core) importing claude and
// branching on backend identity, a literal ADR-0026 violation. Routing
// through backends.ResolveModelFor (the injected, polymorphic seam ADR-0020
// already names for this) means a future backend with its own nickname table
// registers resolveModel once, in its own descriptor, with no operations-side
// edit — closing the gap the hardcoded branch left: a new backend's own
// alias-rejection failure mode got NO protection until someone remembered to
// widen this one `if`.
//
// This IS the delegated child's backend config assembly step — PrepareAgentChat
// assembles the ChatRequest Start hands the spawned engine, and this runs
// before any of that is built. It deliberately lives here rather than in
// agentcoord/coord/spawner.go: an analogous fail-loud gate already lives
// there (headlessSafePermission, checkpointed inside Resolve), and this check
// is its natural sibling — but spawner.go is under concurrent development on
// a sibling slice, so the check sits one layer down instead, upstream in
// operations, gating the exact same spawn moment (Start calls straight
// through here with nothing in between).
//
// This stays claude-only: codex/kiro have no interactive-nickname table to
// mis-resolve (claude's is the ONE alias layer in this codebase), and both
// adapters accept a raw configured model string
// verbatim through their own delivery mechanism (codex: -c model=<value>;
// kiro: --model <value> — see internal/codex/chat.go, internal/kiro/chat.go)
// with no silent-fallback failure mode analogous to claude's opaque -32603.
// An empty model on either simply falls through to the adapter's own
// configured default rather than a wrong one.
func resolveChatModel(rs *ResolvedAgent) error {
	model, ok := backends.ResolveModelFor(rs.Backend, rs.Model)
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

// maxDirtyFilesListed bounds how many uncommitted paths a dirty-tree message
// names before collapsing the rest into "+N more". An agent worktree
// routinely carries dozens of modified delivered-surface files (regenerated
// docs, generated schemas) across a long coordinator session; a wall of
// paths would bury the two sentences that actually matter — what's dirty and
// what ctxloom is about to do about it.
const maxDirtyFilesListed = 10

// Dirty-tree handler values — what a delegated agent_run spawn does when it
// resolves to worktree isolation while the PARENT project tree (workDir —
// the coordinator's own checkout; never the child's future workspace)
// carries uncommitted changes. Settable as a project config default
// (config.Config.GetDirtyTreeHandler, key `dirty_tree_handler`) and
// overridden per call by agent_run's `dirty_tree_handler` parameter — the
// identical two-tier precedence GAP 2's Workspace override already uses (see
// resolveDirtyTreeHandler).
//
// WHY THIS EXISTS AT ALL: worktree isolation runs `git worktree add --detach
// <ref>` (isolation.NewWorktree, internal/lm/isolation/worktree.go) — a
// checkout of COMMITTED state only, HEAD and everything reachable from it. A
// coordinator that drafts a file and then hands the work to a delegated
// child would otherwise get a child that silently runs against stale or
// missing content: exit 0, a plausible transcript, wrong bytes. That is this
// project's signature failure mode (see silent-no-op-failure-mode), and
// worktree-by-default would introduce it deliberately if nothing decided
// what to do about it. Four explicit choices, no refuse-or-degrade blur:
//
//   - DirtyTreeHandlerCommit ("commit", the DEFAULT): commit the parent's
//     dirty state first, so the child sees it. Gated behind a per-PROJECT
//     human acknowledgement (dirty_tree_commit_ack) — see commitDirtyTree.
//   - DirtyTreeHandlerCopy ("copy"): carve the worktree at HEAD as usual,
//     then reproduce the parent's uncommitted changes INSIDE it as
//     uncommitted WIP — nothing is ever committed to the parent's branch.
//   - DirtyTreeHandlerStale ("stale"): proceed against committed state only
//     (today's pre-existing behavior before this gate's original refusal
//     landed), warning that the child will not see the listed changes.
//   - DirtyTreeHandlerFail ("fail"): refuse the spawn outright (this gate's
//     original, sole behavior) — the message names the uncommitted paths and
//     the alternatives.
//
// --degraded (strictness.Degraded()) plays NO role in any of the four: which
// one runs is governed entirely by dirty_tree_handler. Overloading the
// global degraded flag here would silently convert "refuse/handle a dirty
// spawn deliberately" back into "hand the child stale content" via a flag
// set for unrelated startup-finding reasons — reintroducing the exact bug
// this gate exists to prevent. (resolveChatModel/isolationGateErr above DO
// still respect --degraded; that is unchanged and unrelated to this gate.)
//
// DirtyTreeHandler is a DEFINED TYPE, and every boundary that receives one of
// these spellings converts through ParseDirtyTreeHandler exactly once. The
// vocabulary reaches ctxloom from a channel typed by a MODEL (agent_run's
// free-form input Struct), and the fallback member WRITES TO THE USER'S
// REPOSITORY: an unrecognized spelling that resolved to the default would
// auto-commit the parent's working tree on the strength of a typo, past both
// the caller's and the project's explicit choice. Unset and unparseable are
// different inputs — unset takes the default below, unparseable stops.
type DirtyTreeHandler string

const (
	DirtyTreeHandlerCommit DirtyTreeHandler = "commit"
	DirtyTreeHandlerCopy   DirtyTreeHandler = "copy"
	DirtyTreeHandlerStale  DirtyTreeHandler = "stale"
	DirtyTreeHandlerFail   DirtyTreeHandler = "fail"
)

// DirtyTreeHandlerNames returns the recognized handler values, in the order
// they render into user-facing fix-it text and the wire schemas. Single
// source for every writer (ParseDirtyTreeHandler's own error text, the MCP
// tool schemas' enum) so none of them can drift from the vocabulary declared
// above.
func DirtyTreeHandlerNames() []string {
	return []string{
		string(DirtyTreeHandlerCommit),
		string(DirtyTreeHandlerCopy),
		string(DirtyTreeHandlerStale),
		string(DirtyTreeHandlerFail),
	}
}

// ParseDirtyTreeHandler is the ONE conversion between the dirty-tree-handler
// string vocabulary (project config, agent_run's per-call parameter on both
// MCP surfaces) and the typed DirtyTreeHandler. Every boundary that receives
// one parses it exactly once, here; past that parse only the typed value
// travels and nothing downstream re-interprets a string.
//
// Empty passes through as "" (the zero value), meaning "this level said
// nothing" — the caller applies its own precedence and lands on
// defaultDirtyTreeHandler. Any other unrecognized spelling is an ERROR naming
// the bad value and the legal ones. It never warns and never degrades: the
// default member commits the user's working tree, so a spelling nobody
// recognizes must stop the spawn rather than reach it.
func ParseDirtyTreeHandler(s string) (DirtyTreeHandler, error) {
	switch DirtyTreeHandler(s) {
	case "", DirtyTreeHandlerCommit, DirtyTreeHandlerCopy, DirtyTreeHandlerStale, DirtyTreeHandlerFail:
		return DirtyTreeHandler(s), nil
	default:
		return "", fmt.Errorf("unknown dirty_tree_handler %q (known: %s)", s, strings.Join(DirtyTreeHandlerNames(), "|"))
	}
}

// defaultDirtyTreeHandler is the built-in default when NEITHER the agent_run
// caller NOR the project config says anything explicit: "commit" (an empty
// cfg.GetDirtyTreeHandler() falls back to this).
const defaultDirtyTreeHandler = DirtyTreeHandlerCommit

// resolveDirtyTreeHandler applies GAP 2's precedence (per-call req wins,
// else the project config default, else the built-in default) — the exact
// same three-tier resolution PrepareAgentChat already runs for Workspace.
// req arrives ALREADY PARSED (the edge that accepted it — coord's
// serveSpawnAgent, the MCP tool handler — converted it); the project config
// is a raw string and is parsed here, the same way and with the same verdict.
//
// An unrecognized value at either level REFUSES the spawn. It cannot fall
// through to the built-in default: that default is the "commit" handler,
// which mutates the user's branch, and reaching it through a spelling nobody
// recognized routes around the very consent the commit handler is gated on
// (see commitDirtyTree's acknowledgement gate). A project that pinned "fail"
// and a caller who typo'd must land on a refusal, not on the one member that
// writes.
func resolveDirtyTreeHandler(cfg *config.Config, req DirtyTreeHandler) (DirtyTreeHandler, error) {
	if req != "" {
		return req, nil
	}
	handler, err := ParseDirtyTreeHandler(cfg.GetDirtyTreeHandler())
	if err != nil {
		return "", fmt.Errorf("agent_run: this project's dirty_tree_handler config default is unusable: %w — fix `dirty_tree_handler:` in .ctxloom/config.yaml, or pass a valid one on this call", err)
	}
	if handler == "" {
		return defaultDirtyTreeHandler, nil
	}
	return handler, nil
}

// dirtyTreeOutcome is what handleDirtyParentTree decided, for
// PrepareAgentChat to act on. Only the "copy" handler populates copy — its
// file reproduction is deferred until the worktree actually exists (see
// applyCopySnapshot's call site).
type dirtyTreeOutcome struct {
	copy *copySnapshot
}

// copySnapshot is the parent's dirty state captured at handleDirtyParentTree
// time (BEFORE the worktree is created), applied into the worktree once it
// exists. Captured once rather than re-derived later so there is exactly one
// read of "what's dirty" per spawn — no window for the parent tree to drift
// between the decision and the application.
type copySnapshot struct {
	// patch is `git diff HEAD` from the parent — tracked modifications and
	// deletions, ""  when there were none.
	patch string
	// untracked are the parent's untracked-but-not-ignored file paths
	// (relative to sourceDir) to copy verbatim — DiffPatch/git apply never
	// carries these; they need their own byte-for-byte reproduction.
	untracked []string
	// sourceDir is the parent directory untracked files are read FROM.
	sourceDir string
}

// dirtyFileList is the bounded file listing every dirty-tree message shows,
// together with the one fact a bare []string cannot carry: whether the
// listing that produced it succeeded. A WorkingChanges failure rendering as
// the empty set is a lie in exactly the messages that exist to say WHICH
// files are at stake — the refusal that claims uncommitted work would be
// invisible to the child, and the preview shown immediately before
// auto-committing the user's branch.
type dirtyFileList struct {
	listed []string
	more   int
	err    error
}

// boundDirtyChanges truncates changes to maxDirtyFilesListed, recording how
// many were dropped. A WorkingChanges error is CARRIED, not swallowed: every
// caller still proceeds (best-effort), but says plainly that it could not
// find out rather than showing an empty list. Takes the (changes, err) pair
// so a caller can forward git's return directly.
func boundDirtyChanges(changes []string, err error) dirtyFileList {
	if err != nil {
		return dirtyFileList{err: err}
	}
	out := dirtyFileList{listed: changes}
	if len(out.listed) > maxDirtyFilesListed {
		out.more = len(out.listed) - maxDirtyFilesListed
		out.listed = out.listed[:maxDirtyFilesListed]
	}
	return out
}

// writeTo appends the bounded file listing (one indented path per line, "+N
// more" tail) shared by every dirty-tree message, or the reason there is no
// listing to show.
func (d dirtyFileList) writeTo(b *strings.Builder) {
	if d.err != nil {
		fmt.Fprintf(b, "  (could not list the changed files: %v — the paths below are unknown, NOT empty)\n", d.err)
		return
	}
	for _, c := range d.listed {
		fmt.Fprintf(b, "  %s\n", c)
	}
	if d.more > 0 {
		fmt.Fprintf(b, "  (+%d more)\n", d.more)
	}
}

// handleDirtyParentTree is the dirty-tree handler dispatch: given that
// workDir (the PARENT project tree) is being checked ahead of a delegated
// spawn resolving to worktree isolation, decide (and where possible, act on)
// what handler says to do. WHAT COUNTS AS DIRTY is git's own notion of it —
// `git status --porcelain` (git.Git.IsDirty / WorkingChanges), which already
// honors BOTH the tracked .gitignore and the repo's .git/info/exclude. This
// is deliberately NOT a bespoke ignore-pattern allowlist reimplementing "what
// counts as noise": this codebase's own per-agent worktree preparation
// already writes the delivered-surface noise that would otherwise make this
// gate unusable (.mcp.json, .claude/, .agents/, .codex/config.toml, .kiro/,
// .ctxloom/cache/) into the shared common-dir .git/info/exclude
// (gitignore.WorktreeArtifactPatterns, written by
// internal/lm/isolation/worktree.go's Worktree.excludeConfigFromMerge), and
// the tracked .gitignore separately covers .codex/* (config.toml excepted),
// .opencode/, and the generated living-docs journeys. Once a repo has
// prepared even ONE agent worktree, that noise is invisible to `git status
// --porcelain` for every tree sharing the repo's common dir — including the
// parent's, which is exactly the tree this inspects. Reusing git's own
// porcelain check (the SAME mechanism the worktree teardown already trusts
// to decide WIP-safety, git.Git.IsDirty's doc) means this gate's notion of
// "noise" can never drift from the isolation layer's own.
//
// Tracked modifications always count. Untracked files count too, but ONLY
// when git itself would not already call them ignored/excluded.
//
// Best-effort on a git failure (no binary, workDir not a repo — some test
// doubles pass a bare temp dir): never blocks the spawn, matching how the
// isolation chain's OWN git checks degrade (chainFor's worktree branch
// degrades silently to None on a non-repo dir rather than failing the run).
func handleDirtyParentTree(ctx context.Context, cfg *config.Config, gitClient git.Git, workDir, agentName string, handler DirtyTreeHandler) (dirtyTreeOutcome, error) {
	dirty, err := gitClient.IsDirty(ctx, workDir)
	if err != nil || !dirty {
		return dirtyTreeOutcome{}, nil
	}
	files := boundDirtyChanges(gitClient.WorkingChanges(ctx, workDir, 0))

	switch handler {
	case DirtyTreeHandlerFail:
		return dirtyTreeOutcome{}, dirtyTreeFailError(agentName, workDir, files)

	case DirtyTreeHandlerStale:
		var b strings.Builder
		fmt.Fprintf(&b, "agent_run: agent %q is spawning into a worktree while %s has uncommitted changes (dirty_tree_handler: \"stale\") — the child will NOT see these changes, only committed state:\n", agentName, workDir)
		files.writeTo(&b)
		b.WriteString(`change dirty_tree_handler to "commit" or "copy" to carry these across, or pass workspace: "none" for this call to run against the live checkout instead`)
		clidiag.Warn("ctxloom", "%s", b.String())
		return dirtyTreeOutcome{}, nil

	case DirtyTreeHandlerCopy:
		patch, perr := gitClient.DiffPatch(ctx, workDir)
		if perr != nil {
			return dirtyTreeOutcome{}, fmt.Errorf(`dirty_tree_handler "copy": reading %s's tracked changes: %w`, workDir, perr)
		}
		untracked, uerr := gitClient.ListUntracked(ctx, workDir)
		if uerr != nil {
			return dirtyTreeOutcome{}, fmt.Errorf(`dirty_tree_handler "copy": listing %s's untracked files: %w`, workDir, uerr)
		}
		return dirtyTreeOutcome{copy: &copySnapshot{patch: patch, untracked: untracked, sourceDir: workDir}}, nil

	case DirtyTreeHandlerCommit:
		return dirtyTreeOutcome{}, commitDirtyTree(ctx, cfg, gitClient, workDir, agentName, files)

	default:
		// Unreachable through resolveDirtyTreeHandler, which parses before
		// dispatching here. It stays as a REFUSAL rather than a fallback to
		// the commit arm: a caller that reached this dispatch with a value
		// no parse admitted has said nothing this function may act on, and
		// the arm it would otherwise land on rewrites the user's branch.
		return dirtyTreeOutcome{}, fmt.Errorf("agent_run: dirty_tree_handler %q reached the dirty-tree dispatch unparsed (known: %s) — refusing to spawn rather than guess a handler that could commit %s", handler, strings.Join(DirtyTreeHandlerNames(), "|"), workDir)
	}
}

// dirtyTreeFailError renders the "fail" handler's refusal — this gate's
// original, only behavior before the other three handlers existed. Kept
// byte-for-byte compatible with that original message.
func dirtyTreeFailError(agentName, workDir string, files dirtyFileList) error {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: refusing to spawn agent %q into a worktree — %s has uncommitted changes a worktree checkout cannot see (git worktree add checks out committed state only: HEAD and everything reachable from it, nothing you haven't committed yet). These changes would be invisible to the child:\n", agentName, workDir)
	files.writeTo(&b)
	b.WriteString(`commit the changes, or pass workspace: "none" for this agent_run call to run it against the live checkout instead`)
	return errors.New(b.String())
}

// commitDirtyTreeAckKey names the acknowledgement the "commit" handler's
// ack-refusal message and warning point at — kept as a constant so the
// refusal text and any future doc/init-interview wiring name it identically.
// It is no longer a config.yaml key (see config.SetDirtyTreeCommitAck's doc);
// the name survives as the concept's identifier and as the on-disk store's
// own file stem (paths.DirtyTreeCommitAckFileName).
const commitDirtyTreeAckKey = "dirty_tree_commit_ack"

// commitDirtyTree implements dirty_tree_handler: "commit". It is gated
// TWICE, in order: (1) a coherence guard against auto-committing inside a
// detached-HEAD checkout (see the branch=="HEAD" case below), and (2) the
// per-checkout human acknowledgement (config.DirtyTreeCommitAcknowledged,
// read ONLY from its own admission-store file under .ctxloom/state/ — never
// req/agent-supplied data, and never the layered config chain, which has
// THREE channels (a home file, an environment variable, an argv) an agent
// can reach — by design: agent_run is normally invoked by a coordinator
// AGENT over MCP, in a process with no TTY, often while the human is away.
// An interactive prompt would either hang forever or be answered by an
// agent — which is not the user's consent. A durable, human-written
// acknowledgement record is the only form of prior consent that survives
// headless operation. DO NOT add a per-call override for this: a per-call
// parameter would let a delegating AGENT grant itself permission to commit
// on the user's behalf, which defeats the entire point — this must be a
// human act, done once, via `ctxloom init` or `ctxloom manage
// dirty-tree-ack`). Only once both pass does it warn (naming the branch and
// the bounded file list) and
// mutate. It never silently trusts a bare "commit succeeded": CommitAll's
// own before/after diff must show real content, or this refuses (see the
// len(changed)==0 case) — this codebase has a documented history of commits
// landing EMPTY due to an index-clobbering pre-commit-hook bug (since
// fixed), and a post-commit stat is not proof a commit landed.
func commitDirtyTree(ctx context.Context, cfg *config.Config, gitClient git.Git, workDir, agentName string, files dirtyFileList) error {
	branch, berr := gitClient.CurrentBranch(ctx, workDir)
	// An error here used to be discarded (`branch, _ :=`), leaving
	// branch=="" — which is NOT "HEAD", so the detached-HEAD guard below never
	// fired. An unresolvable branch name is exactly the condition this guard
	// exists to catch (the caller cannot tell whether this is a bare
	// checkout), so failing loud here is strictly safer than guessing.
	if berr != nil {
		return fmt.Errorf(`dirty_tree_handler "commit": could not determine %s's current branch: %w (this guard exists specifically to catch a detached-HEAD checkout, and an unresolvable branch name is the one condition it must not silently treat as safe) — pass dirty_tree_handler: "copy" or "stale" for this spawn instead, or "fail" to refuse it outright`, workDir, berr)
	}
	if branch == "HEAD" {
		return fmt.Errorf(`dirty_tree_handler "commit": %s is a detached-HEAD checkout (this looks like a delegated child's OWN isolated worktree, not a branch checkout — committing here would land on no branch and could be silently discarded when that worktree is later torn down) — pass dirty_tree_handler: "copy" or "stale" for this spawn instead, or "fail" to refuse it outright`, workDir)
	}

	if !config.DirtyTreeCommitAcknowledged(cfg.FS(), cfg.GetAppDir()) {
		var b strings.Builder
		fmt.Fprintf(&b, "agent_run: refusing to auto-commit for delegated agent %q on branch %q — %s has uncommitted changes, a worktree checkout only ever contains committed state, and dirty_tree_handler is configured to \"commit\" (the default), but this checkout has not acknowledged that ctxloom may commit on your behalf:\n", agentName, branch, workDir)
		files.writeTo(&b)
		fmt.Fprintf(&b, "\nThe \"commit\" handler WOULD stage and commit these to branch %q so the child could see them. To allow this, a human must run `ctxloom manage commit trust` (or answer yes to the dirty-tree question in `ctxloom init`) — this acknowledgement (%s) is a human act only; it cannot be set from config.yaml, an environment variable, or any per-call parameter.\n\n", branch, commitDirtyTreeAckKey)
		b.WriteString(`Or, for this call, choose a different handler instead: dirty_tree_handler: "copy" (reproduce these changes as uncommitted WIP inside the child's worktree — nothing committed), "stale" (spawn the child without these changes), "fail" (refuse the spawn outright).`)
		return errors.New(b.String())
	}

	preview := renderCommitPreview(agentName, workDir, branch, files)
	clidiag.Warn("ctxloom", "%s", preview)

	sha, changed, cerr := gitClient.CommitAll(ctx, workDir, renderCommitMessage(agentName))
	if cerr != nil {
		return fmt.Errorf(`dirty_tree_handler "commit": committing %s: %w`, workDir, cerr)
	}
	if len(changed) == 0 {
		return fmt.Errorf(`dirty_tree_handler "commit": commit %s on branch %q reports success but captured NO changed files versus its parent — refusing to spawn against what may be an empty commit (this codebase has a documented history of commits landing empty; inspect %s by hand before retrying)`, sha, branch, workDir)
	}
	return nil
}

// renderCommitPreview is the "commit" handler's warning — shown before every
// individual auto-commit, regardless of the standing project acknowledgement
// (the ack authorizes the BEHAVIOR CLASS once; this keeps each individual
// mutation visible). Names the branch, the bounded file list, that this is a
// configured-handler side effect (not an incidental one), and the
// alternatives.
func renderCommitPreview(agentName, workDir, branch string, files dirtyFileList) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_run: dirty_tree_handler \"commit\" is about to commit %s's uncommitted changes on branch %q so delegated agent %q can see them in its own git worktree (a worktree checkout only ever contains committed state):\n", workDir, branch, agentName)
	files.writeTo(&b)
	b.WriteString(`This is happening because dirty_tree_handler is configured to "commit" (the default, acknowledged via `)
	b.WriteString(commitDirtyTreeAckKey)
	b.WriteString(`, set by ctxloom init or ctxloom manage commit trust). Alternatives for this call: dirty_tree_handler "copy" (reproduce these changes as uncommitted WIP inside the child's worktree instead of committing them here), "stale" (spawn the child without these changes), "fail" (refuse the spawn instead of touching this branch); or pass workspace: "none" to run against the live checkout instead.`)
	return b.String()
}

// renderCommitMessage is the exact, self-explanatory commit message format
// for every dirty_tree_handler: "commit" auto-commit — readable in `git log`
// with no other context: WHO did it (ctxloom, automatically), WHY (so a
// named delegated agent's worktree could see it), and that it is a
// configured, reversible choice (naming the handler and its alternatives).
func renderCommitMessage(agentName string) string {
	return fmt.Sprintf(`ctxloom: auto-commit for delegated agent spawn (dirty_tree_handler=commit)

ctxloom committed this working tree automatically before spawning delegated
agent %q into its own git worktree: a worktree checkout only ever contains
committed state, so these changes would otherwise be invisible to the child.

This is the configured dirty_tree_handler behavior ("commit", the default).
Alternatives: "copy" (reproduce these changes as uncommitted WIP inside the
child's worktree instead of committing them here), "stale" (spawn the child
without these changes), "fail" (refuse the spawn instead of touching this
branch). Set dirty_tree_handler in .ctxloom/config.yaml, or pass it per call
to agent_run.
`, agentName)
}

// applyCopySnapshot reproduces snap (captured from the parent BEFORE the
// worktree existed) inside targetDir (the now-created worktree): the tracked
// patch first (git apply — modifications and deletions), then every
// untracked file verbatim. FAILS LOUDLY on any error, including a mid-loop
// untracked-file failure — it never half-applies and continues, matching
// CommitAll's own no-silent-partial-success contract.
func applyCopySnapshot(ctx context.Context, gitClient git.Git, targetDir string, snap *copySnapshot) error {
	if snap.patch != "" {
		applied, err := gitClient.ApplyPatch(ctx, targetDir, snap.patch)
		if err != nil {
			return fmt.Errorf(`dirty_tree_handler "copy": reproducing tracked changes into %s: %w`, targetDir, err)
		}
		// ApplyPatch reports applied=false (no error) for a patch
		// it considered empty/whitespace-only. snap.patch is non-empty here,
		// so applied=false means DiffPatch handed us content ApplyPatch does
		// not consider real — refuse rather than silently reproducing NONE
		// of the parent's tracked changes while reporting success.
		if !applied {
			return fmt.Errorf(`dirty_tree_handler "copy": %s's captured tracked-change patch was not applied (reported as empty) — refusing to proceed as if it had been`, targetDir)
		}
	}
	for _, rel := range snap.untracked {
		if err := copyUntrackedFile(snap.sourceDir, targetDir, rel); err != nil {
			return fmt.Errorf(`dirty_tree_handler "copy": reproducing untracked file %q into %s: %w`, rel, targetDir, err)
		}
	}
	return nil
}

// copyUntrackedFile reproduces one untracked entry from sourceDir/rel at
// targetDir/rel, creating any needed parent directories. `git ls-files
// --others` lists exactly two kinds of entry, and both are reproduced: a
// regular file (bytes + permission bits) and a SYMLINK (recreated as a link
// carrying the same target text — never dereferenced into a copy of what it
// points at, which would silently turn a link into a file and, for a link
// pointing outside the tree, bake a host path into the child's worktree).
// Anything else cannot be reproduced, and is refused rather than skipped:
// applyCopySnapshot's contract is that it never half-applies and continues.
func copyUntrackedFile(sourceDir, targetDir, rel string) error {
	src := filepath.Join(sourceDir, rel)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(targetDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, rerr := os.Readlink(src)
		if rerr != nil {
			return rerr
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file or symlink (mode %s) — ctxloom cannot reproduce it in the child's worktree", info.Mode().Type())
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
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
	// StderrTail reads the runner's bounded stderr tail without reaping it.
	// For a docker-direct runner this is the CONTAINER's streamed stderr —
	// the in-container `ctxloom llm host` process's, and (since internal/acp
	// tees the adapter's stderr to os.Stderr as well as its own ring) the
	// engine adapter's dying words too. It is the ONLY reason available when
	// the whole container dies WITHOUT the in-process EngineHost getting to
	// emit a FAILED RunCompleted — a docker-stop / OOM-kill (runner loss),
	// where the RunChannel simply disconnects. Nil-safe via
	// isolation.StderrTailOf. See isolation.RunnerHandle.StderrTail.
	StderrTail func() string
	// Wait blocks until the runner PROCESS exits and reports why (its error
	// already embeds the stderr tail — see both isolation.RunnerHandle.Wait
	// implementations). It is the DEATH signal a caller needs while it is
	// still waiting for that runner to dial home: readiness on this path is a
	// PUSH (the coordinator's awaitRunner parks on a channel the runner's
	// Hello closes), so without a death signal a runner that died a
	// millisecond after Start is indistinguishable from one that is merely
	// slow, and the caller pays its entire dial-home budget — minutes — before
	// it can say anything at all. Nothing consumed RunnerHandle.Wait before
	// this field existed (internal/lm/grpc/host_runner.go says so in as many
	// words), which is precisely why a dead runner surfaced only as a
	// readiness timeout, with the runner's own dying words never read.
	//
	// Both policies fill it and both are safe to call repeatedly and
	// concurrently: each merely reads the outcome of the ONE background reap
	// its handle already runs. Nil for a starter that captures no process (a
	// test double, or a future policy with nothing to reap), so every caller
	// nil-checks and degrades to its timeout rather than panicking.
	Wait func() error
}

// StartEngine spawns the engine runner process WITHOUT opening the go-plugin
// Chat stream — the StartRun cutover's spawn half, migrated off go-plugin
// entirely. It launches the runner via the
// starter seam (docker-direct `ctxloom llm host` for a container, a bare
// self-invoked `llm host` under setsid for a host) — NO plugin handshake, NO
// container plugin listener. A returned handle means the runner PROCESS is up;
// readiness (its dial-home) is the coordinator's awaitRunner, not this call.
// Refused on the oneshot fallback: there is no persistent engine process to
// host a StartRun.
func (p *PreparedAgentChat) StartEngine(ctx context.Context) (*AgentEngineProcess, error) {
	if p.oneshot {
		return nil, errors.New("delegate: this backend has no structured chat; the oneshot fallback hosts no engine process for StartRun")
	}
	if p.starter == nil {
		// The two spawn seams are independent: a caller-supplied Factory
		// (legacy Chat dial) skips the isolation block that BINDS the
		// production starter, so a caller taking the StartRun path with only
		// Factory set arrives here with nothing to launch. Refusing by name
		// beats the nil-func panic an exported method has no business raising.
		return nil, errors.New("delegate: no engine Starter for this launch — a caller-supplied AgentChatRequest.Factory replaces the isolation-bound starter, so the StartRun path needs AgentChatRequest.Starter supplied too")
	}
	rs := p.req.Resolved
	handle, err := p.starter(ctx)
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
		WorkDir:    p.workDir,
		Env:        env,
		Model:      rs.Model,
		StderrTail: func() string { return isolation.StderrTailOf(handle) },
		Wait:       isolation.WaitOf(handle),
		Kill: func() {
			once.Do(func() {
				handle.Kill()
				p.Abort()
			})
		},
	}, nil
}

// defaultChatDialTimeout is the package default budget for Start's legacy
// go-plugin Chat dial: client.Chat's blocking call to open the bidirectional
// stream against an already-spawned engine process (or container). It is the
// legacy path's mirror of coord/children.go's defaultRunnerAwaitTimeout (the
// migrated StartRun path's dial-home budget) — same underlying operational
// risk (host/container contention slowing a genuinely-succeeding spawn) —
// but it is its OWN named constant and its OWN Option (AgentChatRequest.
// ChatDialTimeout) rather than a reuse of that one: the two paths spawn
// differently (a push-style dial-home wait there vs. a direct blocking RPC
// call here) and are owned by different, actively-changing packages
// (agentcoord/coord vs. operations), so a future retune of one must not have
// to touch the other. 5 minutes matches runnerAwaitTimeout's just-recalibrated
// value (fix/launch-retry-budget) because nothing here suggests
// this path's spawn latency differs from that one's: both exclude image
// build/pull (staged earlier — PrepareAgentChat's own isolation.Prepare call
// for this path, StartEngine's for that one) and both are bounding "process/
// container comes up and finishes a handshake" under the same possible
// contention (loaded docker daemon, DinD nesting, a busy bridge network).
// Absent evidence the legacy path's engines (today: mock, or any
// StartRun-eligible backend launched --degraded) are faster or
// slower to spawn, matching the sibling path's just-tuned number is the
// defensible choice — not a copy-paste, an independent application of the
// same reasoning to the same class of wait.
const defaultChatDialTimeout = 5 * time.Minute

// Start spawns the child and opens its turn stream. ctx bounds the child's
// whole lifetime (the orchestrator's, not one tool call's).
//
// The client.Chat dial below is raced against p.chatDialTimeout rather than
// bounded by deriving a context.WithTimeout(ctx, ...) and passing THAT into
// client.Chat: client.Chat's ctx parameter is not a one-shot "setup" context
// that this function could safely cancel once the call returns — it governs
// the WHOLE returned stream's lifetime (GRPCClient.Chat binds the bidi stream
// to the exact ctx it was given, for as long as the conversation runs). A
// successful dial must keep using the caller's own long-lived ctx, completely
// unbounded, for the life of the conversation that follows; cancelling a
// derived timeout context immediately after a successful call would tear the
// just-opened stream straight back down. Racing the call in a goroutine (never
// touching ctx itself) lets a slow-but-succeeding dial complete normally while
// still failing loud, on a timer, when it never completes at all.
//
// KILL-LIST VERIFICATION: the branch below is the delegated-child go-plugin
// Chat dial. It was NOT deleted — grepping
// coord/children.go's two call sites (runChild, resumeChild) shows it is
// reached ONLY when `!(plan.ViaStartRun && url != "")`, i.e. exactly two
// documented, intentional cases: (a) a StructuredChat backend outside the
// coordinator's ViaStartRun allowlist (coord/spawner.go's
// viaStartRunBackends) — which, since the spool cutover's S3b slice migrated
// opencode onto StartRun, is the "mock" test backend ALONE; no production
// backend reaches this dial by backend identity any more.
// And (b) C1's documented degraded-mode no-reach-back spawn
// fallback (a StartRun-eligible backend launched with CTXLOOM_DEGRADED=1 and
// no coordinator endpoint reachable — the runner could never dial home, so
// StartRun is impossible and this is the only way the child launches at
// all). Both are real, reachable, and intentional — this is NOT the general
// delegated-child path anymore (claude/codex/kiro/acp WITH reach-back ride
// StartRun), so it stays, narrowly scoped and documented as such, and its
// client.Chat dial gets the same fail-loud bound StartRun's dial-home wait
// already has (defaultChatDialTimeout above).
//
// FROZEN (spool-cutover RETIRE-FIRST ruling): this dial and the driveChild
// loop that consumes its launch are retired-in-place — never ported to the
// file-spool messaging substrate that replaces the coordinator mailbox, and
// closed to new backends (coord/spawner.go's checkLegacyChatFreeze refuses
// any backend outside viaStartRunBackends + legacyChatBackends at Resolve).
// Its remaining consumers (mock, the degraded no-reach-back spawn) lose
// delegation when the mailbox deletes.
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

	in, events, errs, err := dialChat(ctx, client, rs.Backend, agent.ChatRequest{
		WorkDir:         p.workDir,
		Model:           rs.Model,
		Env:             env,
		Permissions:     p.req.Permissions,
		MCPServers:      p.req.MCPServers,
		ResumeSessionID: p.req.ResumeSessionID,
	}, p.chatDialTimeout)
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

// chatDialResult is dialChat's internal race payload: exactly the 4-tuple
// pb.Client.Chat returns, boxed so a single channel send can carry it.
type chatDialResult struct {
	in     chan<- agent.ChatMessage
	events <-chan agent.ChatEvent
	errs   <-chan error
	err    error
}

// dialChat races client.Chat(ctx, req) against timeout, giving Start's legacy
// go-plugin dial a fail-loud bound without ever cancelling the ctx client.Chat
// itself receives — see Start's doc for why a context.WithTimeout wrapper
// would be unsafe here (it would double as a kill switch on a successful, now
// long-lived stream). client.Chat runs in its own goroutine regardless of
// which arm of the select fires; resultCh is buffered (size 1) specifically
// so that goroutine can always deliver its result and exit even when nobody
// is left reading — a timeout/ctx.Done() return here is never a goroutine
// leak. On either failure arm, Start's caller kills `client` immediately
// (see Start, right after this call), which is what actually unblocks or
// fails whatever the dial was stuck on; a dial that later "succeeds" against
// an already-killed client cannot leak a live, unreferenced engine either —
// the process backing it is already torn down.
func dialChat(ctx context.Context, client pb.Client, backend string, req agent.ChatRequest, timeout time.Duration) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	resultCh := make(chan chatDialResult, 1)
	go func() {
		in, events, errs, err := client.Chat(ctx, req)
		resultCh <- chatDialResult{in: in, events: events, errs: errs, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-resultCh:
		return res.in, res.events, res.errs, res.err
	case <-timer.C:
		return nil, nil, nil, fmt.Errorf("agent_run: legacy chat dial for backend %q did not open within %s (client.Chat never returned — the engine process/container or its handshake may be stuck); check the runtime's process/container state and backend logs", backend, timeout)
	case <-ctx.Done():
		return nil, nil, nil, ctx.Err()
	}
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
//
// The returned launch winds down on EITHER of the two routes its orchestrator
// uses: In closing (the stream ended on its own) or Close (every other
// terminal cause). Both end the driver goroutine, which closes Events and
// Errs; Close never closes In, whose single closer is the orchestrator.
func (p *PreparedAgentChat) startOneshot(ctx context.Context) *AgentChatLaunch {
	rs := p.req.Resolved
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	turnCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(errs)
		defer close(events)
		for {
			var msg agent.ChatMessage
			var ok bool
			select {
			case msg, ok = <-in:
				if !ok {
					return
				}
			case <-turnCtx.Done():
				// Close() (= cancel) is the orchestrator's terminal lever for
				// every cause that is not "the stream ended on its own"
				// (agent_stop, launch failure, coordinator teardown). It must
				// wind this launch down on its own: the orchestrator closes In
				// only from the path that observes Events CLOSING, so waiting
				// for In here would park this goroutine for the rest of the
				// process's life. Close deliberately does NOT close In itself —
				// In has exactly one closer, the orchestrator.
				return
			}
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
				// The oneshot FALLBACK for a delegated child is still a
				// delegated child: same agent binding, same EFFECTIVE
				// config_home as the structured-chat path bindIsolatedSpawn
				// reads (rs.ConfigHome — already resolved/defaulted).
				ConfigHome: rs.ConfigHome,
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
