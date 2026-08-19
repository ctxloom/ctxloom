package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/mcp"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/vpio"
	"github.com/ctxloom/ctxloom/internal/vpio/dockerexec"
	"github.com/ctxloom/ctxloom/internal/vpio/goplugin"
)

var (
	runLLM         string
	runAgent       string
	runWorkspace   string
	runPermissions string
	runPrompt      string
	runFragments   []string
	runTags        []string
	runProfile     string
	runSavedPrompt string // --command / -r
	runDryRun      bool
	// runOneShot selects the single-turn mode: one turn, the answer, exit. The
	// name is the MODE, not its output — printing is what every mode does, and
	// it is the turn count that decides whether an engine gets a session. Same
	// word the mode carries the rest of the way down (agent.CLISurfaceOneshot,
	// operations.RunOneshot, pb.ExecutionMode_ONESHOT).
	runOneShot       bool
	runPlainTerminal bool
	runVerbosity     int
	runAssumeYes     bool
	runSeedTask      string
	runSeedStatus    string
	// runResumeSession/runResumeDistill are the two deterministic-resume flags
	//   --session <harp>            full resume: the harp's full recorded
	//                                transcript is folded into THIS run's
	//                                assembled context (resumeFullContext).
	//   --session <harp> --distill  distilled resume: the harp's essence
	//                                (distilling on demand if missing) rides
	//                                the CTXLOOM_RESUMED_FROM/PARTS + SessionStart
	//                                -hook essence path (resumeDistillEnv).
	runResumeSession string
	runResumeDistill bool
)

// dryRunJSON is the --format json shape for `run --dry-run`: the resolved
// assembly a profile/flag set produces. The VSCode profile composer reads this
// to preview the effective context (fully inheritance-resolved) without running
// the agent.
type dryRunJSON struct {
	// Agent names the --agent binding this preview resolved (empty for the
	// classic profile flow); Workspace/Runtime are the session's resolved
	// isolation axes.
	Agent     string   `json:"agent,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Runtime   string   `json:"runtime,omitempty"`
	LLM       string   `json:"llm"`
	Backend   string   `json:"backend"`
	Profiles  []string `json:"profiles"`
	Fragments []string `json:"fragments"`
	Context   string   `json:"context"`
	// Tokens is the estimated token count of the assembled Context, computed by
	// the backend (internal/tokens) so a client previewing a profile reads one
	// authoritative estimate instead of re-deriving its own chars/token guess.
	Tokens int    `json:"tokens"`
	Prompt string `json:"prompt,omitempty"`
}

// orEmpty returns a non-nil slice so json renders [] rather than null.
func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// orDefault returns s, or def when s is empty — the axis-default rendering for
// the dry-run preview (an unset workspace/runtime means none/host).
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// execCommand is the seam tests override to avoid actually shelling
// out. Production points it at exec.CommandContext
var execCommand = exec.CommandContext

// shellOutDistill is the ON-DEMAND distill implementation, reached from
// resumeDistillEnv when `run --session <harp> --distill` needs an essence that
// does not exist yet. It runs `ctxloom session distill <harp>` as a child
// process so this file doesn't need to depend on the compactor or any LLM
// machinery itself. Stdout/stderr are piped through to the user.
//
// It used to serve a second caller, the automatic exit-time distill, which was
// removed: distillation is on-demand only now, so a session stays title-less
// until something explicitly asks for one.
//
// ctx bounds the child via exec.CommandContext: when ctx is cancelled the
// stdlib kills the process.
func shellOutDistill(ctx context.Context, harpName string) error {
	// selfexec.Path survives an in-place upgrade that unlinks the executing
	// inode; it is shared with the gRPC client, which cannot import cmd.
	exe := selfexec.Path()
	c := execCommand(ctx, exe, "session", "distill", harpName)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// validateResumeFlags rejects --distill without --session up front (friction
// like an unknown --llm/--permissions): --distill only modifies HOW --session
// resumes, so it is meaningless on its own.
func validateResumeFlags(session string, distill bool) error {
	if distill && session == "" {
		return fmt.Errorf("--distill requires --session <harp>")
	}
	return nil
}

// resumeFullContext is the full-resume mode's context source: it folds the
// resumed harp's full recorded transcript into the assembled context BEFORE
// it is split into fragments, via the SAME primitives the ACP resume path
// already uses (operations.RecordedSessionEntries + RenderResumedTranscript +
// JoinLeadBlocks — see internal/operations/engine_session.go's acp resume and
// coord/spawner.go's ResumeContext). entriesFn is the IoC seam (production:
// operations.RecordedSessionEntries bound to the run's ctx) so this is
// testable without a live session index or backend transcript reader.
//
// Fault-tolerant (CLAUDE.md): an unresolvable or unbound harp (unknown to the
// index, no bound transcript) warns and returns existing unchanged — a typo'd
// or stale --session must never block the launch.
func resumeFullContext(existing, harp string, entriesFn func(string) ([]agent.SessionEntry, error)) string {
	entries, err := entriesFn(harp)
	if err != nil {
		clidiag.Warn("ctxloom", "resume %s: no recorded history to prime (%v); starting with the assembled context only", harp, err)
		return existing
	}
	return operations.JoinLeadBlocks(existing, operations.RenderResumedTranscript(harp, entries))
}

// resumeDistillEnv is the distilled-resume mode's env source: the
// CTXLOOM_RESUMED_FROM/CTXLOOM_RESUMED_PARTS pair that hook_inject_context.go's
// resumedEssenceForInjection (SessionStart hook) and mcp_server.go's
// sessionInstructions already know how to consume — the exact mechanism the
// picker-driven --session resume this replaced used. PARTS is "session" (not
// "tasks" — task restoration was removed along with the picker and is not
// coming back here) so resumePartsIncludeSession's essence gate opens.
//
// essenceFn/staleFn/distillFn are injected (production: operations.
// ReadHarpEssence/resumeEssenceStale/shellOutDistill — the `session distill`
// compactor path, session_cmd.go's runSessionDistill/operations.CompactEntry/
// memory.NewCompactor) so distill-on-demand is unit-testable without
// shelling out. A distill failure warns rather than blocking launch; the
// SessionStart hook's own readHarpEssence call then simply finds nothing and
// omits the essence block.
//
// Path C: this used to distill only when the
// essence was MISSING, never when it was merely stale — so `run --session
// <harp> --distill` against a harp that had been /clear'd since its last
// distill silently resumed from a frozen prefix as long as SOME essence
// existed. staleFn (nil-safe: a nil func means "never stale", matching the
// pre-unification behavior for callers that don't wire one) closes that.
func resumeDistillEnv(harp string, essenceFn func(string) ([]byte, error), staleFn func(string) bool, distillFn func(context.Context, string) error) map[string]string {
	_, err := essenceFn(harp)
	missing := err != nil
	stale := !missing && staleFn != nil && staleFn(harp)
	if missing || stale {
		// Unbounded context.Background(): this runs before the session's
		// terminal is handed to the user, so there is no shell to unblock yet.
		if dErr := distillFn(context.Background(), harp); dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s for resume essence: %v", harp, dErr)
		}
	}
	return map[string]string{
		"CTXLOOM_RESUMED_FROM":  harp,
		"CTXLOOM_RESUMED_PARTS": "session",
	}
}

// resumeEssenceStale is resumeDistillEnv's production staleFn: whether harp's
// essence is out of date relative to its source transcript, via the same
// predicate `session list --distill`'s sweep gates on (Entry.SourceStale()).
// An unresolvable harp is not reported stale — resumeDistillEnv's own
// essence-missing check already covers "nothing to compare against".
func resumeEssenceStale(harp string) bool {
	entry, err := operations.GetSession(harp)
	if err != nil || entry == nil {
		return false
	}
	stale, known := entry.SourceStale()
	return known && stale
}

// seedTaskIntoSession marks the task with harpID In Progress (or the given
// status) in the project's task log, attributing the change to the new session
// (activeHarp). Tasks are project-scoped now (ADR 0025), so seeding is a status
// change rather than a move between per-session stores.
//
// A failure is a FATAL ClassTask finding, not a bare warning.
// Seeding only runs when the user passed --seed-task, so this is an explicit
// ask: if the task log is corrupt or the harp does not resolve, the session
// would otherwise launch looking successful while the task silently stayed
// untouched — the user believing it is In Progress and attributed here when it
// is not. Now that the task-log fold fails loud, swallowing that at a startup
// choke point is exactly what CLAUDE.md says must route through strictness.
// The never-block-launch behaviour survives as the DEGRADED mode (--degraded),
// where the finding is recorded and the launch proceeds.
func seedTaskIntoSession(workDir, activeHarp, harpID, status string) {
	if status == "" {
		status = tasks.StatusInProgress
	}
	res, err := taskops.SetTaskStatus(taskops.TaskContext{
		WorkDir:     workDir,
		ProjectID:   os.Getenv("CTXLOOM_PROJECT_ID"),
		SessionHarp: activeHarp,
	}, harpID, status, "")
	if err != nil {
		strictness.Fail(strictness.ClassTask,
			"check the task harp id (taskloom list), or drop --seed-task to launch without seeding",
			"seed task %s: %v", harpID, err)
		return
	}
	if res.Warning != "" {
		clidiag.Warn("ctxloom", "%s", res.Warning)
	}
	fmt.Fprintf(os.Stderr, "ctxloom: seeded task %s into %s (%s)\n", res.Task.HarpID, activeHarp, res.Task.Status)
}

var runCmd = &cobra.Command{
	Use:   "run [flags] [prompt...]",
	Short: "Assemble context and run AI",
	Long: `Assemble context from fragments and execute the configured LLM.

Fragments are loaded from installed bundles: local bundles in
.ctxloom/content/bundles/ plus remote bundles pinned in the lockfile.

Use --profile/-p to load a predefined set of fragments and variables.
Use --tag/-t to include all fragments with a specific tag.
Additional -f flags will be appended to the profile's fragments.

With no -p/-f/-t and no default profile configured, an interactive picker
lists the installed profiles to choose one for this run (skipped when not on a
terminal).

A profile may declare its own preferred LLM (profile create --llm). It is used
unless overridden by --llm/-l, which always wins.

The LLM runs in isolation, ignoring default context files like Claude.md.

Verbosity levels (-v can be repeated):
  -v      Show LLM commands being executed
  -vv     Show command arguments
  -vvv    Show debug output

Use --session <harp> to deterministically resume a prior harp-named session:
its full recorded transcript is folded into this run's assembled context.
Add --distill to resume via the session's distilled essence instead
(distilling on demand first if one doesn't exist yet).

Examples:
  ctxloom run -f coding-standards "review this code"
  ctxloom run -p developer "explain the architecture"
  ctxloom run -p reviewer -f extra-rules "review this PR"
  ctxloom run -t security "check for vulnerabilities"
  ctxloom run -vv -p developer "debug mode"
  ctxloom run --session swift-amber-falcon
  ctxloom run --session swift-amber-falcon --distill`,
	RunE: runRun,
}

// runState carries one `ctxloom run` invocation's resolved state across the
// phases below, so each phase NAMES what it reads and writes instead of taking
// a positional argument list. The single body this replaces threaded ~50
// locals through 957 lines; hoisting them into parameters would have traded
// cyclomatic complexity for connascence of position, which is worse.
//
// The phases run in one fixed order and each one's outputs are the next one's
// inputs, so every field is written by exactly one phase — named in the
// comment beside it. Reading the field list top to bottom IS the pipeline.
//
// The run* package-level flag variables are deliberately NOT copied in here:
// they are the command's own binding surface (registered in init(), pinned by
// run_flags_test.go), and every phase reads them directly exactly as the
// single body did.
type runState struct {
	cmd  *cobra.Command
	args []string
	// ctx carries shutdown signals so SIGTERM/SIGHUP unwind through runRun's
	// defers — terminal restore, the session end-mark, client.Kill — instead
	// of killing the process mid-raw-mode. Set by withShutdownSignals.
	ctx context.Context

	// The two fail-loudly gate anchors. They TILE: postStartupMark is captured
	// the instant gate 1 passes, so a finding recorded anywhere between the
	// windows still aborts at gate 2 instead of falling into an ungated hole.
	startupMark     strictness.Mark // runRun, before any startup choke fires
	postStartupMark strictness.Mark // gateStartup, immediately after gate 1 passes

	cfg    *config.Config // loadConfig
	prompt string         // resolvePrompt
	llmEnv map[string]string

	// The launch source's resolution (resolveLaunchSource and its three arms):
	// which context this run assembles and which engine carries it.
	ctxResult   *operations.AssembleContextResult
	label       string
	backendName string
	labelModel  string
	// agentPermissions is the --agent binding's declared permission posture
	// (empty for a classic run); the resolver layers the engine label and the
	// built-in default on top.
	agentPermissions string
	// agentSurfaces is the bound agent's resolved delivery preference, carried
	// to the backend on the managed payload.
	agentSurfaces map[agent.SurfaceKind]agent.Approach
	// The session's runtime axis: the agent's resolved runtime, or the project
	// `runtime:` default for a classic run. Parsed once, in resolveLaunchSource
	// (the classic-run default) or resolveAgentBinding (an agent binding's own
	// runtime) — never a bare string past that point.
	agentRuntime agent.RuntimeAxis
	// boundAgent names the agent binding this run launched under (--agent or
	// the default agent) — surround-bar identity only.
	boundAgent string
	// agentConfigHome is the resolved agent binding's EFFECTIVE config-home
	// policy (operations.ResolvedAgent.ConfigHome — always
	// agents.ConfigHomeProject or agents.ConfigHomeHost once a binding
	// resolved), or "" when this run has NO agent binding at all: the
	// -p/-f/-t classic assembly, or a bare launch whose default agent failed
	// to resolve.
	//
	// It exists as its own field because boundAgent cannot answer the
	// question the in-tree engine config home (prepareWorkspace →
	// operations.InTreeAgentHomeEnv) needs: resolveDefaultAgent sets
	// boundAgent too, so "boundAgent != \"\"" is true of a plain `ctxloom run`
	// just as much as `run --agent x` — both bind a real agent, and the
	// decision reads that agent's OWN declared config_home (always host by
	// default), never how it was invoked. Only "was any binding resolved at
	// all" is invocation-shaped, and that is exactly what an empty
	// agentConfigHome (vs. a resolved "project"/"host") already answers.
	agentConfigHome string

	// prepareRequestInputs: everything the RunStart payload is built from that
	// does not depend on the session having been opened.
	sessionWorkspace string
	mode             pb.ExecutionMode
	protoFragments   []*pb.Fragment
	promptFragment   *pb.Fragment
	workDir          string

	// openSession / hostCoordinator: this run's session identity and the
	// coordinator it hosts for delegated agents.
	activeHarp     string
	runEnv         map[string]string
	runnerSpawnEnv map[string]string
	// sessionCoord is held on the state (D2) so the terminal UI can reach
	// ConsumerService/Inject IN-PROCESS — this run IS the coordinator's own
	// hosting process, so its own terminal viewer never needs a network hop.
	sessionCoord *coord.Coordinator

	// buildRunRequest: the resolved permission posture and the wire payload.
	labelPerm string
	// projectPerm is THIS project directory's declared default posture
	// (config.yaml's top-level `permissions:`, empty when undeclared) — the
	// rung between the engine label and the engine's own built-in default.
	projectPerm      string
	requestedPerm    agent.PermissionMode
	hasRequestedPerm bool
	permMode         agent.PermissionMode
	managed          *agent.ManagedConfig
	req              *pb.RunStart

	// The session's isolation axes (workspace × runtime) and what Prepare made
	// of them.
	runAxes isolation.Axes
	policy  isolation.Policy
	ws      isolation.Workspace

	// startTransport: exactly ONE of these is non-nil per run.
	//
	// client is the go-plugin client (host/worktree interactive, oneshot).
	// interactiveLauncher + runnerHandle are Phase 2a-A: a
	// container-policy INTERACTIVE top-level run never constructs a go-plugin
	// client — it launches the StartRunner keepalive container and drives the
	// turn over a docker-exec vpio.Launcher (no in-container listener).
	// ownedRun is Phase 2a-B: a container-policy --one-shot ONESHOT
	// run drives over Transport 2 / EngineHost (an owner-owned run watched via
	// the in-process coordinator) instead of a go-plugin client.
	client              pb.Client
	interactiveLauncher vpio.Launcher
	runnerHandle        *isolation.RunnerHandle
	ownedRun            *ownedRunSession
}

func runRun(cmd *cobra.Command, args []string) error {
	st := &runState{
		cmd:  cmd,
		args: args,
		// Fail-loudly gate: checkpoint before any startup choke fires, so
		// every fatal finding collected across config load, sync, and
		// assembly is caught at one place (gateStartup below) and the launch
		// aborts with the full list. Degraded mode records nothing, so the
		// gate is a no-op.
		startupMark: strictness.Checkpoint(),
	}

	if err := st.validateFlags(); err != nil {
		return err
	}
	if err := st.loadConfig(); err != nil {
		return err
	}
	if err := st.resolvePrompt(); err != nil {
		return err
	}

	stopSignals := st.withShutdownSignals()
	defer stopSignals()

	st.runStartupTasks()

	if err := st.resolveLaunchSource(); err != nil {
		return err
	}
	if err := st.gateStartup(); err != nil {
		return err
	}

	st.prepareRequestInputs()

	// Dry run mode - show the assembled context and prompt, then stop before
	// anything stateful or interactive happens: no session-index writes
	// (AssignSession / EndSession), no coordinator, no task seeding, no
	// isolation, no plugin launch.
	if runDryRun {
		return st.emitDryRun()
	}

	// From here down every phase registers its unwind on runRun's OWN frame,
	// via a cleanup the phase returns or a method that guards itself, rather
	// than deferring inside the phase — a defer inside an extracted method
	// fires when that method returns, which for teardown is far too early.
	// The registration ORDER below is the unwind order (LIFO) and is
	// load-bearing; each defer's own doc says why it sits where it does.
	restoreTitle := st.openSession()
	defer restoreTitle()

	st.exportProjectIdentity()

	// Mark the harp ended on whatever exit path we take — clean return,
	// ctrl+c, or panic. The end timestamp lets the time-window fallback in
	// `ctxloom session distill` find this session's transcript even when the
	// bind middleware never fired (session ended before any MCP method was
	// processed).
	defer st.markSessionEnded()

	closeCoordinator := st.hostCoordinator()
	defer closeCoordinator()

	st.seedTask()
	if err := st.buildRunRequest(); err != nil {
		return err
	}

	// Teardown: kill the go-plugin client (host/worktree/oneshot
	// arms) OR the docker-exec keepalive container (Phase 2a-A interactive arm
	// — RunnerHandle.Kill is Phase 1's rm -f + removeReportsGone). Exactly one
	// is non-nil per run; the container arm never constructs a client.
	//
	// Registered HERE, before the workspace is prepared and before
	// startTransport can return early, rather than after them: a
	// defer only protects returns that happen AFTER it is reached, so a defer
	// placed after startTransport never fires for an early return out of it —
	// exactly the case where startContainerOwnedRun starts a real container,
	// then fails a LATER step and returns handle non-nil alongside the error.
	// Registering the cleanup up front, and having every arm in startTransport
	// assign into runnerHandle/ownedRun/client BEFORE checking its own error,
	// means every early return in between is covered instead of only the
	// successful-setup path.
	defer st.teardownTransport()

	st.prepareWorkspace()
	defer st.cleanupWorkspace()

	// Fail-loudly re-gate: isolation resolves in prepareWorkspace, AFTER the
	// startup gate, so a requested-container-degraded-to-host finding
	// (ClassIsolation, raised inside Prepare) would slip past that
	// already-passed gate. Gate 2 re-checks from postStartupMark — captured
	// the instant gate 1 passed, so the two windows TILE (a finding recorded
	// anywhere between the gates is caught, not just one raised inside
	// Prepare) — and an EXPLICITLY-requested container that can't be satisfied
	// aborts (exit 3) before an UNSANDBOXED engine is spawned — unless
	// --degraded, which records nothing and proceeds on the host per the
	// degrade chain.
	if ferr := failOnFindings(os.Stderr, st.postStartupMark); ferr != nil {
		return ferr
	}

	st.stampWorkspaceOnRequest()

	if err := st.startTransport(); err != nil {
		return err
	}
	return st.drive()
}

// validateFlags is the friction-up-front window: a typed value that isn't a
// known posture is a hard error before any work, so a typo can't silently
// resolve to a more permissive default. Config-sourced postures stay
// fault-tolerant.
func (st *runState) validateFlags() error {
	if err := validatePermissionFlag(runPermissions); err != nil {
		return err
	}
	return validateResumeFlags(runResumeSession, runResumeDistill)
}

// loadConfig loads this run's configuration and settles every upgrade offer it
// raises. Both halves belong together: a config that loaded is not yet a
// config that can be trusted to launch from, and the warnings it downgraded
// are what arms the startup gate.
func (st *runState) loadConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	st.cfg = cfg
	// config.Load downgrades unreadable/malformed/schema-invalid files to
	// warnings (CLAUDE.md fault tolerance) — surface them so a corrupted
	// config.yaml never silently launches an empty-context session.
	config.RecordWarningsTo(os.Stderr, cfg.GetWarnings())
	// If loading upgraded an older config schema in memory, offer to persist
	// it (interactive + consented only; never a silent rewrite).
	confirmUpgrade(cfg.GetPendingUpgrade(), cfg.CommitUpgrade)
	// The HOME layer gets the same offer when a project config also exists.
	// Without this, a stale ~/.ctxloom/config.yaml was upgraded
	// in memory on every load forever and never converged. The prompt names
	// the path, so consenting to rewrite HOME is an informed choice rather
	// than a surprise side effect of a project-scoped run.
	confirmUpgrade(cfg.GetHomePendingUpgrade(), cfg.CommitHomeUpgrade)
	// Profiles can carry an older schema too (e.g. bare bundle refs); offer to
	// persist those rewrites the same way.
	confirmProfileUpgrades(cfg)
	return nil
}

// resolvePrompt builds the prompt from the saved command, the flag, or the
// remaining args, then finalizes it. An empty prompt is allowed — it starts
// interactive mode.
func (st *runState) resolvePrompt() error {
	prompt := runPrompt
	if prompt == "" && runSavedPrompt != "" {
		promptRes, err := operations.GetCommand(st.cmd.Context(), st.cfg, operations.GetCommandRequest{Name: runSavedPrompt})
		if err != nil {
			return fmt.Errorf("failed to load command: %w", err)
		}
		prompt = promptRes.Content
	}
	if prompt == "" && len(st.args) > 0 {
		prompt = strings.Join(st.args, " ")
	}
	// In one-shot mode with no prompt yet, read it from piped stdin. This makes
	// `run --one-shot` a universal reducer: `… | ctxloom run -p synth
	// --one-shot` synthesizes over any piped input (e.g. output collected from
	// other tools or an earlier run). Skipped on a TTY so an interactive read
	// never blocks.
	prompt, err := finalizeRunPrompt(prompt, runOneShot, stdinIsPiped(), os.Stdin)
	if err != nil {
		return err
	}
	st.prompt = prompt
	return nil
}

// withShutdownSignals installs the run's shutdown-signal context and returns
// the stop function for runRun to defer. (Interactive ^C is raw-mode input
// forwarded to the child, not a SIGINT to us.)
func (st *runState) withShutdownSignals() context.CancelFunc {
	ctx, stopSignals := signal.NotifyContext(st.cmd.Context(), shutdownSignals...)
	st.ctx = ctx
	return stopSignals
}

// runStartupTasks is the side-effecting startup window: dependency sync,
// companion reporting, and the orphaned-worktree sweep. Every one of them is
// skipped under --dry-run, which must be side-effect free and non-interactive
// (no network, no installs, no confirm prompt) so it previews assembly against
// the library as it exists on disk.
//
// Items awaiting review are surfaced per-item by the content trust gate during
// assembly (the "N item(s) awaiting review — run 'ctxloom review'" advisory),
// not by a bundle-level lockfile diff here.
func (st *runState) runStartupTasks() {
	// Auto-sync remote dependencies on startup if enabled (graceful failure).
	// Mirrors the behavior of `ctxloom mcp` so the run path doesn't hard-fail
	// on missing parent profiles or bundles that sync would have fetched.
	// In a TTY, confirm with the user before installing anything new.
	syncCfg := st.cfg.GetSyncConfig()
	if syncCfg.ShouldAutoSync() && !runDryRun && confirmSyncInstall(st.ctx, st.cfg) {
		syncCtx, syncCancel := context.WithTimeout(st.ctx, 60*time.Second)
		result, syncErr := operations.SyncOnStartup(syncCtx, st.cfg)
		syncCancel()
		if syncErr != nil {
			if !errors.Is(syncErr, context.Canceled) {
				strictness.Fail(strictness.ClassSync, "check the remote/network, or pass --degraded to launch anyway", "sync failed: %v", syncErr)
			}
		} else {
			operations.WriteAndRecordSyncSummary(os.Stderr, result)
		}
	}

	// Log which companion binaries (taskloom, ltk) this session is wired
	// with, version-probed via `<bin> version --format json`.
	if !runDryRun {
		operations.ReportCompanions(os.Stderr)
	}

	// Startup reaper: sweep any per-agent worktree checkout left behind by a
	// crashed/killed prior run — teardown()'s
	// WIP-safe removal only ever fires on a graceful Cleanup(), so nothing
	// else ever reaps these. Best-effort, silent unless it found something.
	if !runDryRun {
		operations.SweepOrphanedWorktrees(st.ctx, os.Stderr)
	}

	// Startup reaper, second half: sweep any per-session ENGINE-HOME instance
	// left behind by a crashed/killed prior run in this project. Each one holds
	// a credential copied out of the user's real host home, so this is a
	// security sweep, not hygiene — see operations.SweepOrphanedSessionHomes.
	if !runDryRun {
		operations.SweepOrphanedSessionHomes(os.Stderr)
	}
}

// resolveLaunchSource picks between the run's two launch sources and delegates.
//
// --agent runs a named LOCAL binding — its composed profiles become the context
// and its engine + runtime the transport; the interactive picker and the
// -p/-f/-t assembly do not apply (cobra marks the flags mutually exclusive).
// Everything else is the classic profile flow, except a BARE launch (no --agent
// and no explicit context selection), which binds the always-bound default
// agent.
func (st *runState) resolveLaunchSource() error {
	// The session's runtime axis before any agent binding gets a say: the
	// project `runtime:` default. Parsed HERE — the earliest point this
	// config-boundary string is read for a bare/classic launch — via the
	// single canonical ParseRuntimeAxis, so a typo'd project `runtime:`
	// fails loud instead of riding into st.agentRuntime as an unvalidated
	// string an agent binding's own resolveAgentBinding parse would later
	// have to re-interpret (or silently not, for a launch that never binds
	// a named agent at all).
	runtime, err := agent.ParseRuntimeAxis(st.cfg.GetRuntime())
	if err != nil {
		return fmt.Errorf("project `runtime:` default: %w", err)
	}
	st.agentRuntime = runtime

	switch {
	case runAgent != "":
		return st.resolveNamedAgent()
	case runProfile == "" && len(runFragments) == 0 && len(runTags) == 0:
		return st.resolveDefaultAgent()
	default:
		return st.resolveClassicAssembly()
	}
}

// applyResolvedAgent folds an agent binding's resolution into the run's state.
// ResolveAgent already applied the --llm-beats-declared-engine precedence and
// the project fallbacks.
func (st *runState) applyResolvedAgent(rs *operations.ResolvedAgent, name string) {
	st.ctxResult = &operations.AssembleContextResult{
		Profiles:        rs.Profiles,
		FragmentsLoaded: rs.Fragments,
		Context:         rs.Context,
	}
	st.label, st.backendName, st.labelModel = rs.Label, rs.Backend, rs.Model
	st.agentRuntime = rs.Runtime
	st.agentPermissions = rs.Permissions
	st.agentSurfaces = rs.Surfaces
	st.boundAgent = name
	st.agentConfigHome = rs.ConfigHome
}

// resolveNamedAgent is the --agent arm. An unknown name is a HARD error: an
// explicit name is user intent, unlike acp's editor-serving degrade.
func (st *runState) resolveNamedAgent() error {
	rs, rerr := operations.ResolveAgent(st.ctx, st.cfg, runAgent, runLLM)
	if rerr != nil {
		return rerr
	}
	st.applyResolvedAgent(rs, runAgent)
	return nil
}

// resolveDefaultAgent is the BARE-launch arm: no --agent and no explicit
// context selection. Bind the always-bound DEFAULT AGENT (cfg.DefaultAgent)
// exactly like --agent — its composed profiles become the context and its
// engine + runtime + permissions the transport (profiles.defaults was
// retired). Unlike --agent (a HARD error on an unknown name), a
// missing/empty/unresolvable default_agent must NEVER block startup: warn and
// continue with empty context at the project-default label + runtime (CLAUDE.md
// fault tolerance; mirrors acp's operations.OpenEngineSession degrade).
func (st *runState) resolveDefaultAgent() error {
	rs, rerr := operations.ResolveAgent(st.ctx, st.cfg, st.cfg.GetDefaultAgent(), runLLM)
	if rerr == nil {
		st.applyResolvedAgent(rs, st.cfg.GetDefaultAgent())
		return nil
	}

	strictness.Fail(strictness.ClassRef, "set a default agent (ctxloom agent default <name>) or pass --degraded to launch anyway", "default agent %q unavailable; continuing with empty context: %v", st.cfg.GetDefaultAgent(), rerr)
	st.ctxResult = &operations.AssembleContextResult{}
	var lerr error
	// resolveRunLLM: --llm override, else the project primary label.
	st.label, lerr = resolveRunLLM(st.cfg, runLLM, "")
	if lerr != nil {
		return lerr
	}
	st.backendName, st.labelModel = operations.ResolveBackend(st.cfg, st.label)
	runtime, rterr := agent.ParseRuntimeAxis(st.cfg.GetRuntime())
	if rterr != nil {
		return fmt.Errorf("project `runtime:` default: %w", rterr)
	}
	st.agentRuntime = runtime
	return nil
}

// resolveClassicAssembly is the explicit-context-selection arm (-p / -f / -t).
func (st *runState) resolveClassicAssembly() error {
	var aerr error
	st.ctxResult, aerr = operations.AssembleContext(st.ctx, st.cfg, operations.AssembleContextRequest{
		Profile:   runProfile,
		Fragments: runFragments,
		Tags:      runTags,
	})
	if aerr != nil {
		return fmt.Errorf("failed to assemble context: %w", aerr)
	}

	// If user explicitly requested fragments (-f flags) but none loaded,
	// that's an error. Checked via MissingFragments — the always-on
	// builtin companion fragments mean FragmentsLoaded is never empty,
	// so a bare count can't see the miss.
	if len(runFragments) > 0 && len(st.ctxResult.MissingFragments) == len(runFragments) {
		return fmt.Errorf("no fragments loaded: requested fragments not found: %s", strings.Join(st.ctxResult.MissingFragments, ", "))
	}

	// The tag counterpart of the guard above — an explicit
	// -t selection that matches zero fragments must not silently
	// exit 0 having delivered no context.
	if len(runTags) > 0 && len(st.ctxResult.MissingTags) == len(runTags) {
		return fmt.Errorf("no fragments loaded: no fragment matches tag(s): %s", strings.Join(st.ctxResult.MissingTags, ", "))
	}

	// Determine which LLM config to use. Resolution is deferred until after
	// context assembly because the chosen profile may declare its own LLM.
	// Precedence: explicit --llm override (validated up front, friction),
	// else the profile's declared llm, else the primary role's label. The
	// label resolves to a backend type + model; the backend name (not the
	// label) drives session naming and transport.
	var lerr error
	st.label, lerr = resolveRunLLM(st.cfg, runLLM, st.ctxResult.ProfileLLM)
	if lerr != nil {
		return lerr
	}
	// label → backend type + model (shared with the oneshot/agent_run path).
	// The backend name (not the label) drives session naming and transport.
	st.backendName, st.labelModel = operations.ResolveBackend(st.cfg, st.label)
	return nil
}

// gateStartup is the strict startup gate: config load, sync, and assembly have
// run and any fatal-class fault (broken config, unresolvable default
// profile/parent, failed bundle load, partial hook apply) has been recorded.
// Abort now — before launching the backend — listing every finding with its
// fix. A dry run is gated too: previewing a broken setup should say so. In
// degraded mode this returns nil and the launch proceeds as before.
//
// It also anchors gate 2, immediately after gate 1 passes, so the two windows
// tile.
func (st *runState) gateStartup() error {
	// An invalid ui.prefix_key is a broken-config finding like any other:
	// recorded here so the gate below aborts before launch (a viewer on a
	// key the user didn't configure is a wrong-context session's cousin).
	validateTerminalUIConfig(st.cfg)

	if ferr := failOnFindings(os.Stderr, st.startupMark); ferr != nil {
		return ferr
	}
	st.postStartupMark = strictness.Checkpoint()
	return nil
}

// prepareRequestInputs resolves everything the RunStart payload is built from
// that does not depend on a session having been opened — the session workspace
// axis, the assembled context (including a full resume folded into it), the
// execution mode, the prompt fragment, and the work directory.
func (st *runState) prepareRequestInputs() {
	st.llmEnv = operations.LLMEnvFor(st.cfg, st.label)

	// The session's WORKSPACE axis: the invocation flag wins, else the
	// project `workspace:` default. A session trait — never read from the
	// agent binding.
	st.sessionWorkspace = runWorkspace
	if st.sessionWorkspace == "" {
		st.sessionWorkspace = st.cfg.GetWorkspace()
	}

	// --session (full resume — no --distill): prime the assembled context
	// with the resumed harp's full recorded transcript before it's split
	// into fragments below. This rides the SAME assembled-context path
	// every other context source (--agent/default agent/-p/-f/-t) already
	// goes through — the SessionStart hook's inject-context reads it back
	// from the content-addressed cache file this context ultimately
	// writes to (agent.WriteContextFile), same as a normal launch.
	// --distill takes the essence path instead (applyResumeEnv, once
	// activeHarp is assigned) — the two are mutually exclusive per
	// validateResumeFlags' sibling gate (neither flag depends on the other's
	// outcome, so no gate is needed here beyond the runResumeDistill check
	// itself).
	if runResumeSession != "" && !runResumeDistill {
		st.ctxResult.Context = resumeFullContext(st.ctxResult.Context, runResumeSession, func(h string) ([]agent.SessionEntry, error) {
			return operations.RecordedSessionEntries(st.ctx, h)
		})
	}

	// Convert context content to proto fragments
	if st.ctxResult.Context != "" {
		// Split context into individual fragments for display
		// In the actual implementation, we'll keep it as a single assembled fragment
		st.protoFragments = append(st.protoFragments, &pb.Fragment{
			Content: st.ctxResult.Context,
		})
	}

	// Determine execution mode
	st.mode = pb.ExecutionMode_INTERACTIVE
	if runOneShot {
		st.mode = pb.ExecutionMode_ONESHOT
	}

	// Build prompt fragment
	if st.prompt != "" {
		st.promptFragment = &pb.Fragment{
			Content: st.prompt,
		}
	}

	// Determine work directory: CTXLOOM_ROOT override, else git root if in
	// repo, else current directory.
	st.workDir = projectroot.WorkDir()

	// No override and no git root: workDir is just the launch directory. The
	// project's identity — and with it its tasks, plans, and sessions under
	// ~/.ctxloom — is keyed off this path, so they don't follow the directory
	// if it moves and a launch one level up or down won't resume them. Warn,
	// never block (CLAUDE.md fault tolerance).
	if projectroot.RootFromFallback() {
		clidiag.Warn("ctxloom", "not in a git repository — using %s as the project root; its tasks, plans, and sessions live under ~/.ctxloom keyed to this path, so re-launch from here to resume them.", st.workDir)
	}
}

// emitDryRun renders the resolved assembly a profile/flag set produces and
// stops. It is the last thing a --dry-run does.
func (st *runState) emitDryRun() error {
	payload := dryRunJSON{
		Agent:     runAgent,
		Workspace: st.sessionWorkspace,
		Runtime:   string(st.agentRuntime),
		LLM:       st.label,
		Backend:   st.backendName,
		Profiles:  orEmpty(st.ctxResult.Profiles),
		Fragments: orEmpty(st.ctxResult.FragmentsLoaded),
		Context:   st.ctxResult.Context,
		Tokens:    tokens.Estimate(st.ctxResult.Context),
		Prompt:    st.prompt,
	}
	return emit(st.cmd, payload, func() error {
		if runAgent != "" {
			fmt.Println("=== Agent ===")
			fmt.Printf("%s (workspace: %s, runtime: %s)\n", runAgent, orDefault(st.sessionWorkspace, "none"), orDefault(string(st.agentRuntime), "host"))
		}
		fmt.Println("=== LLM ===")
		fmt.Printf("%s (%s)\n", st.label, st.backendName)
		fmt.Println("\n=== Profiles ===")
		if len(st.ctxResult.Profiles) > 0 {
			for _, p := range st.ctxResult.Profiles {
				fmt.Printf("  %s\n", p)
			}
		} else {
			fmt.Println("(no profiles)")
		}
		fmt.Println("\n=== Fragments Loaded ===")
		if len(st.ctxResult.FragmentsLoaded) > 0 {
			for _, f := range st.ctxResult.FragmentsLoaded {
				fmt.Printf("  %s\n", f)
			}
		} else {
			fmt.Println("(no fragments)")
		}
		fmt.Printf("\n=== Assembled Context (~%d tokens) ===\n", payload.Tokens)
		if st.ctxResult.Context != "" {
			fmt.Println(st.ctxResult.Context)
		} else {
			fmt.Println("(no context)")
		}
		fmt.Println("\n=== Prompt ===")
		if st.prompt != "" {
			fmt.Println(st.prompt)
		} else {
			fmt.Println("(interactive mode)")
		}
		// Show context file that would be written
		fmt.Println("\n=== Context File ===")
		fmt.Printf("Would write to: %s/[hash].md\n", filepath.Join(st.workDir, agent.SCMContextSubdir))
		return nil
	})
}

// openSession mints this run's harp and everything keyed to it: the session
// env, the resume env pair, and the start-session banner. It returns the
// terminal-title restore for runRun to defer — the OSC2 push/pop pair has to
// unwind on runRun's frame, not this one.
//
// Session resolution: no interactive resume picker, no flag-based resume —
// every `ctxloom run` opens a FRESH harp. Resuming prior context is the
// in-engine "resume" skill's job (recover_session/load_session/
// get_previous_session), invoked from inside the session that just started,
// not a startup-time choice.
func (st *runState) openSession() func() {
	st.runEnv = map[string]string{}
	for k, v := range st.llmEnv {
		st.runEnv[k] = v
	}

	// If loading the index normalized an older on-disk format, offer to
	// persist it before the upcoming AssignSession write (which would
	// otherwise rewrite it as a side effect of creating the new session).
	if pending, commit, upErr := operations.SessionIndexUpgrade(); upErr != nil {
		clidiag.Warn("ctxloom", "session index open failed: %v", upErr)
	} else {
		confirmUpgrade(pending, commit)
	}

	entry, err := operations.AssignSession(st.workDir, st.backendName)
	if err != nil {
		clidiag.Warn("ctxloom", "session naming failed: %v", err)
		return func() {}
	}

	st.activeHarp = entry.HarpName
	st.runEnv["CTXLOOM_SESSION_HARP"] = entry.HarpName
	st.applyResumeEnv()

	// Start-session display: a read-only summary of this session,
	// printed BEFORE the engine spawns. previous, below, is resolved via
	// the SAME primitive the get_previous_session MCP tool reads — never
	// re-derived — and is purely informational: bringing it back is the
	// resume skill's job, not something this banner offers to do.
	previous, prevErr := operations.ResolvePreviousSession(st.workDir, st.activeHarp)
	if prevErr != nil {
		clidiag.Warn("ctxloom", "previous-session lookup failed: %v", prevErr)
	}
	PrintStartSessionBanner(os.Stderr, StartSessionInfo{
		Harp:      entry.HarpName,
		Backend:   st.backendName,
		Label:     st.label,
		Profiles:  st.ctxResult.Profiles,
		Fragments: st.ctxResult.FragmentsLoaded,
		Tokens:    tokens.Estimate(st.ctxResult.Context),
		Previous:  previous,
	})

	// Set the terminal window title to the harp name via the
	// OSC2 escape sequence. Most terminals (xterm, iTerm2,
	// alacritty, WezTerm, kitty, Windows Terminal) render it;
	// the rest silently ignore the sequence. Skipped for
	// non-TTY (CI, piped) so we don't pollute pipelines — the
	// stderr check matters too, or `2>log` captures would
	// collect raw escape bytes. The XTWINOPS push (22;0t) /
	// pop (23;0t) pair restores the previous title on exit
	// where supported; elsewhere it is silently ignored.
	if isInteractiveTerminal() && stderrIsTerminal() {
		fmt.Fprintf(os.Stderr, "\033[22;0t\033]2;ctxloom · %s\007", entry.HarpName)
		return func() { fmt.Fprint(os.Stderr, "\033[23;0t") }
	}
	return func() {}
}

// applyResumeEnv stamps the CTXLOOM_RESUMED_FROM/PARTS pair for whichever
// --session mode this run is in.
//
// --session --distill: distilled resume via the harp's essence (distilling on
// demand first if missing) — see resumeDistillEnv's doc for the full
// mechanism. Full resume (--session without --distill) already folded its
// transcript into ctxResult.Context in prepareRequestInputs, so it doesn't
// ride this env pair for content — it still sets CTXLOOM_RESUMED_FROM/
// PARTS="transcript" so mcp_server.go's sessionInstructions surfaces the
// "resumed from" note, but with a PARTS value resumePartsIncludeSession
// rejects, so the SessionStart hook's essence injection stays a no-op for this
// mode (the content already rode the context path, not the essence path — no
// double-injection).
func (st *runState) applyResumeEnv() {
	switch {
	case runResumeSession != "" && runResumeDistill:
		for k, v := range resumeDistillEnv(runResumeSession, operations.ReadHarpEssence, resumeEssenceStale, shellOutDistill) {
			st.runEnv[k] = v
		}
		fmt.Fprintf(os.Stderr, "ctxloom: resuming distilled essence from %s\n", runResumeSession)
	case runResumeSession != "":
		st.runEnv["CTXLOOM_RESUMED_FROM"] = runResumeSession
		st.runEnv["CTXLOOM_RESUMED_PARTS"] = "transcript"
		fmt.Fprintf(os.Stderr, "ctxloom: resuming full transcript from %s\n", runResumeSession)
	}
}

// exportProjectIdentity resolves the project's stable identity (ADR 0025) and
// exports it into the session env. Fault-tolerant — any failure warns and
// leaves CTXLOOM_PROJECT_ID unset; the task store degrades rather than
// blocking.
//
// taskStoreWorkDir redirects a linked git worktree with no .ctxloom of
// its own to its primary checkout FIRST: the session/coordinator
// identity workDir itself names stays worktree-distinct (unchanged,
// see coord_host.go), but the task store an agent files findings into
// is deliberately shared with the primary checkout so a task filed
// from an ephemeral worktree reaches whoever is actually watching —
// "tasks aren't context" (see internal/projectroot.TaskStoreRoot).
func (st *runState) exportProjectIdentity() {
	pid, warning, err := taskops.ResolveProjectIdentity(taskStoreWorkDir(st.workDir))
	if err != nil {
		clidiag.Warn("ctxloom", "project identity unresolved: %v", err)
		return
	}
	st.runEnv["CTXLOOM_PROJECT_ID"] = pid
	if warning != "" {
		clidiag.Warn("ctxloom", "%s", warning)
	}
}

// markSessionEnded stamps the harp's end timestamp. It guards on an unbound
// harp itself so runRun can defer it unconditionally — the single body it
// replaces guarded by not registering the defer at all, which is the same
// thing done at a distance.
func (st *runState) markSessionEnded() {
	if st.activeHarp == "" {
		return
	}
	if err := operations.EndSession(st.activeHarp, time.Now()); err != nil {
		clidiag.Warn("ctxloom", "session end-mark failed: %v", err)
	}
}

// hostCoordinator stands the runtime coordinator up and returns its teardown.
//
// COORDINATOR HOSTING (agentcoord B1.6): `ctxloom run` is a session-owning
// process, so it stands the runtime coordinator up — durable delegation
// stores, the gRPC RunnerChannel/RunChannel — and stamps the reach-back trio
// onto the RUNNER's spawn env (the per-spawn seam below), NOT the harness env:
// the parent routes through its own runner like every agent. The runner
// terminates MCP on a local socket; the harness's stdio `ctxloom mcp` forwards
// there, and every coordination tool becomes a typed plane-2 frame back HERE.
// A standup failure is a fatal finding (fail-loud): --degraded downgrades it
// and the harness's shim falls back to its own local orchestrator.
//
// Print (oneshot) runs host the coordinator too: the parent routes through its
// own runner in every topology, so a headless coordinator brief (the echo
// smoke) exercises the same runner-terminated path as an interactive session —
// the bare-mcp shim fallback is for externally-launched harnesses only.
func (st *runState) hostCoordinator() func() {
	if st.activeHarp == "" {
		return func() {}
	}

	sc, coordEnv, cerr := mcp.HostCoordinatorForSession(st.cfg, st.workDir, st.activeHarp, st.agentRuntime)
	if cerr != nil {
		strictness.Fail(strictness.ClassApply,
			"check the coordinator listeners/state dir, or pass --degraded (env CTXLOOM_DEGRADED=1) to launch without agent delegation reach-back",
			"agent coordinator startup failed: %v", cerr)
		return func() {}
	}

	st.sessionCoord = sc
	st.runnerSpawnEnv = coordEnv
	// The runner's local identity (session instructions, plan stamping) is the
	// session harp.
	st.runnerSpawnEnv["CTXLOOM_SESSION_HARP"] = st.activeHarp

	// RevokeSessionOwner existed with zero call sites, so a depth-0
	// session-owner credential (minted per `ctxloom run` process by
	// mcp.SessionOwnerEnv) was never revoked — doc.go's "revocation at run end
	// severs the credential's streams and parked polls" held for run
	// credentials but not for this one, and since runsFold.apply re-applies
	// every factSessionCred on replay/adoption, every owner token ever minted
	// for a project stayed valid forever in that project's coordinator state.
	// Revoke on the SAME teardown that closes the coordinator, and BEFORE it,
	// while the journal is still open to accept the write.
	ownerToken := coordEnv[coord.EnvCoordCred]
	return func() {
		sc.RevokeSessionOwner(ownerToken)
		sc.Close()
	}
}

// seedTask handles --seed-task: move one task from the resume source store
// into this freshly minted session's store, marked for active work. Used by
// `ctxloom tasks run` to spin a browsed task into its own session. The task
// already lives in the project log; seeding marks it In Progress under the new
// session.
func (st *runState) seedTask() {
	if runSeedTask != "" && st.activeHarp != "" {
		seedTaskIntoSession(st.workDir, st.activeHarp, runSeedTask, runSeedStatus)
	}
}

// buildRunRequest assembles the RunStart wire payload: the executable trust
// gate, the isolation axes, the resolved permission posture, and the managed
// config the backend plugin is handed instead of self-loading ctxloom
// config/bundles.
//
// AssembleManagedConfig takes BOTH the executable trust gate (so bundle
// MCP/hooks/command exports are gated at their own choke, TR5) AND
// ctxResult.Profiles — the SELECTED profile set (from -p, or the resolved
// defaults) that AssembleContext scoped context to. Passing the profiles here
// scopes the managed mcp/commands/hooks to the SAME profiles, so `run -p X` no
// longer leaks the default profile's MCP or every pulled bundle's commands
// into X's session.
func (st *runState) buildRunRequest() error {
	// Gate the executable surfaces (bundle MCP servers + bundle hooks + prompt
	// command-file exports) the host ships in ManagedConfig: these bypass the
	// content loader, so each is gated at its own choke via this injected
	// gate. Built once (opens the trust store + registry); fail-closed (a
	// DENY omits the executable). Surfaced below.
	execGate := operations.NewExecutableTrustGate(st.cfg)

	// The session's isolation axes: the SESSION-level workspace (--workspace,
	// else the project `workspace:` default) x the runtime the launch source
	// resolved (the agent's binding, or the project default). The managed path
	// prepares a policy from these below; the permission posture resolves
	// separately from config/CLI (no longer gated on the isolation boundary).
	// The external-plugin-binary path is never isolated (none).
	// A --workspace (or project `workspace:`) spelling the axis does not
	// admit stops the run here. Asserted past the parser it would read as the
	// shared checkout, so a typo'd request for isolation would run in the
	// live project directory having said so out loud to nobody.
	sessionWorkspace, err := isolation.ParseWorkspaceAxis(st.sessionWorkspace)
	if err != nil {
		return err
	}
	st.runAxes = isolation.Axes{
		Workspace: sessionWorkspace,
		Runtime:   isolation.RuntimeAxis(st.agentRuntime),
	}

	// Launch-time permission posture. Precedence: --permissions flag > agent
	// binding > engine-label config > THIS PROJECT DIRECTORY's declared default
	// (config.yaml's top-level `permissions:`) > built-in default (claude-code →
	// bypass while container isolation isn't relied on; others prompt). A
	// headless ONESHOT upgrades a would-block posture to bypass or it would hang
	// with no human to answer the engine.
	labelEntry, _ := st.cfg.GetLLMEntry(st.label)
	st.labelPerm = labelEntry.Permissions
	st.projectPerm = st.cfg.GetPermissions()
	st.permMode = resolvePermissionMode(runPermissions, st.agentPermissions, st.labelPerm, st.projectPerm, st.backendName, st.mode, backends.EnforcesReadOnlyPlan(st.backendName))
	st.requestedPerm, st.hasRequestedPerm = requestedPermission(runPermissions, st.agentPermissions, st.labelPerm, st.projectPerm)
	st.warnPermissionCollapse()
	st.warnHostBypassStopgap()
	st.warnPlanOneshotCancels()

	st.managed = backends.AssembleManagedConfig(st.backendName, st.workDir, execGate.Authorizer(), st.ctxResult.Profiles)
	// The binding's delivery preference rides the managed payload to the
	// backend, which is the only place with the argv sink system-prompt needs.
	// Set AFTER assembly rather than inside it: AssembleManagedConfig resolves
	// PROFILE state and knows nothing about which agent is being launched.
	if st.managed != nil && len(st.agentSurfaces) > 0 {
		st.managed.Surfaces = st.agentSurfaces
	}
	st.req = &pb.RunStart{
		Fragments: st.protoFragments,
		Prompt:    st.promptFragment,
		Options: &pb.RunOptions{
			WorkDir:        st.workDir,
			PermissionMode: st.permMode.String(),
			Mode:           st.mode,
			Env:            st.runEnv,
			Verbosity:      agent.WireVerbosity(runVerbosity),
			// The model comes from the resolved label's config; empty lets the
			// backend pick its own default. e.g., "opus", "sonnet", "haiku".
			Model: st.labelModel,
		},
		ManagedConfig: pb.ManagedConfigToProto(st.managed),
	}
	// Advisory: tell the user if a bundle executable was withheld (content-free).
	execGate.WarnWithheld()
	return nil
}

// warnPermissionCollapse surfaces a posture that resolved to something other
// than what was asked for, so the effective permissions are never silently
// different from intent. Both arms require a REQUESTED posture, which is what
// keeps them disjoint from warnHostBypassStopgap's no-request arm.
func (st *runState) warnPermissionCollapse() {
	switch {
	case st.hasRequestedPerm && st.requestedPerm == agent.PermissionPlan && st.permMode != agent.PermissionPlan:
		// The backend has no read-only tier, so plan collapsed (to prompt, or to
		// bypass headless) — the read-only intent is not enforced.
		clidiag.Warn("ctxloom", "%s has no read-only plan mode; this run uses %q instead", st.backendName, st.permMode)
	case st.hasRequestedPerm && st.requestedPerm != agent.PermissionBypass && st.permMode == agent.PermissionBypass:
		// An explicitly-requested narrower posture was widened to bypass because a
		// headless ONESHOT has no human to answer the engine's prompt.
		clidiag.Warn("ctxloom", "--one-shot can't honor %q without a human in the loop; this run uses bypass", st.requestedPerm)
	}
}

// warnPlanOneshotCancels surfaces that a --one-shot run has no human to answer
// a gated call. After resolvePermissionMode's ONESHOT floor, plan is the ONLY
// posture that can still reach here without having been widened to bypass
// (SafeHeadless: bypass never asks; default/acceptEdits already floored up to
// bypass) — so plan surviving into a ONESHOT run means read-only intent is
// real, but so is the empty chair: any gated (mutating) call has nobody to ask.
// The engine-side answer to an unanswerable gate is to CANCEL it outright, not
// deny it, so mutating steps silently do not run
// unless this is called out loudly up front.
func (st *runState) warnPlanOneshotCancels() {
	if st.mode == pb.ExecutionMode_ONESHOT && st.permMode == agent.PermissionPlan {
		clidiag.Warn("ctxloom", "--one-shot with plan permissions has no human to approve a gated call; the engine cancels every gated call, so mutating steps will not run")
	}
}

// warnHostBypassStopgap surfaces the claude-code host-bypass stopgap: blanket
// auto-approval on the bare host. It's the default path, so surface it only
// under -v to avoid warning fatigue while still making the posture
// discoverable.
func (st *runState) warnHostBypassStopgap() {
	if !st.hasRequestedPerm && st.permMode == agent.PermissionBypass && st.backendName == config.BackendClaudeCode && runVerbosity > 0 {
		clidiag.Warn("ctxloom", "permissions bypassed on the host (claude-code stopgap)")
	}
}

// teardownTransport kills whichever transport this run stood up. See runRun's
// own comment at the deferral site for why it is registered before the
// workspace is prepared rather than after the transport is chosen.
func (st *runState) teardownTransport() {
	if st.client != nil {
		st.client.Kill()
	}
	if st.runnerHandle != nil {
		st.runnerHandle.Kill()
	}
	if st.ownedRun != nil {
		st.ownedRun.cancel()
	}
}

// prepareWorkspace prepares the workspace along the per-axis degrade chain.
// Fault tolerance: a container requested but unlaunchable (no runtime, or the
// agent image absent) drops ONLY the runtime axis — a requested worktree
// survives — and a worktree failure degrades to the live project dir, so
// `runtime: container` is a safe default. Findings it raises are caught by
// gate 2, which runRun runs immediately after.
//
// The session identity (harp + project id) rides the same runEnv the engine
// gets, so the isolation state mounts and the in-container writers key off one
// source.
func (st *runState) prepareWorkspace() {
	// The permission posture is resolved once from config/CLI/agent and is
	// authoritative regardless of how the isolation boundary degrades: a
	// container that failed to launch does NOT drop a configured bypass —
	// that is the point of the host stopgap.
	st.policy, st.ws = isolation.Prepare(st.ctx, st.runAxes, st.backendName, operations.IsolationImageConfig(st.cfg, st.backendName), st.workDir, st.activeHarp, isolation.SessionStateFromEnv(st.runEnv))

	// Per-agent config-home envs (worktree) isolate each engine's GLOBAL
	// config layer (CLAUDE_CONFIG_DIR / CODEX_HOME / KIRO_HOME / ...) from
	// this run; nil for none/container. Mirrors the fan-out member path
	// (operations/oneshot.go's workspaceEnv/env assembly): merged UNDER the
	// already-assembled req.Options.Env (session identity + user --env), so
	// an explicit user/session var still wins over a resolved isolation var
	// — this must never clobber a caller-set env, only fill gaps.
	st.req.Options.Env = mergeWorkspaceEnv(st.req.Options.Env, isolation.WorkspaceEnv(st.ws))

	// IN-TREE AGENT HOME. On the none axis there is no isolation-provided
	// config home, and claude/kiro would otherwise run against the human's own
	// ~/.claude / ~/.kiro. A run bound to an agent whose EFFECTIVE config_home
	// is "project" gets a project-scoped controlled home instead; every other
	// run — no binding at all, or a binding that is undeclared or declares
	// "host" — keeps the real one. See operations.InTreeAgentHomeEnv for the
	// whole rule, and st.agentConfigHome for why this reads the resolved
	// agent's OWN declared policy rather than how it was invoked.
	//
	// Merged with the SAME precedence as the isolation env above (and layered
	// under it — the call reads the already-merged map and declines any var
	// already present), so an explicit user/session var still wins.
	st.req.Options.Env = mergeWorkspaceEnv(st.req.Options.Env, operations.InTreeAgentHomeEnv(operations.InTreeAgentHome{
		Backend: st.backendName,
		WorkDir: st.workDir,
		// The instance is PER SESSION, and st.activeHarp is this session:
		// runRun calls openSession() BEFORE prepareWorkspace(), and openSession
		// is what assigns it (and stamps CTXLOOM_SESSION_HARP into runEnv). A
		// run that reached here with no session name gets no instance and keeps
		// the engine's own host home.
		Harp:       st.activeHarp,
		ConfigHome: st.agentConfigHome,
		Policy:     st.policy,
		Env:        st.req.Options.Env,
	}))
}

// cleanupWorkspace tears the prepared workspace down. none's cleanup is a
// noop. The error is deliberately dropped: a cleanup failure surfaces from
// INSIDE Cleanup (a streamed warning naming the residue path + fix — see
// warnCleanupResidue), and this runs post-gate where no choke owner could act
// on an error anyway.
func (st *runState) cleanupWorkspace() {
	_ = st.ws.Cleanup()
}

// stampWorkspaceOnRequest folds the prepared workspace back into the RunStart
// payload, once gate 2 has accepted the isolation that resolved.
func (st *runState) stampWorkspaceOnRequest() {
	// A container-requested run whose boundary silently degraded to the bare
	// host still carries a configured bypass. For the claude-code host stopgap
	// that is intended; for any other backend the boundary that justified
	// bypass is gone, so surface it rather than run full-auto with no signal.
	// A SATISFIED container request (the container OR container-worktree
	// policy prepared) never warns. In strict mode a lost boundary recorded
	// a ClassIsolation finding and gate 2 already aborted, so this warning
	// fires only in degraded mode (or if a degrade recorded nothing).
	if warnBypassOnLostContainer(st.runAxes, st.policy.Name(), st.permMode, st.backendName) {
		clidiag.Warn("ctxloom", "container isolation unavailable; running %s with bypass on the host", st.backendName)
	}

	// The engine's cwd lands in the prepared workspace (identical-path for
	// container/none; a worktree in Phase 2).
	st.req.Options.WorkDir = st.ws.Dir()

	// Stamp the resolved isolation cell so the plugin knows which cell it
	// runs in (it can't infer it from WorkDir alone). Setup's setupViaCells
	// (launch_backend.go) consumes it to pick the delivery cell.
	st.req.Options.CellKind = pb.CellKindToProto(operations.CellKindForPolicy(st.policy))

	// For a container policy ONLY, stamp the in-container
	// ctxloom binary path so the MCP-surface writer (running inside the
	// container, per agentcoord B1.6's runner-terminated MCP) emits a
	// `command` the container can actually exec, instead of the host
	// self-exec path (which does not exist inside the container — the
	// engine's `ctxloom mcp` stdio shim then never launches and the child
	// has zero MCP tools). "" for none/worktree: the host self-exec-
	// absolute invariant (agent.CtxloomCommand's doc) is untouched.
	if override := operations.MCPCommandOverrideForPolicy(st.policy); override != "" {
		if st.req.Options.Env == nil {
			st.req.Options.Env = make(map[string]string, 1)
		}
		st.req.Options.Env[agent.MCPCommandOverrideEnv] = override
	}
}

// startTransport creates the run's transport. The isolation axes (runAxes,
// default none/host) decide WHERE the top-level run's workspace lives and HOW
// its plugin is spawned.
//
// Phase 2a-A swap: an INTERACTIVE container-policy top-level run goes
// docker-exec instead of go-plugin. Launch the container via the SAME
// StartRunner primitive Phase 1 uses (an `llm host` keepalive), hand the
// RunStart off by 0600 file in the bind-mounted persist dir, and build the
// docker-exec Launcher; NO go-plugin client is constructed. The oneshot
// container arm (Part B) and every host/worktree arm stay on SpawnClient +
// goplugin. The observation/injection wrap sits ABOVE the seam (untouched) —
// the Launcher just receives the already-wrapped streams.
//
// Every arm assigns its handle into the state BEFORE checking its own error
// so runRun's already-registered teardown sees it — see the per-arm notes.
func (st *runState) startTransport() error {
	switch runTransport(st.policy.Name(), st.mode) {
	case armDockerExecInteractive:
		handle, launcher, lerr := startContainerInteractive(st.ctx, st.policy, st.ws, st.req, st.backendName, st.label, runVerbosity, st.activeHarp, st.runnerSpawnEnv)
		if lerr != nil {
			return fmt.Errorf("failed to start container interactive turn: %w", lerr)
		}
		st.runnerHandle = handle
		st.interactiveLauncher = launcher

	case armOwnedRunContainer:
		// Phase 2a-B: a oneshot container → owner-owned run on
		// Transport 2. Launched through the SAME StartRunner primitive
		// (an `llm host` runner WITH the run-id trio → EngineHost); the
		// host watches it via WatchRuns. No go-plugin client; no
		// in-container listener.
		handle, sess, oerr := startContainerOwnedRun(st.ctx, st.sessionCoord, ownedRunLaunch{
			Policy:      st.policy,
			Workspace:   st.ws,
			Req:         st.req,
			BackendName: st.backendName,
			Label:       st.label,
			Verbosity:   runVerbosity,
			Harp:        st.activeHarp,
			ContextText: st.ctxResult.Context,
			Prompt:      st.prompt,
			MCPServers:  st.managed.ChatMCPServers(st.req.Options.Env[agent.MCPCommandOverrideEnv]),
			Permission:  st.permMode,
			Mode:        st.mode,
			RunnerEnv:   st.runnerSpawnEnv,
		})
		// Assign BEFORE checking oerr: startContainerOwnedRun
		// can return a non-nil handle ALONGSIDE a non-nil error (the
		// container started; a later step in StartOwnedRun failed) — if
		// the assignment waited for the error check, that early return
		// would discard the handle before runRun's teardown defer ever
		// sees it, leaking the running container.
		st.runnerHandle = handle
		st.ownedRun = sess
		if oerr != nil {
			return fmt.Errorf("failed to start container oneshot run: %w", oerr)
		}

	case armGoPlugin:
		// Spawn through the policy, carrying the resolved label so serve
		// configures exactly this entry (not the first map-ordered entry of the
		// same type). Assigned into the state before the error check for the
		// same reason the owned-run arm is.
		var err error
		st.client, err = st.policy.SpawnClient(st.backendName, st.label, runVerbosity, st.ws, st.runnerSpawnEnv)
		if err != nil {
			return fmt.Errorf("failed to start plugin: %w", err)
		}
	}
	return nil
}

// drive runs the session over whichever transport startTransport stood up, and
// is the last thing runRun does.
func (st *runState) drive() error {
	// Phase 2a-B: a container-policy --one-shot ONESHOT drives over Transport
	// 2 — collect the run's FINAL answer, record the oneshot transcript, exit
	// with the run's status. No go-plugin client is constructed for this arm,
	// so it returns before the vpio/go-plugin Run path below.
	if st.ownedRun != nil {
		return runOneshotViaCoord(st.ctx, st.ownedRun, st.activeHarp, st.backendName, st.prompt, os.Stdout)
	}

	return st.driveTerminalSession()
}

// sessionIO is the terminal seam set the vpio launcher is handed. It is one
// value rather than five returns because the five are decided together and
// consumed together, and restore composes onto the others.
type sessionIO struct {
	stdin  io.Reader
	stdout io.Writer
	resize <-chan *pb.WindowSize
	// capture is the S6 oneshot tee's buffer; nil for INTERACTIVE.
	capture *bytes.Buffer
	// restore unwinds the terminal (raw mode, and the observation layer's
	// scroll region + held output when one engaged). Idempotent, and a no-op
	// for a run that never took the terminal.
	restore func()
}

// driveTerminalSession is the go-plugin / docker-exec launch path: the run
// owns (or tees) the terminal, starts the engine over the vpio seam, and waits.
func (st *runState) driveTerminalSession() error {
	sio := st.prepareSessionIO()
	// Deferred (the value may be the composed one) so a panic inside the
	// session can't strand the shell in raw mode. restore is idempotent; the
	// inline call in launchSession still restores before any normal-path
	// output. This defer belongs on THIS frame, not runRun's: it must unwind
	// before anything else, and this call is the last thing runRun does.
	defer sio.restore()
	return st.launchSession(sio)
}

// prepareSessionIO decides the run's terminal seams. For an interactive run
// the frontend owns the terminal: raw mode + stdin + resize are pumped over
// the VIRTUALIZED-PROCESS-IO (vpio) seam — internal/vpio — to the controller's
// pty. Oneshot runs need none of that. Everything here stays above the seam:
// it references only pb.WindowSize (the wire's resize payload shape, not a
// transport call) and vpio types, never a transport client method directly.
func (st *runState) prepareSessionIO() sessionIO {
	sio := sessionIO{stdout: os.Stdout, restore: func() {}}

	// S6 oneshot capture: a ONESHOT `--one-shot` run drives Backend.Execute,
	// which returns prose on stdout with no ChatEvent stream — the
	// structured tee (GRPCClient.Chat, internal/lm/grpc/chat.go) never
	// fires for it, so this is the runner's own seam onto both halves of
	// a two-entry canonical transcript (transcript.RecordOneshot): the
	// prompt is already known (st.prompt), and this captures the returned
	// half by teeing the SAME bytes already bound for the terminal into a
	// buffer, alongside (never instead of) the user-visible stdout. Never
	// allocated for INTERACTIVE (the pty path, out of scope — a separate,
	// tracked gap) or when a container-policy ONESHOT drives over Transport 2
	// instead (st.ownedRun != nil returns earlier, in drive).
	if st.mode == pb.ExecutionMode_ONESHOT {
		sio.capture = &bytes.Buffer{}
		sio.stdout = io.MultiWriter(sio.stdout, sio.capture)
	}

	if st.mode == pb.ExecutionMode_INTERACTIVE {
		sio.stdin, sio.resize, sio.restore = interactiveTerminal(st.ctx)
		// Wrap the terminal seams with the observation layer (prefix-key
		// viewer + surround bar) — real tty only, never a pipe, and
		// --plain-terminal opts a session out entirely. Its Close composes
		// onto the raw-mode restore so every exit path (clean, error,
		// signal-cancelled ctx) unwinds scroll region, held output, and
		// raw mode together.
		if sio.stdin != nil && !runPlainTerminal {
			// The TUI is about to own this terminal, so clidiag warnings must
			// stop writing to it. Diverted to the session's diagnostics log,
			// announced before the handover.
			restoreDiag := redirectDiagnosticsForTUI(st.activeHarp, os.Stderr)
			if ui := setupTerminalUI(st.ctx, st.cfg, st.sessionCoord, terminalUIIdentity{
				WorkDir: st.workDir,
				Harp:    st.activeHarp,
				Agent:   st.boundAgent,
				Backend: st.backendName,
				Model:   st.labelModel,
			}, sio.stdin, sio.resize); ui != nil {
				sio.stdin, sio.stdout, sio.resize = ui.Stdin(), ui.Stdout(), ui.Resize()
				rawRestore := sio.restore
				sio.restore = func() { ui.Close(); restoreDiag(); rawRestore() }
			} else {
				// No TUI engaged after all — stderr is still the user's,
				// so put the warnings back on it.
				restoreDiag()
			}
		}
	}
	return sio
}

// launchSession runs the AI plugin over the vpio seam — the SWAP POINT. An
// interactive container run selected the docker-exec Launcher in
// startTransport (Phase 2a-A); every other arm wraps the go-plugin Run stream
// (client.Run, unchanged) below the seam. Above-the-seam (this call site + the
// observation wrap) references only vpio types, so the swap is invisible here.
func (st *runState) launchSession(sio sessionIO) error {
	launcher := st.interactiveLauncher
	if launcher == nil {
		launcher = goplugin.NewLauncher(st.client, st.req)
	}
	session, err := launcher.Start(st.ctx, vpio.ProcessSpec{
		Stdin:  sio.stdin,
		Stdout: sio.stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("failed to start plugin: %w", err)
	}
	pumpResize(session, sio.resize)
	status, err := session.Wait()
	sio.restore()
	if err != nil {
		return fmt.Errorf("AI plugin failed: %w", err)
	}

	// Best-effort: capture failure warns but must never fail an
	// otherwise-successful (or otherwise-failed — the exit code below
	// is unaffected either way) run. Captured even on a nonzero exit:
	// partial prose on stdout is still real memory of what happened.
	var captureErr error
	if sio.capture != nil {
		captureErr = recordOneshotAnswer(st.activeHarp, st.backendName, st.prompt, sio.capture.String())
	}

	// Interactive-pty exit seam for vendor-transcript import
	// (docs/transcript-schema.md §8's "interactive-pty gap"):
	// the structured tee (transcript.Tee/TeeAndClose) never reaches a pty,
	// so this is the ONLY place ctxloom can turn the just-exited engine's
	// OWN transcript into canonical memory. Mirrors oneshotCapture's own
	// "capture even on a nonzero exit" note just above — a session that
	// errored out mid-way still has real prior turns worth keeping.
	// Best-effort exactly like RecordOneshot: a lookup/convert failure
	// warns, never fails the run.
	if st.mode == pb.ExecutionMode_INTERACTIVE {
		convertVendorTranscriptOnExit(st.activeHarp)
	}

	if status.Code != 0 {
		return &ExitError{Code: int(status.Code)}
	}
	// The engine's own exit code wins when it is nonzero: it already said
	// what went wrong. Only an otherwise-GREEN run that produced no answer
	// falls through to this.
	if captureErr != nil {
		return captureErr
	}

	return nil
}

// finalizeRunPrompt applies the last two steps of prompt resolution, after the
// flag / saved-command / positional-args sources have had their turn: read the
// piped-stdin source a `--one-shot` run may be using, and refuse a `--one-shot` run
// that ends up with nothing to say.
//
// Both steps used to be missing. An unreadable pipe was swallowed
// (`if data, rerr := io.ReadAll(os.Stdin); rerr == nil`, the error dropped),
// and nothing downstream rejected an empty ONESHOT prompt, so
// `broken-producer | ctxloom run --one-shot` launched a headless engine with an
// empty prompt and exited 0 having asked nothing. A one-shot gets exactly one
// turn; an empty one delivers nothing at all. Interactive runs are untouched —
// an empty prompt there legitimately means "open a session".
func finalizeRunPrompt(prompt string, print, stdinPiped bool, stdin io.Reader) (string, error) {
	if prompt == "" && print && stdinPiped {
		data, rerr := io.ReadAll(stdin)
		if rerr != nil {
			return "", fmt.Errorf("--one-shot: the prompt was to be read from stdin and stdin could not be read: %w", rerr)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if print && prompt == "" {
		return "", errors.New("--one-shot: nothing to run — no prompt was given by --prompt, --command, positional " +
			"arguments or piped stdin, and a one-shot run gets exactly one turn, so an empty prompt asks nothing at all")
	}
	return prompt, nil
}

// recordOneshotAnswer is the single seam both `--one-shot` arms use to close out a
// one-shot run: the go-plugin/Backend.Execute arm (run.go) and the Transport-2
// container arm (runOneshotViaCoord). It records the two-entry canonical
// transcript and reports the zero-answer case as a failure.
//
// The container arm warned about an empty answer and returned nil
// anyway; the go-plugin arm had no check at all and simply handed the empty
// string to RecordOneshot, which treats "nothing to record" as a legitimate
// no-op. Either way `ctxloom run --one-shot ... > out.txt` produced an empty file
// and exit 0. There is no legitimately-empty one-shot answer — one question was
// asked and none was answered — so it exits nonzero and says so.
//
// Transcript capture itself stays best-effort: losing capture must never change
// the exit code of a run that DID answer.
func recordOneshotAnswer(harp, backend, prompt, answer string) error {
	if strings.TrimSpace(answer) == "" {
		clidiag.Warn("ctxloom", "one-shot run produced no answer text: the engine started and finished "+
			"without emitting a single answer byte, so there is nothing to print or record")
		return &ExitError{Code: 1}
	}
	if terr := transcript.RecordOneshot(harp, backend, prompt, answer); terr != nil {
		clidiag.Warn("ctxloom", "oneshot transcript capture: %v", terr)
	}
	return nil
}

// convertVendorTranscriptOnExit runs the vendor-transcript heal
// (operations.ResolveAndHeal) for an interactive-pty session that just
// exited. Extracted to its own small, directly-unit-testable function (no
// goplugin/pty involved) rather than inlined at the call site above,
// mirroring how transcript.RecordOneshot itself is a standalone function the
// oneshot branch just calls. A blank harp (no session identity — e.g.
// AssignSession failed earlier and the run proceeded unharped) or an
// unindexed harp are silent no-ops; any other lookup/heal failure is warned,
// never returned, so a transcript-import hiccup can never fail an otherwise-
// successful interactive run.
//
// Path H (the pty-exit defect): this used to call operations.ConvertVendorTranscript directly, whose
// presence guard makes it a PERMANENT NO-OP once any canonical transcript
// exists for the harp. A session where the user ran /recover mid-flight
// materializes exactly such a canonical file — so every session that used
// /recover got NO final capture at exit, and everything after that /recover
// was invisible to every later distill: silent no-op, exit 0, looks
// complete. LivenessFinished now means refresh once, unconditionally — this
// IS the one call site that semantic change exists for (see
// operations.Liveness's doc): a canonical file existing here is not evidence
// it is complete.
func convertVendorTranscriptOnExit(harp string) {
	if harp == "" {
		return
	}
	// A FRESH background context, deliberately NOT the run's own ctx: that
	// one is signal.NotifyContext-derived (this file's RunE, `ctx, stopSignals
	// := signal.NotifyContext(...)`), so it is already Done() by the time
	// this runs whenever the interactive session ended via the same Ctrl-C
	// that stops most interactive TUIs — arguably the MOST common clean-exit
	// path. Reusing it would make the heal abort immediately
	// (vendorreader.VendorAdapter implementations check ctx.Err() up front) on
	// exactly the sessions this hook most needs to capture.
	src, err := operations.ResolveAndHeal(context.Background(), harp, operations.LivenessFinished)
	if err != nil {
		clidiag.Warn("ctxloom", "vendor transcript import: look up %s: %v", harp, err)
		return
	}
	if src.Entry == nil {
		return
	}
	if src.HealErr != nil {
		clidiag.Warn("ctxloom", "vendor transcript import: %v", src.HealErr)
	}
}

// stampHostTerminalEnv copies the host's TERM/COLORTERM into req.Options.Env
// (never clobbering a value the caller already set), so the in-container engine
// child renders in the terminal the user is actually watching — the docker-exec
// counterpart of isolation.hostTerminalEnv on the go-plugin container path. It
// is what keeps the engine's color intact even though the turn PROCESS runs
// under TERM=dumb (the Launcher's query-suppression).
func stampHostTerminalEnv(req *pb.RunStart) {
	if req.Options == nil {
		req.Options = &pb.RunOptions{}
	}
	if req.Options.Env == nil {
		req.Options.Env = map[string]string{}
	}
	for _, k := range []string{"TERM", "COLORTERM"} {
		if v := os.Getenv(k); v != "" {
			if _, set := req.Options.Env[k]; !set {
				req.Options.Env[k] = v
			}
		}
	}
}

// runTransportArm is the transport a top-level built-in run drives its engine
// over. The three arms are exhaustive and mutually exclusive over the two
// inputs runTransport takes, and naming the go-plugin arm is half the point:
// as an unnamed `else` no single place stated the whole decision, so a fourth
// input combination could only be reasoned about by reading two predicates in
// two files and inferring what neither covered.
//
//   - armGoPlugin: SpawnClient + go-plugin. Every host/worktree run of any
//     mode; none of them had the container-state problem the container arms fix.
//   - armDockerExecInteractive: Phase 2a-A. The interactive turn runs via
//     `docker exec` against a StartRunner keepalive container.
//   - armOwnedRunContainer: Phase 2a-B. An owner-owned run watched over the
//     in-process coordinator; the container dials out on Transport 2 and opens
//     no in-container listener.
type runTransportArm int

const (
	armGoPlugin runTransportArm = iota
	armDockerExecInteractive
	armOwnedRunContainer
)

// runTransport decides a run's transport arm. Only a container policy ever
// leaves the go-plugin arm, so a container policy NEVER reaches SpawnClient and
// a host/worktree policy ALWAYS does — a leak either way is the regression this
// decision exists to prevent.
func runTransport(policyName string, mode pb.ExecutionMode) runTransportArm {
	if !isolation.IsContainerPolicyName(policyName) {
		return armGoPlugin
	}
	if mode == pb.ExecutionMode_ONESHOT {
		return armOwnedRunContainer
	}
	return armDockerExecInteractive
}

// startContainerInteractive is the Phase 2a-A docker-exec arm: it launches the
// StartRunner keepalive container (Phase 1's `llm host` primitive — same
// workspace mounts, auth env, session-state mounts, teardown-by-name), hands
// the resolved RunStart off by a 0600 file in the bind-mounted persist dir
// (never argv/env), and returns a docker-exec vpio.Launcher that runs the
// interactive turn via `docker|podman exec -it <container> ctxloom llm turn`.
// The keepalive carries NO coordinator reach-back (it just blocks); the trio
// rides the exec into the turn process, which stands up its own runner-MCP —
// so exactly one process dials home, mirroring the single top-level runner the
// go-plugin path spawns. No in-container listener; teardown is RunnerHandle.Kill.
func startContainerInteractive(ctx context.Context, policy isolation.Policy, ws isolation.Workspace, req *pb.RunStart, backendName, label string, verbosity int, harp string, runnerEnv map[string]string) (*isolation.RunnerHandle, vpio.Launcher, error) {
	rt := operations.RuntimeForPolicy(policy)
	if rt == nil {
		return nil, nil, fmt.Errorf("container interactive: policy %q exposes no launch runtime", policy.Name())
	}
	persistDir := operations.ContainerPersistDirForPolicy(policy, harp)
	if persistDir == "" {
		return nil, nil, fmt.Errorf("container interactive: no session harp — the RunStart handoff needs the bind-mounted persist dir")
	}

	// The Launcher forces the TURN process's TERM=dumb (to silence ctxloom's
	// init-time terminal query, which would eat the user's first keystrokes off
	// this shared stdin). Stamp the REAL terminal description into the engine's
	// env (RunStart.Options.Env, which the child's BuildEnv overlays over the
	// turn's os.Environ) so the engine child still renders in full color —
	// mirroring hostTerminalEnv on the go-plugin container path.
	stampHostTerminalEnv(req)

	// Hand off RunStart by file BEFORE the container starts (the persist dir is
	// the bind SOURCE, created here and by the session-state mounts alike).
	if _, err := writeRunStartHandoff(harp, req); err != nil {
		return nil, nil, err
	}
	startPath := path.Join(persistDir, runStartHandoffFile)

	// Keepalive env: session identity only — NO reach-back trio, so the `llm
	// host` keepalive degrades to standup+block without dialing home (the turn
	// owns the single dial). The container's auth/TERM/git env ride the
	// workspace's own mounts+env, not this map.
	keepaliveEnv := map[string]string{}
	if harp != "" {
		keepaliveEnv["CTXLOOM_SESSION_HARP"] = harp
	}
	handle, err := policy.StartRunner(ctx, backendName, label, verbosity, ws, keepaliveEnv)
	if err != nil {
		return nil, nil, err
	}

	launcher := dockerexec.NewLauncher(rt, handle.Name, dockerexec.TurnSpec{
		Backend:   backendName,
		Label:     label,
		StartPath: startPath,
		// The full reach-back trio (+ harp) crosses to the turn as bare `-e
		// NAME` (values on the exec subprocess env, never argv), so the turn's
		// runner-MCP standup can dial the session coordinator.
		Env: runnerEnv,
	})
	return handle, launcher, nil
}

// mergeWorkspaceEnv layers the isolation-resolved per-engine config-home env
// (isolation.WorkspaceEnv's CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME/... — nil
// for a none/container workspace) UNDER an already-assembled env map, so an
// explicit user/session var set before isolation resolves still wins over a
// resolved isolation var: this must fill gaps, never clobber a caller-set
// env. Mirrors the fan-out member path's workspaceEnv/ExtraEnv precedence
// (operations/oneshot.go's runResolvedAgent). A nil/empty workspaceEnv is a
// true no-op — existing is returned unchanged (not copied), so the top-level
// run's shared/none path (the overwhelming common case) allocates nothing new.
func mergeWorkspaceEnv(existing, workspaceEnv map[string]string) map[string]string {
	if len(workspaceEnv) == 0 {
		return existing
	}
	merged := make(map[string]string, len(workspaceEnv)+len(existing))
	maps.Copy(merged, workspaceEnv)
	maps.Copy(merged, existing)
	return merged
}

// warnBypassOnLostContainer reports whether the launch should warn that a
// container-requested run lost its boundary and now runs full-auto on the bare
// host: the runtime axis asked for a container, the PREPARED policy is not
// container-backed (neither container nor container-worktree — a satisfied
// request, including a successful container-worktree, never warns), and the
// resolved posture is bypass. The claude-code host stopgap is exempt:
// bypass-on-host is its intended posture.
func warnBypassOnLostContainer(axes isolation.Axes, preparedName string, permMode agent.PermissionMode, backendName string) bool {
	return axes.WantsContainer() && !isolation.IsContainerPolicyName(preparedName) &&
		permMode == agent.PermissionBypass && backendName != config.BackendClaudeCode
}

// resolvePermissionMode picks the launch-time permission posture for the top-level
// run. Precedence: the explicit --permissions flag, then the --agent binding, then
// the engine label's configured permissions, then THIS PROJECT DIRECTORY's
// declared default (config.yaml's top-level `permissions:`), then the built-in
// default. The built-in default is bypass for claude-code (the host stopgap while
// container isolation isn't relied on) and default (prompt) for every other
// backend. Config/CLI is authoritative: the isolation boundary no longer earns or
// drops bypass. A non-interactive ONESHOT has no human to answer the engine, so a
// would-block posture (default/acceptEdits) upgrades to bypass or it hangs.
//
// projectPerm sits LAST among the declarations and BEFORE the built-in default,
// and both halves of that placement are load-bearing:
//
//   - Below flag/agent/label, so a project default can never widen a posture
//     someone declared somewhere more specific. Precedence, not "most
//     restrictive wins": a binding may still declare a WIDER posture than the
//     project default, exactly as it may today against the built-in one.
//   - Above the built-in default, so a declared project posture beats the
//     claude-code host stopgap. The stopgap exists for the case where nobody
//     stated a posture at all; a project that states one has answered the
//     question it was standing in for, and leaving the stopgap on top would
//     make `permissions: plan` in a claude-code project silently mean bypass —
//     the exact silent widening this chain is built to prevent.
//
// projectPerm reaches here from config.GetPermissions(), which can only ever
// carry a value THIS project's .ctxloom/config.yaml (or an explicit
// --config-set) wrote: layerscope drops the key from a home config or the
// environment before the merge. See config.Config.permissions.
func resolvePermissionMode(flag, agentPerm, labelPerm, projectPerm, backendType string, mode pb.ExecutionMode, backendEnforcesPlan bool) agent.PermissionMode {
	m, honoured := agent.ResolveDefault([]string{flag, agentPerm, labelPerm, projectPerm}, backendType == config.BackendClaudeCode)
	if !honoured {
		// A declared posture that does not parse is already floored to the most
		// restrictive tier and reported as a fatal finding. It returns AS IS:
		// every step below widens (the plan collapse trades read-only for
		// prompt-per-call; the ONESHOT floor trades prompt-per-call for
		// bypass), and running both on a floored value would walk a typo back
		// up to the posture the floor exists to deny it.
		return m
	}
	// plan is only a genuine read-only posture on backends that enforce it. On a
	// backend with no read-only tier it collapses to default — the nearest posture
	// that still gates each tool call on a human. Interactive: that prompts; a
	// headless ONESHOT then floors default up to bypass below (it can't hang),
	// which the caller warns about.
	m = m.CollapsePlanIfUnenforced(backendEnforcesPlan)
	if mode == pb.ExecutionMode_ONESHOT && !m.SafeHeadless() {
		m = agent.PermissionBypass
	}
	return m
}

// requestedPermission returns the posture the user or config asked for — the
// first parseable of flag > agent > label > project default — independent of any
// backend collapse. ok is false when nothing parseable was requested (so the
// caller falls back to a built-in default). It is the input to the "backend
// can't honor this" warning.
//
// The project default counts as a REQUEST, deliberately: a project that pinned
// `permissions: plan` on a backend with no read-only tier must be told the pin
// collapsed. That is the same silent widening the warning exists to surface, and
// it does not stop being one because the declaration lived in the project file
// rather than on a binding.
func requestedPermission(flag, agentPerm, labelPerm, projectPerm string) (agent.PermissionMode, bool) {
	for _, s := range []string{flag, agentPerm, labelPerm, projectPerm} {
		if pm, ok := agent.ParsePermissionMode(s); ok {
			return pm, true
		}
	}
	return agent.PermissionDefault, false
}

// validatePermissionFlag rejects an explicitly-typed --permissions value that
// isn't a known posture, up front (friction like an unknown --llm). A typo such
// as "plann" must not silently fall through to a more permissive default — on
// claude-code that would be the host bypass, the opposite of the restraint the
// user typed. An empty flag is no override. Config-sourced agent/label postures
// stay fault-tolerant (warn + fall through) — only the value typed now is strict.
func validatePermissionFlag(flag string) error {
	if flag == "" {
		return nil
	}
	if _, ok := agent.ParsePermissionMode(flag); !ok {
		return fmt.Errorf("unknown --permissions %q; valid: %s",
			flag, strings.Join(agent.PermissionModeNames(), "|"))
	}
	return nil
}

// completePermissionModes offers the permission-posture values for shell
// completion of `run --permissions`.
func completePermissionModes(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return agent.PermissionModeNames(), cobra.ShellCompDirectiveNoFileComp
}

// resolveRunLLM picks the config label to launch. Precedence: an explicit
// --llm override (validated up front — an unknown value is a hard error, since
// the user typed it now), else the profile's declared llm, else the primary
// role's label. A misconfigured profile.llm is fault-tolerant per CLAUDE.md: it
// warns and falls back to the primary label rather than blocking startup.
func resolveRunLLM(cfg *config.Config, override, profileLLM string) (string, error) {
	if override != "" {
		return validateExplicitLLM(cfg, override)
	}
	if profileLLM != "" {
		if validated, err := validateExplicitLLM(cfg, profileLLM); err == nil {
			return validated, nil
		} else {
			clidiag.Warn("ctxloom",
				"profile-declared llm %q is unusable (%v); falling back to the primary role",
				profileLLM, err)
		}
	}
	return cfg.PrimaryLabel(), nil
}

// validateExplicitLLM validates a non-empty --llm override (friction-up-front):
// it must be a configured label, or a registered backend type whose binary is
// installed (treated as an ad-hoc label). Returns the validated label or an
// error naming what is usable now. Shared by `run` and `bundle distill` so an
// unknown --llm is reported rather than silently swallowed.
func validateExplicitLLM(cfg *config.Config, override string) (string, error) {
	// A configured label is trusted (the user set up its backend/binary/args).
	if _, configured := cfg.GetLLMEntry(override); configured {
		return override, nil
	}
	// Otherwise allow naming a registered backend type whose binary is present.
	if backends.Exists(override) && backends.IsAvailable(override) {
		return override, nil
	}
	if backends.Exists(override) {
		return "", fmt.Errorf("LLM %q is a known backend but not configured and its binary is not installed; usable now: %s",
			override, strings.Join(usableLLMs(cfg), ", "))
	}
	return "", fmt.Errorf("unknown LLM %q; usable now: %s", override, strings.Join(usableLLMs(cfg), ", "))
}

// usableLLMs returns what can be launched right now: every configured label,
// plus any registered backend type whose binary is installed. "mock"
// (test-only) is excluded.
func usableLLMs(cfg *config.Config) []string {
	set := map[string]bool{}
	for _, label := range cfg.GetLLMLabels() {
		set[label] = true
	}
	for _, name := range backends.List() {
		if isMockBackend(name) {
			continue
		}
		if backends.IsAvailable(name) {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVarP(&runLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast, codex); overrides the configured default")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "command", "r", "", "Run a saved command by name")
	runCmd.Flags().StringSliceVarP(&runFragments, "fragment", "f", nil, "Context fragment(s) to include (can be repeated)")
	runCmd.Flags().StringSliceVarP(&runTags, "tag", "t", nil, "Include fragments with this tag (can be repeated)")
	runCmd.Flags().StringVarP(&runProfile, "profile", "p", "", "Profile to use (predefined fragment collection)")
	runCmd.Flags().StringVar(&runAgent, "agent", "", "Run a named local agent binding: its composed profiles, engine, and runtime (excludes -p/-f/-t)")
	runCmd.Flags().StringVar(&runWorkspace, "workspace", "", "Session workspace axis (none|worktree; empty = project default)")
	runCmd.Flags().StringVar(&runPermissions, "permissions", "", "Permission posture: default|acceptEdits|plan|bypass (overrides the agent/config default)")
	runCmd.MarkFlagsMutuallyExclusive("agent", "profile")
	runCmd.MarkFlagsMutuallyExclusive("agent", "fragment")
	runCmd.MarkFlagsMutuallyExclusive("agent", "tag")
	_ = runCmd.RegisterFlagCompletionFunc("agent", completeAgentNames)
	_ = runCmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
	_ = runCmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
	runCmd.Flags().BoolVarP(&runDryRun, "dry-run", "n", false, "Show command that would be executed")
	runCmd.Flags().BoolVar(&runOneShot, "one-shot", false, "Run one turn non-interactively, print the response, and exit")
	runCmd.Flags().BoolVar(&runPlainTerminal, "plain-terminal", false, "Disable ctxloom's terminal layer (the prefix-key agent viewer and the surround status bar) for this session")
	runCmd.Flags().CountVarP(&runVerbosity, "verbose", "v", "Increase verbosity (can be repeated: -v, -vv, -vvv)")
	runCmd.Flags().BoolVarP(&runAssumeYes, "yes", "y", false, "Assume yes for the install-on-startup prompt")

	// Deterministic resume (two modes; see resumeFullContext/resumeDistillEnv):
	// bare --session folds the harp's full recorded transcript into this run's
	// assembled context; --session --distill resumes via its distilled essence
	// instead, distilling on demand first if one doesn't exist yet.
	runCmd.Flags().StringVar(&runResumeSession, "session", "", "Resume the named harp session: folds its full recorded transcript into this run's assembled context. Combine with --distill to resume via its distilled essence instead.")
	runCmd.Flags().BoolVar(&runResumeDistill, "distill", false, "With --session, resume via the harp's distilled essence instead of its full transcript (distills on demand first if not yet distilled)")

	// Internal: used by `ctxloom tasks run` to seed one browsed task into the
	// new session's store. Hidden — not part of the public run surface.
	runCmd.Flags().StringVar(&runSeedTask, "seed-task", "", "Move the named task (harp id) from the resume source store into this session, marked for active work")
	runCmd.Flags().StringVar(&runSeedStatus, "seed-status", "", "Status to set on the seeded task (default: \"In Progress\")")
	_ = runCmd.Flags().MarkHidden("seed-task")
	_ = runCmd.Flags().MarkHidden("seed-status")

	// Register completions
	_ = runCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = runCmd.RegisterFlagCompletionFunc("fragment", completeFragmentNames)
	_ = runCmd.RegisterFlagCompletionFunc("tag", completeTagNames)
	_ = runCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = runCmd.RegisterFlagCompletionFunc("command", completePromptNames)
}

// confirmSyncInstall returns true if startup sync should proceed.
// In an interactive terminal with pending installs, it lists them and asks
// for y/N confirmation. Non-interactive contexts (CI, piped) and --yes
// auto-confirm so they don't hang. On any check error, it falls through to
// the existing graceful-failure path in SyncOnStartup.
// confirmUpgrade offers to persist a schema upgrade that loading applied in
// memory (config or session index). This is the only place ctxloom rewrites such
// a file on startup, and only with consent: with -y it commits; outside an
// interactive terminal it leaves the file untouched and the upgrade simply stays
// in memory for this run (the next interactive run prompts again). A nil pending
// means the file was already current.
func confirmUpgrade(p *upgrade.Pending, commit func() error) {
	if p == nil {
		return
	}
	if runAssumeYes {
		commitUpgrade(p, commit)
		return
	}
	if !isInteractiveTerminal() {
		return // in-memory only — never a silent rewrite
	}

	fmt.Fprintf(os.Stderr, "ctxloom: %s is an older schema (%s).\n", p.Path, strings.Join(p.Applied, ", "))
	if yes, err := promptYesNo("Rewrite it to the current format? [y/N] "); err == nil && yes {
		commitUpgrade(p, commit)
	}
}

// confirmProfileUpgrades offers to persist any older-schema rewrites that loading
// the configured profiles applied in memory (e.g. bare bundle refs qualified with
// their remote). It resolves each of the default agent's composed profiles through
// one loader — which loads parents too — so every pending rewrite is surfaced,
// then prompts per file via the shared confirmUpgrade path. No pending means every
// profile was current (profiles.defaults was retired — see DefaultAgentProfiles).
func confirmProfileUpgrades(cfg *config.Config) {
	loader := cfg.GetProfileLoader()
	for _, name := range cfg.DefaultAgentProfiles() {
		_, _ = loader.ResolveProfile(name, nil)
	}
	for _, p := range loader.PendingUpgrades() {
		confirmUpgrade(p, func() error { return loader.CommitUpgrade(p) })
	}
}

// commitUpgrade persists a pending upgrade, warning (never fatal) on failure —
// the in-memory config is valid regardless.
func commitUpgrade(p *upgrade.Pending, commit func() error) {
	if err := commit(); err != nil {
		clidiag.Warn("ctxloom", "could not rewrite %s: %v", p.Path, err)
	}
}

func confirmSyncInstall(ctx context.Context, cfg *config.Config) bool {
	if runAssumeYes || !isInteractiveTerminal() {
		return true
	}

	check, err := operations.CheckMissingDependencies(ctx, cfg, operations.CheckMissingDependenciesRequest{})
	if err != nil || check == nil || check.Count == 0 {
		return true
	}

	fmt.Fprintf(os.Stderr, "ctxloom will install %d missing dependenc%s:\n", check.Count, plural(check.Count, "y", "ies"))
	for _, dep := range check.Missing {
		fmt.Fprintf(os.Stderr, "  - %s (%s, from profile %q)\n", dep.Reference, dep.Type, dep.Profile)
	}
	yes, err := promptYesNo("Proceed? [y/N] ")
	if err != nil || !yes {
		fmt.Fprintln(os.Stderr, "ctxloom: skipping sync")
		return false
	}
	return true
}
