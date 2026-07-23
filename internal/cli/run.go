package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
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
	// runSavedPromptDeprecated backs the deprecated --run-prompt alias (F9: the
	// "prompt" vocabulary is stale — a saved, user-invoked item is a "command",
	// matching the resources/commands/ terminology and the command item-kind).
	// Reconciled into runSavedPrompt in RunE before it's read; kept only so
	// existing scripts/invocations using --run-prompt keep working.
	runSavedPromptDeprecated string
	runDryRun                bool
	runPrint                 bool
	runStructured            bool
	runPlainTerminal         bool
	runVerbosity             int
	runAssumeYes             bool
	runSeedTask              string
	runSeedStatus            string
	// runResumeSession/runResumeDistill are the two deterministic-resume flags
	// (restored after WS-4/5 removed all flag-based resume + the interactive
	// picker — see resolveResumeIntentWith's deletion in c7cddd9). Unlike the
	// old picker-era flags (--tasks-from/--new-session/--no-tasks), this is a
	// single axis with two modes:
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
// out. Production points it at exec.CommandContext (FINDING #3: the
// exit-path caller needs to be able to kill a stalled distill via ctx
// cancellation); tests substitute a fake that records the arguments and
// returns a harmless exec.Cmd (e.g., /bin/true) so .Run() succeeds without
// side effects.
var execCommand = exec.CommandContext

// shellOutDistill is the exit path's (distillSessionOnExit) distill
// implementation. It runs `ctxloom session distill <harp>` as a child process
// so this file doesn't need to depend on the compactor or any LLM machinery
// itself. Stdout/stderr are piped through to the user.
//
// ctx bounds the child via exec.CommandContext: when ctx is cancelled the
// stdlib kills the process. distillSessionOnExit derives ctx from
// context.WithTimeout(exitDistillTimeout) — see its own doc for why exit-time
// distillation is bounded rather than unbounded.
func shellOutDistill(ctx context.Context, harpName string) error {
	exe := resolveSelfExecutable()
	c := execCommand(ctx, exe, "session", "distill", harpName)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

// shouldDistillOnExit decides whether the just-ended session at activeHarp
// should be synchronously distilled when `ctxloom run` exits. It mirrors the
// guard MarkSessionEnded already uses (a bound harp) and additionally
// requires an INTERACTIVE run — the caller folds in two disqualifying cases:
//
//   - structured-REPL runs: --structured returns via runStructuredREPL before
//     goplugin.NewLauncher(...).Start ever runs Setup, so a structured
//     session never gets its session_id bound by the SessionStart hook —
//     there would be nothing for `session distill` to find.
//   - oneshot/--print runs (FINDING #2): a headless `ctxloom run -p X --print`
//     mints a fresh harp on every invocation, so the idempotency check
//     (essenceFn) never short-circuits — without this gate, distillation
//     would fire as a blocking LLM call at the end of EVERY headless call.
func shouldDistillOnExit(activeHarp string, interactive bool) bool {
	return activeHarp != "" && interactive
}

// exitDistillTimeout bounds distillSessionOnExit's synchronous exit-time
// distill (FINDING #3): a stalled LLM/network call at process-exit time must
// not wedge the exiting shell forever.
const exitDistillTimeout = 120 * time.Second

// distillSessionOnExit runs the exit-time distill decided by
// shouldDistillOnExit. essenceFn/distillFn are injected (readHarpEssence/
// shellOutDistill in production) so the decision logic is unit-testable
// without shelling out or touching the filesystem.
//
// BLOCKING IS DELIBERATE (user decision, not an oversight): distillation
// used to happen lazily, only when a future session's resume path (now
// removed — see Decision 11) needed the essence. The user chose to pay for it
// here instead, so a session's LLM-driven distill happens synchronously on
// exit rather than being deferred onto some future session's startup —
// `ctxloom run` visibly pauses on "distilling session <harp>…" before the
// shell gets control back.
//
// BOUNDED, NOT UNBOUNDED (FINDING #3): the block above is deliberate, but
// unbounded is not — a stalled LLM/network call at process-exit time must
// not wedge the exiting shell forever. distillFn is run under a
// context.WithTimeout(timeout) derived from context.Background() (NOT tied
// to the run's own shutdown-signal context, so a Ctrl-C exit still gets the
// full budget rather than an already-cancelled context). On timeout,
// distillFn is left to be killed by its own ctx-cancellation handling
// (shellOutDistill uses exec.CommandContext, so the child process is
// killed) and distillSessionOnExit returns without error — a manual
// `ctxloom session distill <harp>` (or the "resume" skill, which redistills a
// still-live session's growing transcript) picks up the incomplete distill
// later.
//
// Idempotent: skipped if an essence already exists for the harp.
func distillSessionOnExit(activeHarp string, interactive bool, essenceFn func(string) ([]byte, error), distillFn func(context.Context, string) error, timeout time.Duration, out io.Writer) {
	if !shouldDistillOnExit(activeHarp, interactive) {
		return
	}
	if _, essErr := essenceFn(activeHarp); essErr == nil {
		return // already distilled
	}
	fmt.Fprintf(out, "ctxloom: distilling session %s…\n", activeHarp)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- distillFn(ctx, activeHarp) }()

	select {
	case dErr := <-done:
		if dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s on exit: %v", activeHarp, dErr)
			return
		}
		fmt.Fprintf(out, "ctxloom: distilled session %s\n", activeHarp)
	case <-ctx.Done():
		fmt.Fprintf(out, "ctxloom: distillation timed out; it will complete on next startup\n")
	}
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
// pre-WS-4/5 picker-driven --session resume used. PARTS is "session" (not
// "tasks" — task restoration was removed along with the picker and is not
// coming back here) so resumePartsIncludeSession's essence gate opens.
//
// essenceFn/distillFn mirror distillSessionOnExit's own injection (production:
// readHarpEssence/shellOutDistill — the SAME `session distill` compactor path
// as the exit-time auto-distill, session_cmd.go's runSessionDistill/
// compactEntry/memory.NewCompactor) so distill-on-demand is unit-testable
// without shelling out. A distill failure warns rather than blocking launch;
// the SessionStart hook's own readHarpEssence call then simply finds nothing
// and omits the essence block.
func resumeDistillEnv(harp string, essenceFn func(string) ([]byte, error), distillFn func(context.Context, string) error) map[string]string {
	if _, err := essenceFn(harp); err != nil {
		// Unbounded context.Background(): unlike the exit-path distill
		// (FINDING #3), this runs before the session's terminal is handed to
		// the user, not after process exit — no shell to unblock.
		if dErr := distillFn(context.Background(), harp); dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s for resume essence: %v", harp, dErr)
		}
	}
	return map[string]string{
		"CTXLOOM_RESUMED_FROM":  harp,
		"CTXLOOM_RESUMED_PARTS": "session",
	}
}

// resolveSelfExecutable returns the path to use when re-invoking ctxloom
// from inside a running ctxloom process, surviving in-place upgrades that
// unlink the executing inode. The logic lives in internal/selfexec so the
// gRPC client (which cmd cannot be imported by) shares it.
func resolveSelfExecutable() string {
	return selfexec.Path()
}

// seedTaskIntoSession marks the task with harpID In Progress (or the given
// status) in the project's task log, attributing the change to the new session
// (activeHarp). Tasks are project-scoped now (ADR 0025), so seeding is a status
// change rather than a move between per-session stores.
//
// A failure is a FATAL ClassTask finding, not a bare warning (worst-pony).
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
	RunE: func(cmd *cobra.Command, args []string) error {
		// Fail-loudly gate: checkpoint before any startup choke fires, so every
		// fatal finding collected across config load, sync, and assembly is
		// caught at one place (failOnFindings below) and the launch aborts with
		// the full list. Degraded mode records nothing, so the gate is a no-op.
		startupMark := strictness.Checkpoint()

		// Friction up front: a typed --permissions value that isn't a known posture
		// is a hard error before any work, so a typo can't silently resolve to a
		// more permissive default. Config-sourced postures stay fault-tolerant.
		if err := validatePermissionFlag(runPermissions); err != nil {
			return err
		}
		if err := validateResumeFlags(runResumeSession, runResumeDistill); err != nil {
			return err
		}

		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		// config.Load downgrades unreadable/malformed/schema-invalid files to
		// warnings (CLAUDE.md fault tolerance) — surface them so a corrupted
		// config.yaml never silently launches an empty-context session.
		printConfigWarnings(os.Stderr, cfg.GetWarnings())
		// If loading upgraded an older config schema in memory, offer to persist
		// it (interactive + consented only; never a silent rewrite).
		confirmUpgrade(cfg.GetPendingUpgrade(), cfg.CommitUpgrade)
		// The HOME layer gets the same offer when a project config also exists
		// (long-ice). Without this, a stale ~/.ctxloom/config.yaml was upgraded
		// in memory on every load forever and never converged. The prompt names
		// the path, so consenting to rewrite HOME is an informed choice rather
		// than a surprise side effect of a project-scoped run.
		confirmUpgrade(cfg.GetHomePendingUpgrade(), cfg.CommitHomeUpgrade)
		// Profiles can carry an older schema too (e.g. bare bundle refs); offer to
		// persist those rewrites the same way.
		confirmProfileUpgrades(cfg)

		// F9: reconcile the deprecated --run-prompt alias into --command before
		// either is read. --command (the flag actually parsed) wins if somehow
		// both were passed — an explicit current-vocabulary flag beats a
		// deprecated one rather than silently being overridden by it.
		if runSavedPrompt == "" && runSavedPromptDeprecated != "" {
			runSavedPrompt = runSavedPromptDeprecated
		}

		// Build the prompt - from saved command, flag, or remaining args
		// Empty prompt is allowed (starts interactive mode)
		prompt := runPrompt
		if prompt == "" && runSavedPrompt != "" {
			promptRes, err := operations.GetCommand(cmd.Context(), cfg, operations.GetCommandRequest{Name: runSavedPrompt})
			if err != nil {
				return fmt.Errorf("failed to load command: %w", err)
			}
			prompt = promptRes.Content
		}
		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}
		// In --print (oneshot) mode with no prompt yet, read it from piped stdin.
		// This makes `run --print` a universal reducer: `… | ctxloom run -p synth
		// --print` synthesizes over any piped input (e.g. `ctxloom map` output or
		// non-ctxloom text). Skipped on a TTY so an interactive read never blocks.
		if prompt == "" && runPrint && stdinIsPiped() {
			if data, rerr := io.ReadAll(os.Stdin); rerr == nil {
				prompt = strings.TrimSpace(string(data))
			}
		}

		// Assemble context using operations. The context carries shutdown
		// signals so SIGTERM/SIGHUP unwind through the defers — terminal
		// restore, the session end-mark, client.Kill — instead of killing the
		// process mid-raw-mode. (Interactive ^C is raw-mode input forwarded to
		// the child, not a SIGINT to us.)
		ctx, stopSignals := signal.NotifyContext(cmd.Context(), shutdownSignals...)
		defer stopSignals()

		// Auto-sync remote dependencies on startup if enabled (graceful failure).
		// Mirrors the behavior of `ctxloom mcp` so the run path doesn't hard-fail
		// on missing parent profiles or bundles that sync would have fetched.
		// In a TTY, confirm with the user before installing anything new.
		// Skipped under --dry-run: a dry run must be side-effect free and
		// non-interactive (no network, no installs, no confirm prompt), so it
		// previews assembly against the library as it exists on disk.
		syncCfg := cfg.GetSyncConfig()
		if syncCfg.ShouldAutoSync() && !runDryRun && confirmSyncInstall(ctx, cfg) {
			syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
			result, syncErr := operations.SyncOnStartup(syncCtx, cfg)
			syncCancel()
			if syncErr != nil {
				if !errors.Is(syncErr, context.Canceled) {
					strictness.Fail(strictness.ClassSync, "check the remote/network, or pass --degraded to launch anyway", "sync failed: %v", syncErr)
				}
			} else {
				writeSyncSummary(os.Stderr, result)
			}
		}

		// Items awaiting review are surfaced per-item by the content trust gate
		// during assembly (the "N item(s) awaiting review — run 'ctxloom review'"
		// advisory), not by a bundle-level lockfile diff here.

		// Log which companion binaries (taskloom, ltk) this session is wired
		// with, version-probed via `<bin> version --format json`. Skipped under
		// --dry-run, which previews assembly without executing anything.
		if !runDryRun {
			reportCompanions(os.Stderr)
		}

		// Startup reaper (bony-carry bug #2): sweep any per-agent worktree
		// checkout left behind by a crashed/killed prior run — teardown()'s
		// WIP-safe removal only ever fires on a graceful Cleanup(), so nothing
		// else ever reaps these. Best-effort, silent unless it found something.
		// Skipped under --dry-run for the same reason as reportCompanions: a dry
		// run previews assembly without any side effect.
		if !runDryRun {
			sweepOrphanedWorktrees(ctx, os.Stderr)
		}

		// Two launch sources: --agent runs a named LOCAL binding — its composed
		// profiles become the context and its engine + runtime the transport;
		// the interactive picker and the -p/-f/-t assembly do not apply (cobra
		// marks the flags mutually exclusive). Everything else is the classic
		// profile flow. An unknown --agent is a HARD error: an explicit name is
		// user intent, unlike acp's editor-serving degrade.
		var (
			ctxResult   *operations.AssembleContextResult
			label       string
			backendName string
			labelModel  string
			// agentPermissions is the --agent binding's declared permission
			// posture (empty for a classic run); the resolver layers the engine
			// label and the built-in default on top.
			agentPermissions string
			// The session's runtime axis: the agent's resolved runtime, or the
			// project `runtime:` default for a classic run.
			agentRuntime = cfg.GetRuntime()
			// boundAgent names the agent binding this run launched under
			// (--agent or the default agent) — surround-bar identity only.
			boundAgent string
		)
		if runAgent != "" {
			rs, rerr := operations.ResolveAgent(ctx, cfg, runAgent, runLLM)
			if rerr != nil {
				return rerr
			}
			ctxResult = &operations.AssembleContextResult{
				Profiles:        rs.Profiles,
				FragmentsLoaded: rs.Fragments,
				Context:         rs.Context,
			}
			// ResolveAgent already applied the --llm-beats-declared-engine
			// precedence and the project fallbacks.
			label, backendName, labelModel = rs.Label, rs.Backend, rs.Model
			agentRuntime = rs.Runtime
			agentPermissions = rs.Permissions
			boundAgent = runAgent
		} else if runProfile == "" && len(runFragments) == 0 && len(runTags) == 0 {
			// Bare launch: no --agent and no explicit context selection. Bind the
			// always-bound DEFAULT AGENT (cfg.DefaultAgent) exactly like --agent —
			// its composed profiles become the context and its engine + runtime +
			// permissions the transport (profiles.defaults was retired). Unlike
			// --agent (a HARD error on an unknown name), a missing/empty/unresolvable
			// default_agent must NEVER block startup: warn and continue with empty
			// context at the project-default label + runtime (CLAUDE.md fault
			// tolerance; mirrors acp's operations.OpenEngineSession degrade).
			if rs, rerr := operations.ResolveAgent(ctx, cfg, cfg.GetDefaultAgent(), runLLM); rerr != nil {
				strictness.Fail(strictness.ClassRef, "set a default agent (ctxloom agent default <name>) or pass --degraded to launch anyway", "default agent %q unavailable; continuing with empty context: %v", cfg.GetDefaultAgent(), rerr)
				ctxResult = &operations.AssembleContextResult{}
				var lerr error
				// resolveRunLLM: --llm override, else the project primary label.
				label, lerr = resolveRunLLM(cfg, runLLM, "")
				if lerr != nil {
					return lerr
				}
				backendName, labelModel = operations.ResolveBackend(cfg, label)
				agentRuntime = cfg.GetRuntime()
			} else {
				ctxResult = &operations.AssembleContextResult{
					Profiles:        rs.Profiles,
					FragmentsLoaded: rs.Fragments,
					Context:         rs.Context,
				}
				// ResolveAgent already applied the --llm-beats-declared-engine
				// precedence and the project fallbacks.
				label, backendName, labelModel = rs.Label, rs.Backend, rs.Model
				agentRuntime = rs.Runtime
				agentPermissions = rs.Permissions
				boundAgent = cfg.GetDefaultAgent()
			}
		} else {
			// Explicit context selection (-p / -f / -t): classic assembly.
			var aerr error
			ctxResult, aerr = operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{
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
			if len(runFragments) > 0 && len(ctxResult.MissingFragments) == len(runFragments) {
				return fmt.Errorf("no fragments loaded: requested fragments not found: %s", strings.Join(ctxResult.MissingFragments, ", "))
			}

			// Determine which LLM config to use. Resolution is deferred until after
			// context assembly because the chosen profile may declare its own LLM.
			// Precedence: explicit --llm override (validated up front, friction),
			// else the profile's declared llm, else the primary role's label. The
			// label resolves to a backend type + model; the backend name (not the
			// label) drives session naming and transport.
			var lerr error
			label, lerr = resolveRunLLM(cfg, runLLM, ctxResult.ProfileLLM)
			if lerr != nil {
				return lerr
			}
			// label → backend type + model (shared with the oneshot/map/weave path).
			// The backend name (not the label) drives session naming and transport.
			backendName, labelModel = operations.ResolveBackend(cfg, label)
		}

		// An invalid ui.prefix_key is a broken-config finding like any other:
		// recorded here so the gate below aborts before launch (a viewer on a
		// key the user didn't configure is a wrong-context session's cousin).
		validateTerminalUIConfig(cfg)

		// Strict startup gate: config load, sync, and assembly have run and any
		// fatal-class fault (broken config, unresolvable default profile/parent,
		// failed bundle load, partial hook apply) has been recorded. Abort now —
		// before launching the backend — listing every finding with its fix.
		// A dry run is gated too: previewing a broken setup should say so. In
		// degraded mode this returns nil and the launch proceeds as before.
		if ferr := failOnFindings(os.Stderr, startupMark); ferr != nil {
			return ferr
		}
		// Anchor for the SECOND gate (after isolation.Prepare, far below):
		// captured immediately after gate 1 passes so the two windows TILE — a
		// finding recorded between the gates (trust gate, session accounting,
		// hook/config work on the way to the launch) still aborts at gate 2
		// instead of falling into an ungated hole.
		postStartupMark := strictness.Checkpoint()

		llmBinary, llmArgs := llmBinaryArgsFor(cfg, label)
		llmEnv := llmEnvFor(cfg, label)

		// The session's WORKSPACE axis: the invocation flag wins, else the
		// project `workspace:` default. A session trait — never read from the
		// agent binding.
		sessionWorkspace := runWorkspace
		if sessionWorkspace == "" {
			sessionWorkspace = cfg.GetWorkspace()
		}

		// --session (full resume — no --distill): prime the assembled context
		// with the resumed harp's full recorded transcript before it's split
		// into fragments below. This rides the SAME assembled-context path
		// every other context source (--agent/default agent/-p/-f/-t) already
		// goes through — the SessionStart hook's inject-context reads it back
		// from the content-addressed cache file this context ultimately
		// writes to (agent.WriteContextFile), same as a normal launch.
		// --distill takes the essence path instead (below, once activeHarp is
		// assigned) — the two are mutually exclusive per validateResumeFlags'
		// sibling gate (neither flag depends on the other's outcome, so no
		// gate is needed here beyond the runResumeDistill check itself).
		if runResumeSession != "" && !runResumeDistill {
			ctxResult.Context = resumeFullContext(ctxResult.Context, runResumeSession, func(h string) ([]agent.SessionEntry, error) {
				return operations.RecordedSessionEntries(ctx, h)
			})
		}

		// Convert context content to proto fragments
		var protoFragments []*pb.Fragment
		if ctxResult.Context != "" {
			// Split context into individual fragments for display
			// In the actual implementation, we'll keep it as a single assembled fragment
			protoFragments = append(protoFragments, &pb.Fragment{
				Content: ctxResult.Context,
			})
		}

		// Determine execution mode
		mode := pb.ExecutionMode_INTERACTIVE
		if runPrint {
			mode = pb.ExecutionMode_ONESHOT
		}

		// The model comes from the resolved label's config; empty lets the
		// backend pick its own default.
		model := labelModel

		// Build prompt fragment
		var promptFragment *pb.Fragment
		if prompt != "" {
			promptFragment = &pb.Fragment{
				Content: prompt,
			}
		}

		// Determine work directory: CTXLOOM_ROOT override, else git root if in
		// repo, else current directory.
		workDir := projectroot.WorkDir()

		// No override and no git root: workDir is just the launch directory. The
		// project's identity — and with it its tasks, plans, and sessions under
		// ~/.ctxloom — is keyed off this path, so they don't follow the directory
		// if it moves and a launch one level up or down won't resume them. Warn,
		// never block (CLAUDE.md fault tolerance).
		if projectroot.RootFromFallback() {
			clidiag.Warn("ctxloom", "not in a git repository — using %s as the project root; its tasks, plans, and sessions live under ~/.ctxloom keyed to this path, so re-launch from here to resume them.", workDir)
		}

		// Dry run mode - show the assembled context and prompt, then stop
		// before anything stateful or interactive happens: no resume picker,
		// no session-index writes (AssignSession / MarkSessionEnded), no
		// task seeding, no plugin launch.
		if runDryRun {
			payload := dryRunJSON{
				Agent:     runAgent,
				Workspace: sessionWorkspace,
				Runtime:   agentRuntime,
				LLM:       label,
				Backend:   backendName,
				Profiles:  orEmpty(ctxResult.Profiles),
				Fragments: orEmpty(ctxResult.FragmentsLoaded),
				Context:   ctxResult.Context,
				Tokens:    tokens.Estimate(ctxResult.Context),
				Prompt:    prompt,
			}
			return emit(cmd, payload, func() error {
				if runAgent != "" {
					fmt.Println("=== Agent ===")
					fmt.Printf("%s (workspace: %s, runtime: %s)\n", runAgent, orDefault(sessionWorkspace, "none"), orDefault(agentRuntime, "host"))
				}
				fmt.Println("=== LLM ===")
				fmt.Printf("%s (%s)\n", label, backendName)
				fmt.Println("\n=== Profiles ===")
				if len(ctxResult.Profiles) > 0 {
					for _, p := range ctxResult.Profiles {
						fmt.Printf("  %s\n", p)
					}
				} else {
					fmt.Println("(no profiles)")
				}
				fmt.Println("\n=== Fragments Loaded ===")
				if len(ctxResult.FragmentsLoaded) > 0 {
					for _, f := range ctxResult.FragmentsLoaded {
						fmt.Printf("  %s\n", f)
					}
				} else {
					fmt.Println("(no fragments)")
				}
				fmt.Printf("\n=== Assembled Context (~%d tokens) ===\n", payload.Tokens)
				if ctxResult.Context != "" {
					fmt.Println(ctxResult.Context)
				} else {
					fmt.Println("(no context)")
				}
				fmt.Println("\n=== Prompt ===")
				if prompt != "" {
					fmt.Println(prompt)
				} else {
					fmt.Println("(interactive mode)")
				}
				// Show context file that would be written
				fmt.Println("\n=== Context File ===")
				fmt.Printf("Would write to: %s/[hash].md\n", filepath.Join(workDir, agent.SCMContextSubdir))
				return nil
			})
		}

		// Session resolution (Decision 11): no interactive resume picker, no
		// flag-based resume — every `ctxloom run` opens a FRESH harp. Resuming
		// prior context is the in-engine "resume" skill's job (recover_session/
		// load_session/get_previous_session), invoked from inside the session
		// that just started, not a startup-time choice.
		runEnv := map[string]string{}
		for k, v := range llmEnv {
			runEnv[k] = v
		}
		// If loading the index normalized an older on-disk format, offer to
		// persist it before the upcoming AssignSession write (which would
		// otherwise rewrite it as a side effect of creating the new session).
		if pending, commit, upErr := operations.SessionIndexUpgrade(); upErr != nil {
			clidiag.Warn("ctxloom", "session index open failed: %v", upErr)
		} else {
			confirmUpgrade(pending, commit)
		}
		var activeHarp string
		if entry, err := operations.AssignSession(workDir, backendName); err != nil {
			clidiag.Warn("ctxloom", "session naming failed: %v", err)
		} else {
			activeHarp = entry.HarpName
			runEnv["CTXLOOM_SESSION_HARP"] = entry.HarpName
			// --session --distill: distilled resume via the harp's essence
			// (distilling on demand first if missing) — see resumeDistillEnv's
			// doc for the full mechanism. Full resume (--session without
			// --distill) already folded its transcript into ctxResult.Context
			// above, so it doesn't ride this env pair for content — it still
			// sets CTXLOOM_RESUMED_FROM/PARTS="transcript" so
			// mcp_server.go's sessionInstructions surfaces the "resumed
			// from" note, but with a PARTS value resumePartsIncludeSession
			// rejects, so the SessionStart hook's essence injection stays a
			// no-op for this mode (the content already rode the context
			// path, not the essence path — no double-injection).
			switch {
			case runResumeSession != "" && runResumeDistill:
				for k, v := range resumeDistillEnv(runResumeSession, readHarpEssence, shellOutDistill) {
					runEnv[k] = v
				}
				fmt.Fprintf(os.Stderr, "ctxloom: resuming distilled essence from %s\n", runResumeSession)
			case runResumeSession != "":
				runEnv["CTXLOOM_RESUMED_FROM"] = runResumeSession
				runEnv["CTXLOOM_RESUMED_PARTS"] = "transcript"
				fmt.Fprintf(os.Stderr, "ctxloom: resuming full transcript from %s\n", runResumeSession)
			}
			// Start-session display (WS-5): a read-only summary of this session,
			// printed BEFORE the engine spawns. previous, below, is resolved via
			// the SAME primitive the get_previous_session MCP tool reads — never
			// re-derived — and is purely informational: bringing it back is the
			// resume skill's job, not something this banner offers to do.
			previous, prevErr := operations.ResolvePreviousSession(workDir, activeHarp)
			if prevErr != nil {
				clidiag.Warn("ctxloom", "previous-session lookup failed: %v", prevErr)
			}
			PrintStartSessionBanner(os.Stderr, StartSessionInfo{
				Harp:      entry.HarpName,
				Backend:   backendName,
				Label:     label,
				Profiles:  ctxResult.Profiles,
				Fragments: ctxResult.FragmentsLoaded,
				Tokens:    tokens.Estimate(ctxResult.Context),
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
				defer fmt.Fprint(os.Stderr, "\033[23;0t")
			}
		}
		// Resolve the project's stable identity (ADR 0025) and export it into the
		// session env. Fault-tolerant — any failure warns and leaves
		// CTXLOOM_PROJECT_ID unset; the task store degrades rather than blocking.
		//
		// taskStoreWorkDir redirects a linked git worktree with no .ctxloom of
		// its own to its primary checkout FIRST: the session/coordinator
		// identity workDir itself names stays worktree-distinct (unchanged,
		// see coord_host.go), but the task store an agent files findings into
		// is deliberately shared with the primary checkout so a task filed
		// from an ephemeral worktree reaches whoever is actually watching —
		// "tasks aren't context" (see internal/projectroot.TaskStoreRoot).
		if pid, warning, err := taskops.ResolveProjectIdentity(taskStoreWorkDir(workDir)); err != nil {
			clidiag.Warn("ctxloom", "project identity unresolved: %v", err)
		} else {
			runEnv["CTXLOOM_PROJECT_ID"] = pid
			if warning != "" {
				clidiag.Warn("ctxloom", "%s", warning)
			}
		}

		// Distill the just-ended session on exit (see distillSessionOnExit
		// for why this blocks). Registered BEFORE the MarkSessionEnded defer
		// below so it runs AFTER it — defers unwind LIFO, and `session
		// distill`'s time-window fallback wants ended_at stamped first.
		//
		// Gated to INTERACTIVE runs only (FINDING #2): oneshot/--print
		// (mode == ONESHOT) mints a fresh harp per invocation, so distilling
		// there would be a blocking LLM call on every headless call with no
		// idempotency guard to save it; --structured never binds a
		// session_id at all (see shouldDistillOnExit).
		interactiveExit := mode == pb.ExecutionMode_INTERACTIVE && !runStructured
		defer distillSessionOnExit(activeHarp, interactiveExit, readHarpEssence, shellOutDistill, exitDistillTimeout, os.Stderr)

		// Mark the harp ended on whatever exit path we take — clean
		// return, ctrl+c, or panic. The end timestamp lets the time-
		// window fallback in `ctxloom session distill` find this
		// session's transcript even when the bind middleware never
		// fired (session ended before any MCP method was processed).
		if activeHarp != "" {
			defer func() {
				if err := operations.MarkSessionEnded(activeHarp, time.Now()); err != nil {
					clidiag.Warn("ctxloom", "session end-mark failed: %v", err)
				}
			}()
		}

		// COORDINATOR HOSTING (agentcoord B1.6): `ctxloom run` is a
		// session-owning process, so it stands the runtime coordinator up —
		// durable delegation stores, the gRPC RunnerChannel/RunChannel — and
		// stamps the reach-back trio onto the RUNNER's spawn env (the
		// per-spawn seam below), NOT the harness env: the parent routes
		// through its own runner like every agent. The runner terminates
		// MCP on a local socket; the harness's stdio `ctxloom mcp` forwards
		// there, and every coordination tool becomes a typed plane-2 frame
		// back HERE. A standup failure is a fatal finding (fail-loud):
		// --degraded downgrades it and the harness's shim falls back to its
		// own local orchestrator.
		// Print (oneshot) runs host the coordinator too: the parent routes
		// through its own runner in every topology, so a headless
		// coordinator brief (the echo smoke) exercises the same
		// runner-terminated path as an interactive session — the bare-mcp
		// shim fallback is for externally-launched harnesses only.
		var runnerSpawnEnv map[string]string
		// sessionCoord is hoisted (D2) so the terminal UI, below, can reach
		// ConsumerService/Inject IN-PROCESS — this run IS the coordinator's
		// own hosting process, so its own terminal viewer never needs a
		// network hop (the agentbus socket it used to dial is gone).
		var sessionCoord *coord.Coordinator
		if activeHarp != "" {
			if sc, coordEnv, cerr := hostCoordinatorForSession(cfg, workDir, activeHarp, agentRuntime); cerr != nil {
				strictness.Fail(strictness.ClassApply,
					"check the coordinator listeners/state dir, or pass --degraded (env CTXLOOM_DEGRADED=1) to launch without agent delegation reach-back",
					"agent coordinator startup failed: %v", cerr)
			} else {
				sessionCoord = sc
				defer sessionCoord.Close()
				runnerSpawnEnv = coordEnv
				// The runner's local identity (session instructions, plan
				// stamping) is the session harp.
				runnerSpawnEnv["CTXLOOM_SESSION_HARP"] = activeHarp
			}
		}

		// --seed-task: move one task from the resume source store into this
		// freshly minted session's store, marked for active work. Used by
		// `ctxloom tasks run` to spin a browsed task into its own session.
		// The task already lives in the project log; seeding marks it In
		// Progress under the new session. Best-effort: a failure warns and
		// the session still launches (CLAUDE.md).
		if runSeedTask != "" && activeHarp != "" {
			seedTaskIntoSession(workDir, activeHarp, runSeedTask, runSeedStatus)
		}

		// Gate the executable surfaces (bundle MCP servers + bundle hooks + prompt
		// command-file exports) the host ships in ManagedConfig: these bypass the
		// content loader, so each is gated at its own choke via this injected
		// gate. Built once (opens the trust store + registry); fail-closed (a
		// DENY omits the executable). Surfaced below.
		execGate := operations.NewExecutableTrustGate(cfg)

		// The session's isolation axes: the SESSION-level workspace
		// (--workspace, else the project `workspace:` default) x the runtime the
		// launch source resolved (the agent's binding, or the project default).
		// The session's isolation axes (workspace × runtime). The managed path
		// prepares a policy from these below; the permission posture resolves
		// separately from config/CLI (no longer gated on the isolation boundary).
		// The external-plugin-binary path is never isolated (none).
		runAxes := isolation.Axes{
			Workspace: isolation.WorkspaceAxis(sessionWorkspace),
			Runtime:   isolation.RuntimeAxis(agentRuntime),
		}

		// Build request. The host now assembles the config/bundle setup payload
		// (slash commands, hooks, MCP, statusline) and ships it in ManagedConfig
		// so the backend plugin never self-loads ctxloom config/bundles. The
		// exports are resolved for this backend's enablement + metadata.
		//
		// AssembleManagedConfig takes BOTH the executable trust gate (so bundle
		// MCP/hooks/command exports are gated at their own choke, TR5) AND
		// ctxResult.Profiles — the SELECTED profile set (from -p, or the resolved
		// defaults) that AssembleContext scoped context to. Passing the profiles
		// here scopes the managed mcp/commands/hooks to the SAME profiles, so
		// `run -p X` no longer leaks the default profile's MCP or every pulled
		// bundle's commands into X's session.
		// Launch-time permission posture. Precedence: --permissions flag > agent
		// binding > engine-label config > built-in default (claude-code → bypass
		// while container isolation isn't relied on; others prompt). A headless
		// ONESHOT upgrades a would-block posture to bypass or it would hang with no
		// human to answer the engine.
		labelEntry, _ := cfg.GetLLMEntry(label)
		labelPerm := labelEntry.Permissions
		permMode := resolvePermissionMode(runPermissions, agentPermissions, labelPerm, backendName, mode, backends.EnforcesReadOnlyPlan(backendName))
		// Surface a posture that resolved to something other than what was asked for,
		// so the effective permissions are never silently different from intent.
		requested, hasRequest := requestedPermission(runPermissions, agentPermissions, labelPerm)
		switch {
		case hasRequest && requested == agent.PermissionPlan && permMode != agent.PermissionPlan:
			// The backend has no read-only tier, so plan collapsed (to prompt, or to
			// bypass headless) — the read-only intent is not enforced.
			clidiag.Warn("ctxloom", "%s has no read-only plan mode; this run uses %q instead", backendName, permMode)
		case hasRequest && requested != agent.PermissionBypass && permMode == agent.PermissionBypass:
			// An explicitly-requested narrower posture was widened to bypass because a
			// headless ONESHOT has no human to answer the engine's prompt.
			clidiag.Warn("ctxloom", "--print can't honor %q without a human in the loop; this run uses bypass", requested)
		case !hasRequest && permMode == agent.PermissionBypass && backendName == config.BackendClaudeCode && runVerbosity > 0:
			// The claude-code host-bypass stopgap: blanket auto-approval on the bare
			// host. It's the default path, so surface it only under -v to avoid warning
			// fatigue while still making the posture discoverable.
			clidiag.Warn("ctxloom", "permissions bypassed on the host (claude-code stopgap)")
		}

		managed := backends.AssembleManagedConfig(backendName, workDir, execGate.Gate(), ctxResult.Profiles)
		req := &pb.RunStart{
			Fragments: protoFragments,
			Prompt:    promptFragment,
			Options: &pb.RunOptions{
				WorkDir:        workDir,
				PermissionMode: permMode.String(),
				Mode:           mode,
				Env:            runEnv,
				Verbosity:      uint32(runVerbosity * 16), // Each -v adds 16 to verbosity level
				Model:          model,                     // e.g., "opus", "sonnet", "haiku"
			},
			ManagedConfig: pb.ManagedConfigToProto(managed),
		}
		// Advisory: tell the user if a bundle executable was withheld (content-free).
		execGate.WarnWithheld()

		// Create plugin client. The isolation axes (runAxes, default none/host) decide
		// WHERE the top-level run's workspace lives and HOW its plugin is spawned.
		// The external-plugin-binary path is spawned directly — isolation wraps the
		// built-in serve transport, not a user-supplied binary — and stays none.
		var client pb.Client
		if llmBinary != "" {
			// The external plugin binary is spawned DIRECTLY — isolation wraps the
			// built-in serve transport, not a user-supplied binary — so it can be
			// neither containerized nor given a worktree. An EXPLICITLY-requested
			// container is therefore a lost sandbox boundary: recordExternalPlugin-
			// IsolationDrop raises a fatal ClassIsolation finding the gate below
			// aborts on before the UNSANDBOXED binary spawns (unless --degraded).
			// This mirrors the built-in path's second isolation gate (the else
			// branch below), which the earlier startup gate can't cover because
			// isolation resolves AFTER it. The gate re-checks from postStartupMark
			// so the windows tile.
			recordExternalPluginIsolationDrop(runAxes, llmBinary)
			if ferr := failOnFindings(os.Stderr, postStartupMark); ferr != nil {
				return ferr
			}
			// Use external plugin binary
			client, err = pb.NewLLMRunner(llmBinary, llmArgs, runVerbosity)
			if err != nil {
				return fmt.Errorf("failed to start plugin: %w", err)
			}
		} else {
			// Prepare the workspace along the per-axis degrade chain. Fault
			// tolerance: a container requested but unlaunchable (no runtime, or
			// the agent image absent) drops ONLY the runtime axis — a requested
			// worktree survives — and a worktree failure degrades to the live
			// project dir, so `runtime: container` is a safe default.
			//
			// Fail-loudly re-gate: isolation resolves HERE, AFTER the startup gate
			// (failOnFindings above), so a requested-container-degraded-to-host
			// finding (ClassIsolation, raised inside Prepare) would slip past that
			// already-passed gate. Gate 2 below re-checks from postStartupMark —
			// captured immediately after gate 1 passed, so the two windows TILE
			// (a finding recorded anywhere between the gates is caught, not just
			// one raised inside Prepare) — and an EXPLICITLY-requested container
			// that can't be satisfied aborts (exit 3) before an UNSANDBOXED
			// engine is spawned — unless --degraded, which records nothing and
			// proceeds on the host per the degrade chain.
			// The session identity (harp + project id) rides the same runEnv the
			// engine gets, so the isolation state mounts and the in-container
			// writers key off one source.
			prepared, ws := isolation.Prepare(ctx, runAxes, backendName, operations.IsolationImageConfig(cfg, backendName), workDir, activeHarp, isolation.SessionStateFromEnv(runEnv))
			// The permission posture is resolved once from config/CLI/agent and is
			// authoritative regardless of how the isolation boundary degrades: a
			// container that failed to launch does NOT drop a configured bypass —
			// that is the point of the host stopgap.
			policy := prepared
			// Per-agent config-home envs (worktree) isolate each engine's GLOBAL
			// config layer (CLAUDE_CONFIG_DIR / CODEX_HOME / KIRO_HOME / ...) from
			// this run; nil for none/container. Mirrors the fan-out member path
			// (operations/oneshot.go's workspaceEnv/env assembly): merged UNDER the
			// already-assembled req.Options.Env (session identity + user --env), so
			// an explicit user/session var still wins over a resolved isolation var
			// — this must never clobber a caller-set env, only fill gaps.
			req.Options.Env = mergeWorkspaceEnv(req.Options.Env, isolation.WorkspaceEnv(ws))
			// Tear the workspace down after the client is killed (kill the plugin/
			// container before removing its scratch — WIP-safe). Registered before
			// client.Kill so it runs after, and before the gate below so an abort on
			// a container→worktree degrade still tears the prepared worktree down.
			// none's cleanup is a noop. The error is deliberately dropped: a
			// cleanup failure surfaces from INSIDE Cleanup (a streamed warning
			// naming the residue path + fix — see warnCleanupResidue), and this
			// runs post-gate where no choke owner could act on an error anyway.
			defer func() { _ = ws.Cleanup() }()
			if ferr := failOnFindings(os.Stderr, postStartupMark); ferr != nil {
				return ferr
			}
			// A container-requested run whose boundary silently degraded to the bare
			// host still carries a configured bypass. For the claude-code host stopgap
			// that is intended; for any other backend the boundary that justified
			// bypass is gone, so surface it rather than run full-auto with no signal.
			// A SATISFIED container request (the container OR container-worktree
			// policy prepared) never warns. In strict mode a lost boundary recorded
			// a ClassIsolation finding and gate 2 above already aborted, so this
			// warning fires only in degraded mode (or if a degrade recorded nothing).
			if warnBypassOnLostContainer(runAxes, prepared.Name(), permMode, backendName) {
				clidiag.Warn("ctxloom", "container isolation unavailable; running %s with bypass on the host", backendName)
			}
			// The engine's cwd lands in the prepared workspace (identical-path for
			// container/none; a worktree in Phase 2).
			req.Options.WorkDir = ws.Dir()
			// Stamp the resolved isolation cell so the plugin knows which cell it
			// runs in (it can't infer it from WorkDir alone). The external-plugin
			// path above never reaches here, so it leaves cell_kind unset →
			// UNSPECIFIED → Shared, which is correct: that path stays none.
			// Setup's setupViaCells (launch_backend.go) consumes it to pick the
			// delivery cell.
			req.Options.CellKind = pb.CellKindToProto(operations.CellKindForPolicy(policy))
			// dire-five: for a container policy ONLY, stamp the in-container
			// ctxloom binary path so the MCP-surface writer (running inside the
			// container, per agentcoord B1.6's runner-terminated MCP) emits a
			// `command` the container can actually exec, instead of the host
			// self-exec path (which does not exist inside the container — the
			// engine's `ctxloom mcp` stdio shim then never launches and the child
			// has zero MCP tools). "" for none/worktree: the host self-exec-
			// absolute invariant (agent.CtxloomCommand's doc) is untouched.
			if override := operations.MCPCommandOverrideForPolicy(policy); override != "" {
				if req.Options.Env == nil {
					req.Options.Env = make(map[string]string, 1)
				}
				req.Options.Env[agent.MCPCommandOverrideEnv] = override
			}
			// Spawn through the policy, carrying the resolved label so serve
			// configures exactly this entry (not the first map-ordered entry of the
			// same type).
			client, err = policy.SpawnClient(backendName, label, runVerbosity, ws, runnerSpawnEnv)
			if err != nil {
				return fmt.Errorf("failed to start plugin: %w", err)
			}
		}
		defer client.Kill()

		// --structured: drive the session as a structured turn REPL (the gRPC
		// WatchSession + user_message interface) instead of owning the terminal.
		// The Chat RPC never runs Setup, so the managed MCP servers Setup would
		// write to the engine's settings file ride the session instead.
		if runStructured {
			return runStructuredREPL(ctx, client, req, managed.ChatMCPServers(backendName), outputFormatOf(cmd), os.Stdin, os.Stdout)
		}

		// For an interactive run the frontend owns the terminal: raw mode + stdin
		// + resize are pumped over the VIRTUALIZED-PROCESS-IO (vpio) seam —
		// internal/vpio — to the controller's pty. Oneshot runs need none of
		// that. Everything in this block stays above the seam: it references
		// only pb.WindowSize (the wire's resize payload shape, not a transport
		// call) and vpio types from here down, never a transport client method
		// directly.
		var stdin io.Reader
		var stdout io.Writer = os.Stdout
		var resize <-chan *pb.WindowSize
		restoreTerm := func() {}
		// S6 oneshot capture: a ONESHOT `--print` run drives Backend.Execute,
		// which returns prose on stdout with no ChatEvent stream — the
		// structured tee (GRPCClient.Chat, internal/lm/grpc/chat.go) never
		// fires for it, so this is the runner's own seam onto both halves of
		// a two-entry canonical transcript (transcript.RecordOneshot): the
		// prompt is already known (the `prompt` var above), and this
		// captures the returned half by teeing the SAME bytes already bound
		// for the terminal into a buffer, alongside (never instead of) the
		// user-visible stdout. Never allocated for INTERACTIVE (the pty
		// path, out of scope — petty-green) or when mode==ONESHOT via
		// --structured (returns earlier, at runStructuredREPL above).
		var oneshotCapture *bytes.Buffer
		if mode == pb.ExecutionMode_ONESHOT {
			oneshotCapture = &bytes.Buffer{}
			stdout = io.MultiWriter(stdout, oneshotCapture)
		}
		if mode == pb.ExecutionMode_INTERACTIVE {
			stdin, resize, restoreTerm = interactiveTerminal(ctx)
			// Wrap the terminal seams with the observation layer (prefix-key
			// viewer + surround bar) — real tty only, never a pipe, and
			// --plain-terminal opts a session out entirely. Its Close composes
			// onto the raw-mode restore so every exit path (clean, error,
			// signal-cancelled ctx) unwinds scroll region, held output, and
			// raw mode together.
			if stdin != nil && !runPlainTerminal {
				// The TUI is about to own this terminal, so clidiag warnings
				// must stop writing to it (large-album). Diverted to the
				// session's diagnostics log, announced before the handover.
				restoreDiag := redirectDiagnosticsForTUI(activeHarp, os.Stderr)
				if ui := setupTerminalUI(ctx, cfg, sessionCoord, terminalUIIdentity{
					WorkDir: workDir,
					Harp:    activeHarp,
					Agent:   boundAgent,
					Backend: backendName,
					Model:   labelModel,
				}, stdin, resize); ui != nil {
					stdin, stdout, resize = ui.Stdin(), ui.Stdout(), ui.Resize()
					rawRestore := restoreTerm
					restoreTerm = func() { ui.Close(); restoreDiag(); rawRestore() }
				} else {
					// No TUI engaged after all — stderr is still the user's,
					// so put the warnings back on it.
					restoreDiag()
				}
			}
			// Deferred via closure (the value above may be the composed one) so
			// a panic inside the session can't strand the shell in raw mode.
			// restoreTerm is idempotent; the inline call below still restores
			// before any normal-path output.
			defer func() { restoreTerm() }()
		}

		// Run the AI plugin over the vpio seam. goplugin.Launcher is the seam's
		// SWAP POINT: it wraps the existing go-plugin Run stream (client.Run,
		// unchanged) below the seam; a future docker-exec or host-pty
		// vpio.Launcher plugs in here without this call site changing.
		session, err := goplugin.NewLauncher(client, req).Start(ctx, vpio.ProcessSpec{
			Stdin:  stdin,
			Stdout: stdout,
			Stderr: os.Stderr,
		})
		if err != nil {
			return fmt.Errorf("failed to start plugin: %w", err)
		}
		pumpResize(session, resize)
		status, err := session.Wait()
		restoreTerm()
		if err != nil {
			return fmt.Errorf("AI plugin failed: %w", err)
		}

		// Best-effort: capture failure warns but must never fail an
		// otherwise-successful (or otherwise-failed — the exit code below
		// is unaffected either way) run. Captured even on a nonzero exit:
		// partial prose on stdout is still real memory of what happened.
		if oneshotCapture != nil {
			if terr := transcript.RecordOneshot(activeHarp, backendName, prompt, oneshotCapture.String()); terr != nil {
				clidiag.Warn("ctxloom", "oneshot transcript capture: %v", terr)
			}
		}

		// Interactive-pty exit seam for vendor-transcript import
		// (docs/transcript-schema.md §8's "interactive-pty gap", petty-green):
		// the structured tee (transcript.Tee/TeeAndClose) never reaches a pty,
		// so this is the ONLY place ctxloom can turn the just-exited engine's
		// OWN transcript into canonical memory. Mirrors oneshotCapture's own
		// "capture even on a nonzero exit" note just above — a session that
		// errored out mid-way still has real prior turns worth keeping.
		// Best-effort exactly like RecordOneshot: a lookup/convert failure
		// warns, never fails the run.
		if mode == pb.ExecutionMode_INTERACTIVE {
			convertVendorTranscriptOnExit(activeHarp)
		}

		if status.Code != 0 {
			return &ExitError{Code: int(status.Code)}
		}

		return nil
	},
}

// convertVendorTranscriptOnExit runs the vendor-transcript importer
// (operations.ConvertVendorTranscript) for an interactive-pty session that
// just exited. Extracted to its own small, directly-unit-testable function
// (no goplugin/pty involved) rather than inlined at the call site above,
// mirroring how transcript.RecordOneshot itself is a standalone function the
// oneshot branch just calls. A blank harp (no session identity — e.g.
// AssignSession failed earlier and the run proceeded unharped) or an
// unindexed harp are silent no-ops; any other lookup/convert failure is
// warned, never returned, so a transcript-import hiccup can never fail an
// otherwise-successful interactive run.
func convertVendorTranscriptOnExit(harp string) {
	if harp == "" {
		return
	}
	entry, err := operations.GetSession(harp)
	if err != nil {
		clidiag.Warn("ctxloom", "vendor transcript import: look up %s: %v", harp, err)
		return
	}
	if entry == nil {
		return
	}
	// A FRESH background context, deliberately NOT the run's own ctx: that
	// one is signal.NotifyContext-derived (this file's RunE, `ctx, stopSignals
	// := signal.NotifyContext(...)`), so it is already Done() by the time
	// this runs whenever the interactive session ended via the same Ctrl-C
	// that stops most interactive TUIs — arguably the MOST common clean-exit
	// path. Reusing it would make Convert abort immediately
	// (importer.VendorAdapter implementations check ctx.Err() up front) on
	// exactly the sessions this hook most needs to capture.
	if _, cerr := operations.ConvertVendorTranscript(context.Background(), *entry); cerr != nil {
		clidiag.Warn("ctxloom", "vendor transcript import: %v", cerr)
	}
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

// externalPluginIsolationFixIt is the fix-it for a container requested on the
// external-plugin-binary path, which cannot be sandboxed at all.
const externalPluginIsolationFixIt = "this backend runs an external plugin binary that ctxloom cannot sandbox — use a built-in backend for container isolation, drop the container runtime request, or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox"

// recordExternalPluginIsolationDrop records the isolation faults for the
// external-plugin-binary path (llmBinary != ""), which is spawned directly and
// so can be neither containerized nor given a worktree. An EXPLICITLY-requested
// container is a lost sandbox boundary — a fatal ClassIsolation finding the
// caller's gate aborts on unless --degraded downgrades it to the host run. A
// requested worktree only warns: it degrades benignly to the live project dir,
// the same non-fatal outcome as the built-in worktree→none degrade (only a lost
// CONTAINER boundary is fatal). The warning streams in both modes; the finding
// records in strict mode only (strictness.Fail is a no-op under --degraded).
func recordExternalPluginIsolationDrop(axes isolation.Axes, binary string) {
	if axes.WantsContainer() {
		strictness.Fail(strictness.ClassIsolation, externalPluginIsolationFixIt,
			"runtime: container requested but the external plugin binary %q cannot be containerized — running on the HOST without a container boundary (this session is NOT sandboxed)", binary)
	}
	if axes.WantsWorktree() {
		clidiag.Warn("ctxloom", "workspace: worktree requested but the external plugin binary %q runs in the live project dir", binary)
	}
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
// the engine label's configured permissions, then the built-in default. The
// built-in default is bypass for claude-code (the host stopgap while container
// isolation isn't relied on) and default (prompt) for every other backend.
// Config/CLI is authoritative: the isolation boundary no longer earns or drops
// bypass. A non-interactive ONESHOT has no human to answer the engine, so a
// would-block posture (default/acceptEdits) upgrades to bypass or it hangs.
func resolvePermissionMode(flag, agentPerm, labelPerm, backendType string, mode pb.ExecutionMode, backendEnforcesPlan bool) agent.PermissionMode {
	m := agent.ResolveDefault([]string{flag, agentPerm, labelPerm}, backendType == config.BackendClaudeCode)
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
// first parseable of flag > agent > label — independent of any backend collapse.
// ok is false when nothing parseable was requested (so the caller falls back to a
// built-in default). It is the input to the "backend can't honor this" warning.
func requestedPermission(flag, agentPerm, labelPerm string) (agent.PermissionMode, bool) {
	for _, s := range []string{flag, agentPerm, labelPerm} {
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
		if name == "mock" {
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

	runCmd.Flags().StringVarP(&runLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast, antigravity); overrides the configured default")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "command", "r", "", "Run a saved command by name")
	// Deprecated F9 alias: --run-prompt is the pre-rename "prompt" vocabulary
	// (resources/commands/ and the command item-kind have always said
	// "command"). Kept working, not just aliased in help — see the RunE
	// reconciliation above.
	runCmd.Flags().StringVar(&runSavedPromptDeprecated, "run-prompt", "", "Deprecated: use --command instead")
	_ = runCmd.Flags().MarkDeprecated("run-prompt", "use --command instead")
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
	runCmd.Flags().BoolVar(&runPrint, "print", false, "Print response and exit (non-interactive mode)")
	runCmd.Flags().BoolVar(&runStructured, "structured", false, "Structured turn REPL: type messages and see native turns (composes the gRPC WatchSession + user_message interface). One line = one message; \\n, \\t and quotes are decoded within a line.")
	runCmd.Flags().BoolVar(&runPlainTerminal, "plain-terminal", false, "Disable ctxloom's terminal layer (the prefix-key agent viewer and the surround status bar) for this session")
	runCmd.MarkFlagsMutuallyExclusive("structured", "print")
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
	_ = runCmd.RegisterFlagCompletionFunc("run-prompt", completePromptNames)
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

// stdinReader is the single buffered reader over os.Stdin shared by every
// interactive y/N prompt. A fresh bufio.Reader per prompt would silently discard
// any bytes a previous reader buffered past its line (type-ahead / paste between
// back-to-back confirmations), so all prompts read through this one reader.
var stdinReader = bufio.NewReader(os.Stdin)

// promptLine writes prompt to stderr and reads one trimmed line from the shared
// stdin reader, returning the read error (e.g. EOF) so callers can apply their
// own fallback. It is the single read primitive every interactive prompt funnels
// through (promptYesNo and the TR4 trust menus) so a line buffered past one
// prompt is not discarded before the next (ctxloom-code-08-002).
func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptYesNo writes prompt to stderr and reads one line from the shared stdin
// reader, reporting whether the answer was affirmative ("y"/"yes",
// case-insensitive). The read error (e.g. EOF) is returned so each caller can
// apply its own fallback; anything that is not an explicit yes is a no.
func promptYesNo(prompt string) (bool, error) {
	line, err := promptLine(prompt)
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(line)
	return answer == "y" || answer == "yes", nil
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

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
