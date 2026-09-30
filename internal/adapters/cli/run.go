package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/mcp"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
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
	// it is the turn count that decides whether an engine gets a session. It
	// is engine.Structured on the Source, and the wire's ONESHOT mode.
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
	// classic profile flow). Workspace/Runtime are the isolation axes as
	// DECLARED — the invocation's --workspace, the binding's runtime — each
	// unset where nothing asked for it; Resolved is the pair the run would
	// land on. Kept apart so a default is never reported as a guarantee
	// somebody asked for.
	Agent     string   `json:"agent,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Runtime   string   `json:"runtime,omitempty"`
	Resolved  axesJSON `json:"resolved"`
	// Environment is what the preview PROBED for Resolved: the runtime the
	// run would get and how its runner reaches home.
	Environment *environmentJSON `json:"environment,omitempty"`
	LLM         string           `json:"llm"`
	Backend     string           `json:"backend"`
	Profiles    []string         `json:"profiles"`
	Fragments   []string         `json:"fragments"`
	Context     string           `json:"context"`
	// ResumedEssence is what a --session --distill launch delivers through
	// its SessionStart hook rather than through Context: the harp's
	// distilled essence (distilledResumePreview). ResumedEssenceNote says
	// when the launch would distill first, so what is shown is not final.
	ResumedEssence     string `json:"resumed_essence,omitempty"`
	ResumedEssenceNote string `json:"resumed_essence_note,omitempty"`
	// Delivery is the plan's static routes: each surface's root, the
	// project-root and work-dir routes marked unsafe.
	Delivery []routeJSON `json:"delivery"`
	// EngineHome is the home the engine runs against: "session", or the
	// binding's unsafe "host" selection.
	EngineHome engineHomeJSON `json:"engine_home"`
	// Tokens is the estimated token count of the assembled Context, computed by
	// the backend (internal/tokens) so a client previewing a profile reads one
	// authoritative estimate instead of re-deriving its own chars/token guess.
	Tokens int    `json:"tokens"`
	Prompt string `json:"prompt,omitempty"`
	// Findings is every finding the preview collected, in record order; the
	// fatal ones are what the startup gate refuses on after the plan.
	Findings []findingJSON `json:"findings"`
}

// findingJSON is one collected finding on the wire. Fatal is whether this
// mode's gate refuses on it (strictness.Mode.Fatal); the rest were warnings.
type findingJSON struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Remedy string `json:"remedy,omitempty" label:"fix"`
	Fatal  bool   `json:"fatal"`
}

// findingsOf is found on the wire under mode.
func findingsOf(mode strictness.Mode, found report.Findings) []findingJSON {
	out := make([]findingJSON, 0, len(found))
	for _, f := range found {
		out = append(out, findingJSON{Kind: string(f.Kind), Text: f.Text, Remedy: f.Remedy, Fatal: mode.Fatal(f)})
	}
	return out
}

// axesJSON is one isolation-axis pair on the wire.
type axesJSON struct {
	Workspace string `json:"workspace"`
	Runtime   string `json:"runtime"`
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
// textblocks.Join — see
// coord/spawner.go's ResumeHistory). entriesFn is the IoC seam (production:
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
	return textblocks.Join(existing, operations.RenderResumedTranscript(harp, entries))
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
		"CTXLOOM_RESUMED_PARTS": resumedPartsSession,
	}
}

// resumedPartsSession is the CTXLOOM_RESUMED_PARTS value a distilled resume
// sets, and the one resumePartsIncludeSession opens the essence gate on.
const resumedPartsSession = "session"

// distilledResumePreview is what a --distill --dry-run shows of the resume:
// the essence the launch's SessionStart hook would inject
// (resumedEssenceForInjection, over the env resumeDistillEnv sets), plus a
// caveat when the launch would replace it first.
//
// It is READ-ONLY, and that is the difference from a real launch: a launch
// distills a missing or stale essence on demand (resumeDistillEnv) before the
// engine starts, which writes the essence and the session index. A preview
// writes nothing, so it names what the launch would do instead of doing it.
func distilledResumePreview(harp string, staleFn func(string) bool) (essence, note string) {
	essence = resumedEssenceForInjection(1, "startup", harp, resumedPartsSession)
	switch {
	case essence == "":
		note = fmt.Sprintf("%s is not distilled yet; the launch distills it on demand before the session starts", harp)
	case staleFn(harp):
		note = fmt.Sprintf("%s's essence is stale; the launch re-distills it before the session starts, so the session will see a newer one than this", harp)
	}
	return essence, note
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
		ProjectID:   os.Getenv(sessions.EnvProjectID),
		SessionHarp: activeHarp,
	}, harpID, status, "")
	if err != nil {
		strictness.Fail(report.KindTask,
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

Exit status: when the engine ran and exited, ctxloom run exits with the
engine's own status — its exit code, or 128+signum when a signal ended it
(143 for SIGTERM), as a shell would report. A run that failed without an
engine status (cancelled, or the engine never launched) exits 1. ctxloom's
own refusals (2) and fatal startup findings (3) happen before the engine
launches, so a 2 or 3 after the engine ran is the engine's.

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

	// The fail-loudly gates. Windows TILE by construction: phaseGates.close
	// re-opens as it closes, so a finding recorded anywhere between two gates
	// still aborts at the next one instead of falling into an ungated hole.
	gates *phaseGates

	cfg       *config.Config // loadConfig
	prompt    string         // resolvePrompt
	workDir   string         // the project root the launch is asked for
	projectID string         // the project identity the session serves

	// launch is THE resolved launch (launch.Resolve, through
	// operations.StartRun): the engine, the label, the floored permission,
	// the axes, the cell, the package, the plan, the endpoint. Every field
	// below it is a projection the transport and drive arms read; nothing
	// is re-derived from flags or config once it exists.
	launch launch.Launch
	// rebind re-mints launch's endpoint against the deps it was resolved
	// with (coord.OwnerRun.Rebind).
	rebind func(context.Context, launch.Launch) (launch.Launch, error)
	// opened is the launch's package as this process reads it (the local
	// launcher's half of the carrier codec) and the managed payload the
	// plugin arm hands its writers.
	opened      operations.Opened
	label       string
	backendName string
	labelModel  string
	permMode    agent.PermissionMode
	managed     *agent.ManagedConfig
	activeHarp  string
	// env is the cell's prepared environment: where and how the runner is
	// started, whatever the runtime.
	env isolation.Environment

	// hostCoordinator: the coordinator this run hosts for delegated agents
	// and the reach-back env its runner is spawned with.
	ownerToken string
	// sessionCoord is held on the state so the terminal UI can reach
	// ConsumerService/Inject IN-PROCESS — this run IS the coordinator's own
	// hosting process, so its own terminal viewer never needs a network hop.
	sessionCoord *coord.Coordinator

	// startTransport. ownedRun is the owner-owned run the coordinator this
	// process hosts drives its runner through (StartOwnedRun) — every
	// launch. pty is the runner's pseudo-terminal for an INTERACTIVE launch
	// (this process keeps the master; the terminal layer wraps it; a
	// container's is removed by name with it); runnerHandle is a --one-shot
	// launch's plain runner process.
	ownedRun     *ownedRunSession
	pty          runnerTTY
	runnerHandle *isolation.RunnerHandle
}

func runRun(cmd *cobra.Command, args []string) error {
	st := &runState{
		cmd:  cmd,
		args: args,
		// Fail-loudly gate: checkpoint before any startup choke fires, so
		// every fatal finding collected across config load, sync, and
		// assembly is caught at one place (gateStartup below) and the launch
		// aborts with the full list. Degraded mode still RECORDS, but the gate
		// acts only on a NonDegradable finding there.
		gates: newPhaseGates(os.Stderr, App().Strictness),
	}

	if err := st.validateFlags(); err != nil {
		return err
	}
	if err := st.loadConfig(); err != nil {
		return err
	}
	// An invalid ui.prefix_key is a broken-config finding like any other:
	// recorded with the config load so the startup gate aborts on it before
	// launch (a viewer on a key the user didn't configure is a wrong-context
	// session's cousin).
	validateTerminalUIConfig(st.cfg)
	if runLLM != "" {
		if _, err := validateExplicitLLM(st.cfg, runLLM); err != nil {
			return err
		}
	}
	if err := st.resolvePrompt(); err != nil {
		return err
	}

	stopSignals := st.withShutdownSignals()
	defer stopSignals()

	st.runStartupTasks()
	st.resolveProject()

	// Dry run mode - show the resolved launch and the prompt, then stop
	// before anything stateful or interactive happens: the same resolver,
	// over stateless ports (no session written, no cell prepared, no
	// coordinator, no task seeding, no plugin launch).
	if runDryRun {
		return st.emitDryRun()
	}

	// From here down every phase registers its unwind on runRun's OWN frame,
	// via a cleanup the phase returns or a method that guards itself, rather
	// than deferring inside the phase — a defer inside an extracted method
	// fires when that method returns, which for teardown is far too early.
	// The registration ORDER below is the unwind order (LIFO) and is
	// load-bearing; each defer's own doc says why it sits where it does.
	if err := st.resolveLaunch(); err != nil {
		return st.refused(err)
	}
	// Mark the harp ended on whatever exit path we take — clean return,
	// ctrl+c, or panic. The end timestamp lets the time-window fallback in
	// `ctxloom session distill` find this session's transcript even when the
	// bind middleware never fired.
	defer st.markSessionEnded()

	// The startup gate: config load, sync, assembly and the cell all ran
	// inside the resolver; every fatal-class finding they recorded aborts
	// here, before the engine launches, listing every finding with its fix.
	if err := st.gateStartup(); err != nil {
		return err
	}
	st.warnPosture()

	restoreTitle := st.openSessionBanner()
	defer restoreTitle()

	closeCoordinator := st.hostCoordinator()
	defer closeCoordinator()

	st.seedTask()

	// Teardown: end the runner (the pty session or the plain process — a
	// container removed by name), then release the cell. Registered HERE,
	// before startTransport can return early: a defer only protects returns
	// that happen AFTER it is reached, and the starter records the runner's
	// handle the moment it exists, so every early return in between is
	// covered instead of only the successful path.
	defer st.teardownAll()

	if err := st.startTransport(); err != nil {
		return err
	}
	return st.drive()
}

// resolveProject resolves the project root the launch is asked for and the
// project's stable identity the session serves. The identity is
// fault-tolerant: a failure warns and the session carries none; the task
// store degrades rather than blocking.
//
// taskStoreWorkDir redirects a linked git worktree with no .ctxloom of its
// own to its primary checkout FIRST: the session identity workDir itself
// names stays worktree-distinct, but the task store an agent files findings
// into is deliberately shared with the primary checkout so a task filed from
// an ephemeral worktree reaches whoever is actually watching.
func (st *runState) resolveProject() {
	st.workDir = projectroot.WorkDir()
	// No override and no git root: workDir is just the launch directory. The
	// project's identity — and with it its tasks, plans, and sessions under
	// ~/.ctxloom — is keyed off this path, so they don't follow the directory
	// if it moves and a launch one level up or down won't resume them. Warn,
	// never block.
	if projectroot.RootFromFallback() {
		clidiag.Warn("ctxloom", "not in a git repository — using %s as the project root; its tasks, plans, and sessions live under ~/.ctxloom keyed to this path, so re-launch from here to resume them.", st.workDir)
	}
	pid, warning, err := taskops.ResolveProjectIdentity(taskStoreWorkDir(st.workDir))
	if err != nil {
		clidiag.Warn("ctxloom", "project identity unresolved: %v", err)
		return
	}
	st.projectID = pid
	if warning != "" {
		clidiag.Warn("ctxloom", "%s", warning)
	}
}

// source is the launch as this invocation asks for it: the flags, and
// nothing decided. --agent names a binding (an unknown name is a HARD
// error: an explicit name is user intent); a bare launch binds the default
// agent; -p/-f/-t is the explicit assembly. --llm is the label override,
// --one-shot the mode, --workspace the session's workspace axis,
// --permissions the flag the floor reads first. The engine passthrough is
// the label's request-borne env plus the resume pair.
func (st *runState) source() (launch.Source, error) {
	workspace, err := launch.ParseWorkspaceAxis(runWorkspace)
	if err != nil {
		return launch.Source{}, err
	}
	perm := agent.PermissionNotRequested
	if runPermissions != "" {
		perm, _ = agent.ParsePermissionMode(runPermissions)
	}
	src := launch.Source{
		Agent:      runAgent,
		Profiles:   nil,
		Fragments:  runFragments,
		Tags:       runTags,
		Label:      runLLM,
		Prompt:     st.prompt,
		WorkDir:    st.workDir,
		Workspace:  workspace,
		Permission: perm,
		Degraded:   App().Strictness.Degraded,
		Env:        map[string]string{},
	}
	if runProfile != "" {
		src.Profiles = []string{runProfile}
	}
	if runOneShot {
		src.Mode = engine.Structured
	}
	for k, v := range st.resumeEnv() {
		src.Env[k] = v
	}
	src.Extra = st.resumedTranscript()
	return src, nil
}

// resolveLaunch is the trunk: mint the session and resolve the launch
// through the one resolver, then bind its projections for the transport
// and drive arms.
func (st *runState) resolveLaunch() error {
	src, err := st.source()
	if err != nil {
		return err
	}
	deps, err := App().LaunchDeps(st.ctx)
	if err != nil {
		return err
	}
	l, err := operations.StartRun(st.ctx, deps, sessions.Seed{ProjectDir: st.workDir, ProjectID: st.projectID}, src)
	if err != nil {
		return err
	}
	// The startup findings are composed HERE, after the cell was prepared:
	// a degraded-to-host finding is the case they exist for.
	l, err = launch.WithLead(st.ctx, deps.ForSession(l.Identity.Harp), l, st.startupFindings()...)
	if err != nil {
		return err
	}
	opened, err := operations.OpenLaunch(st.ctx, deps.ForSession(l.Identity.Harp), l)
	if err != nil {
		return err
	}
	st.bindLaunch(l, opened)
	st.rebind = endpointRebinder(deps)
	return nil
}

// bindLaunch projects the resolved launch onto the fields the transport and
// drive arms read. Decided once, here; nothing downstream re-derives.
func (st *runState) bindLaunch(l launch.Launch, opened operations.Opened) {
	st.launch = l
	st.opened = opened
	st.activeHarp = l.Identity.Harp
	st.label = l.Label.Label
	st.backendName = string(l.Engine)
	st.labelModel = l.Label.Model
	st.permMode = l.Permission.Mode
	st.managed = opened.Managed
	if env, ok := operations.EnvironmentOf(l.Cell); ok {
		st.env = env
	}
}

// boundAgent names the binding this run launched under (--agent, or the
// default agent for a bare launch; none for an explicit -p/-f/-t assembly) —
// the surround bar's identity, read off the Source this invocation built.
func (st *runState) boundAgent() string {
	switch {
	case runAgent != "":
		return runAgent
	case runProfile == "" && len(runFragments) == 0 && len(runTags) == 0:
		return st.cfg.GetDefaultAgent()
	}
	return ""
}

// resumedTranscript is the --session (full resume — no --distill) lead: the
// resumed harp's full recorded transcript trails the assembled context as
// its own block. --distill takes the essence path instead (resumeEnv).
func (st *runState) resumedTranscript() []composite.Fragment {
	if runResumeSession == "" || runResumeDistill {
		return nil
	}
	rendered := resumeFullContext("", runResumeSession, func(h string) ([]agent.SessionEntry, error) {
		return operations.RecordedSessionEntries(st.ctx, App().Engines(), h)
	})
	if rendered == "" {
		return nil
	}
	return []composite.Fragment{{Name: "resumed-transcript", Body: rendered}}
}

// warnPosture says out loud when the posture the run launches with is not
// what the flag asked for, when a one-shot has nobody to approve a gated
// call, and (under -v) when the engine's declared host default is
// what decided it.
func (st *runState) warnPosture() {
	if runPermissions != "" {
		if requested, ok := agent.ParsePermissionMode(runPermissions); ok && requested != st.permMode {
			clidiag.Warn("ctxloom", "--permissions %q cannot be honoured as asked on %s; this run uses %q", requested, st.backendName, st.permMode)
		}
	}
	if st.launch.Mode == engine.Structured && st.permMode != agent.PermissionBypass {
		clidiag.Warn("ctxloom", "--one-shot with %s permissions has no human to approve a gated call; the engine denies every call the posture would ask about, so those steps will not run", st.permMode)
	}
	pf := operations.EnginePermissionFacts(App().Engines(), st.backendName)
	if runPermissions == "" && runVerbosity > 0 && pf.HostDefaultReason != "" && st.permMode == pf.HostDefault {
		clidiag.Warn("ctxloom", "%s", pf.HostDefaultReason)
	}
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
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	st.cfg = cfg
	// The reader records schema-invalid keys and refused overrides as
	// warnings — surface them so a degraded config.yaml never silently
	// launches an empty-context session.
	config.ReportWarnings(strictness.Sink("ctxloom"), cfg.GetWarnings())
	// If loading upgraded an older config schema in memory, offer to persist
	// it (interactive + consented only; never a silent rewrite).
	confirmConfigUpgrade(cfg.GetPendingUpgrade(), cfg.CommitUpgrade)
	// The HOME layer gets the same offer when a project config also exists.
	// Without this, a stale ~/.ctxloom/config.yaml was upgraded
	// in memory on every load forever and never converged. The prompt names
	// the path, so consenting to rewrite HOME is an informed choice rather
	// than a surprise side effect of a project-scoped run.
	confirmConfigUpgrade(cfg.GetHomePendingUpgrade(), cfg.CommitHomeUpgrade)
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
	// Auto-sync remote dependencies on startup if enabled (graceful failure),
	// so the run doesn't hard-fail on missing parent profiles or bundles that
	// sync would have fetched. In a TTY, confirm with the user before
	// installing anything new. The sync ANNOUNCES itself: it is the one
	// startup task with a network side, and a dry run's suppression of it is
	// observable only because a real start says so.
	syncCfg := st.cfg.GetSyncConfig()
	if syncCfg.ShouldAutoSync() && !runDryRun && confirmSyncInstall(st.ctx, st.cfg) {
		fmt.Fprintf(os.Stderr, "ctxloom: syncing remote bundles and profiles from config...\n")
		syncCtx, syncCancel := context.WithTimeout(st.ctx, 60*time.Second)
		result, syncErr := operations.SyncOnStartup(syncCtx, App())
		syncCancel()
		if syncErr != nil {
			if !errors.Is(syncErr, context.Canceled) {
				strictness.Fail(report.KindSync, "check the remote/network, or pass --degraded to launch anyway", "sync failed: %v", syncErr)
			}
		} else {
			operations.WriteAndRecordSyncSummary(os.Stderr, result)
		}
	}

	// Log which companion binaries (taskloom, ltk) this session is wired
	// with, version-probed via `<bin> version --format json`.
	if !runDryRun {
		operations.ReportCompanions(os.Stderr, App().Prober(), st.cfg.Trust().Root())
	}

	// Startup reaper: sweep any per-agent worktree checkout left behind by a
	// crashed/killed prior run — teardown()'s
	// WIP-safe removal only ever fires on a graceful Cleanup(), so nothing
	// else ever reaps these. Best-effort, silent unless it found something.
	if !runDryRun {
		operations.SweepOrphanedWorktrees(st.ctx, os.Stderr)
	}
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
	// close() opens the workspace window as it closes the startup one, so the
	// two abut with no ungated instant between them.
	return st.gates.close(PhaseStartup)
}

// refused is the resolver's failure path. The cell is prepared INSIDE the
// resolver, and the cell gate refuses by RECORDING a finding (a requested
// container that could not be provided, a controlled home that cannot
// authenticate) and returning launch.ErrRuntimeUnavailable over it — which
// surfaces before the startup gate ever closes, as exit 1 with no class, no
// fix and no "--degraded does NOT bypass" header. That refusal is the
// finding's, so the gate reports it: the fatal-findings abort.
//
// An EMPTY assembly (launch.ErrContextEmpty) is the same shape when a fatal
// finding was recorded on the way: a profile whose bundle did not load, or
// whose parent did not resolve, assembles to nothing BECAUSE of that finding,
// and the finding names the fix where "assembled to nothing" only names the
// symptom. Nothing else pads a profile set — companion content is delivered
// beside the selection, but a set that selects nothing loadable is still
// empty — so the gate reports first; with no finding recorded, the empty
// error returns as it came. Every other refusal is its own message and
// returns as it came, findings or not — an empty explicit selection records
// the fragment it missed AND refuses as the empty-selection error, and that
// error is what the caller reads.
func (st *runState) refused(err error) error {
	if errors.Is(err, launch.ErrRuntimeUnavailable) || errors.Is(err, launch.ErrContextEmpty) {
		if ferr := st.gateStartup(); ferr != nil {
			return ferr
		}
	}
	return err
}

// emitDryRun renders the launch this invocation would resolve and stops.
// The SAME resolver runs, over stateless ports: an in-memory session store,
// a cell that is the project root itself. Nothing is written and nothing is
// started. It is the last thing a --dry-run does.
//
// A preview does not stop at the first problem: it renders the plan it could
// compute, carrying every finding collected so far, and only THEN closes the
// startup gate — which lists the fatal ones with their fixes and exits
// non-zero exactly where a run would refuse. Only a launch that could not be
// resolved at all is refused before a plan exists.
func (st *runState) emitDryRun() error {
	src, err := st.source()
	if err != nil {
		return err
	}
	deps, err := App().LaunchDeps(st.ctx)
	if err != nil {
		return err
	}
	deps.Sessions = sessions.NewMemStore()
	deps.Cells = operations.PreviewCells(App().LaunchFacts())
	deps.Assembler = operations.PreviewAssembler(App().Engines())
	deps.ClaimCheck = operations.PreviewClaims()
	l, err := operations.StartRun(st.ctx, deps, sessions.Seed{ProjectDir: st.workDir, ProjectID: st.projectID}, src)
	if err != nil {
		return st.refused(err)
	}
	pkg, err := launch.Open(st.ctx, deps, l)
	if err != nil {
		return err
	}
	// The preview shows what the launch would carry: a --session full
	// resume trails the assembled context exactly as the lead the launch
	// composes (resumedTranscript).
	context := pkg.Context.Text
	if runResumeSession != "" && !runResumeDistill {
		context = resumeFullContext(context, runResumeSession, func(h string) ([]agent.SessionEntry, error) {
			return operations.RecordedSessionEntries(st.ctx, App().Engines(), h)
		})
	}
	payload := dryRunJSON{
		Agent:       runAgent,
		Workspace:   string(l.Declared.Workspace),
		Runtime:     string(l.Declared.Runtime),
		Resolved:    axesJSON{Workspace: string(l.Axes.Workspace), Runtime: string(l.Axes.Runtime)},
		Environment: probedEnvironment(l.Cell),
		LLM:         l.Label.Label,
		Backend:     string(l.Engine),
		Profiles:    pkg.Selection.Profiles,
		Fragments:   pkg.Loaded,
		Context:     context,
		Delivery:    deliveryRoutes(l.Plan),
		EngineHome:  engineHomeRoute(l.Cell.HomeMode),
		Tokens:      tokens.Estimate(context),
		Prompt:      st.prompt,
	}
	if runResumeSession != "" && runResumeDistill {
		payload.ResumedEssence, payload.ResumedEssenceNote = distilledResumePreview(runResumeSession, resumeEssenceStale)
	}
	payload.Findings = findingsOf(st.gates.mode, st.gates.pending())
	if err := st.printDryRun(l, payload); err != nil {
		return err
	}
	return st.gateStartup()
}

// printDryRun emits the dry run's plan in the requested format.
func (st *runState) printDryRun(l launch.Launch, payload dryRunJSON) error {
	context := payload.Context
	return emit(st.cmd, payload, func() error {
		if runAgent != "" {
			fmt.Println("=== Agent ===")
			fmt.Printf("%s (workspace: %s, runtime: %s)\n", runAgent, l.Axes.Workspace, l.Axes.Runtime)
		}
		printEnvironment(os.Stdout, payload.Environment)
		fmt.Println("=== LLM ===")
		fmt.Printf("%s (%s)\n", l.Label.Label, l.Engine)
		fmt.Println("\n=== Profiles ===")
		printListOr(payload.Profiles, "(no profiles)")
		fmt.Println("\n=== Fragments Loaded ===")
		printListOr(payload.Fragments, "(no fragments)")
		printDeliveryRoutes(os.Stdout, payload.Delivery)
		printEngineHome(os.Stdout, payload.EngineHome)
		fmt.Printf("\n=== Assembled Context (~%d tokens) ===\n", payload.Tokens)
		if context != "" {
			fmt.Println(context)
		} else {
			fmt.Println("(no context)")
		}
		if runResumeSession != "" && runResumeDistill {
			printResumedEssence(payload)
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

// environmentJSON is the probed environment on the wire, as
// isolation.Environment.Describe names it. Where the run would refuse it reads
// isolation.RuntimeUnavailable / isolation.ReachUnknown rather than the
// fallback a refused run never gets.
type environmentJSON struct {
	Runtime string `json:"runtime"`
	Reach   string `json:"reach"`
}

// probedEnvironment describes the environment the preview cell prepared; nil
// for a cell the cells adapter did not make.
func probedEnvironment(cell launch.Cell) *environmentJSON {
	env, ok := operations.EnvironmentOf(cell)
	if !ok {
		return nil
	}
	d := env.Describe()
	return &environmentJSON{Runtime: d.Runtime, Reach: d.Reach}
}

// printEnvironment renders the probed environment as the dry-run's text form.
func printEnvironment(w io.Writer, e *environmentJSON) {
	if e == nil {
		return
	}
	fmt.Fprintln(w, "=== Environment ===")
	fmt.Fprintf(w, "runtime: %s, reach: %s\n", e.Runtime, e.Reach)
}

// printListOr prints each item indented on its own line, or none when there
// are no items.
func printListOr(items []string, none string) {
	if len(items) == 0 {
		fmt.Println(none)
		return
	}
	for _, it := range items {
		fmt.Printf("  %s\n", it)
	}
}

// printResumedEssence prints the distilled resume's essence and its note.
func printResumedEssence(payload dryRunJSON) {
	fmt.Printf("\n=== Resumed Essence (%s, delivered at session start) ===\n", runResumeSession)
	if payload.ResumedEssence != "" {
		fmt.Println(payload.ResumedEssence)
	}
	if payload.ResumedEssenceNote != "" {
		fmt.Println("(" + payload.ResumedEssenceNote + ")")
	}
}

// resumeEnv is the CTXLOOM_RESUMED_FROM/PARTS pair for whichever --session
// mode this run is in.
//
// --session --distill: distilled resume via the harp's essence (distilling on
// demand first if missing) — see resumeDistillEnv's doc for the full
// mechanism. Full resume (--session without --distill) carries its transcript
// as a trailing context block (resumedTranscript); it still sets CTXLOOM_RESUMED_FROM/
// PARTS="transcript" so the session instructions surface the "resumed from"
// note, with a PARTS value the SessionStart hook's essence injection ignores
// (the content already rode the fragment path — no double-injection).
func (st *runState) resumeEnv() map[string]string {
	switch {
	case runResumeSession != "" && runResumeDistill:
		fmt.Fprintf(os.Stderr, "ctxloom: resuming distilled essence from %s\n", runResumeSession)
		return resumeDistillEnv(runResumeSession, operations.ReadHarpEssence, resumeEssenceStale, shellOutDistill)
	case runResumeSession != "":
		fmt.Fprintf(os.Stderr, "ctxloom: resuming full transcript from %s\n", runResumeSession)
		return map[string]string{"CTXLOOM_RESUMED_FROM": runResumeSession, "CTXLOOM_RESUMED_PARTS": "transcript"}
	}
	return nil
}

// openSessionBanner prints the start-session display — a read-only summary
// of this session, BEFORE the engine spawns — and sets the terminal window
// title to the harp. It returns the terminal-title restore for runRun to
// defer — the OSC2 push/pop pair has to unwind on runRun's frame.
//
// previous is resolved via the SAME primitive the get_previous_session MCP
// tool reads — never re-derived — and is purely informational: bringing it
// back is the resume skill's job, not something this banner offers to do.
func (st *runState) openSessionBanner() func() {
	previous, prevErr := operations.ResolvePreviousSession(st.workDir, st.activeHarp)
	if prevErr != nil {
		clidiag.Warn("ctxloom", "previous-session lookup failed: %v", prevErr)
	}
	PrintStartSessionBanner(os.Stderr, StartSessionInfo{
		Harp:      st.activeHarp,
		Backend:   st.backendName,
		Label:     st.label,
		Profiles:  st.opened.Package.Selection.Profiles,
		Fragments: st.opened.Package.Loaded,
		Tokens:    tokens.Estimate(st.opened.Package.Context.Text),
		Unsafe:    unsafeLabels(st.launch),
		Previous:  previous,
	})

	// Set the terminal window title to the harp name via the OSC2 escape
	// sequence. Skipped for non-TTY (CI, piped) so we don't pollute
	// pipelines — the stderr check matters too, or `2>log` captures would
	// collect raw escape bytes. The XTWINOPS push (22;0t) / pop (23;0t) pair
	// restores the previous title on exit where supported.
	if isInteractiveTerminal() && stderrIsTerminal() {
		fmt.Fprintf(os.Stderr, "\033[22;0t\033]2;ctxloom · %s\007", st.activeHarp)
		return func() { fmt.Fprint(os.Stderr, "\033[23;0t") }
	}
	return func() {}
}

// markSessionEnded stamps the harp's end timestamp. runRun defers it only
// once openSession has minted the harp, so there is always one to mark.
func (st *runState) markSessionEnded() {
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
// and the session runs without delegation — the harness's shim refuses the
// agent tools rather than hosting a coordinator of its own.
//
// Print (oneshot) runs host the coordinator too: the parent routes through its
// own runner in every topology, so a headless coordinator brief (the echo
// smoke) exercises the same runner-terminated path as an interactive session.
// recordCoordinatorStartupFinding raises the finding for a coordinator that
// could not stand up. It is DEGRADABLE: a session without a coordinator
// cannot delegate, but every child's mail is a file spool, so nothing already
// spawned is stranded and no work is lost by launching anyway.
//
// Two shapes, by cause. A project ANOTHER live session already owns
// (coord.ErrStateOwned) is its own class: a project has one coordinator, so
// the second claimant is refused BY NAME rather than degraded into a rival
// on state of its own; --degraded still launches it, without delegation.
// Every other cause (listeners, the state dir) is an apply failure.
//
// Split out of hostCoordinator as its own function purely so it is TESTABLE:
// the call site needs a live session, a state dir and a real listener
// stand-up to reach this branch, which would have left the permitting
// finding — the one that must not silently regress into a refusal —
// asserted by nothing.
func recordCoordinatorStartupFinding(cerr error) {
	if errors.Is(cerr, coord.ErrStateOwned) {
		strictness.Fail(report.KindOwner,
			"end the session that owns this project (its pid is stamped in the state dir's "+coord.OwnerLockFileName+"), or pass --degraded (env CTXLOOM_DEGRADED=1) to launch this one without agent delegation",
			"a project has one session owner and this one is already owned: %v — this session is refused as a second coordinator; nothing the owner has spawned is affected", cerr)
		return
	}
	strictness.Fail(report.KindApply,
		"check the coordinator listeners/state dir, or pass --degraded (env CTXLOOM_DEGRADED=1) to launch without agent delegation",
		"agent coordinator startup failed: %v — this session cannot delegate; nothing it has already spawned is affected, their mail is a file spool", cerr)
}

func (st *runState) hostCoordinator() func() {
	sc, ownerToken, cerr := mcp.HostCoordinatorForSession(NewCoordinator, App(), st.workDir, st.activeHarp)
	if cerr != nil {
		recordCoordinatorStartupFinding(cerr)
		return func() {}
	}

	st.sessionCoord = sc
	// The owner's credential identifies the owner-owned run's minter
	// (startOwnedRun); the owner's RUNNER is stamped by StartOwnedRun with
	// its own per-run trio, so nothing rides this process's environment.
	st.ownerToken = ownerToken

	// A depth-0 session-owner credential is minted per `ctxloom run`
	// process; runsFold.apply re-applies every factSessionCred on
	// replay/adoption, so one never revoked would stay valid forever in the
	// project's coordinator state. Revoke on the SAME teardown that closes
	// the coordinator, and BEFORE it, while the journal is still open to
	// accept the write.
	return func() {
		// DRAIN, WAIT, THEN CLOSE -- the sequence the coordinator documents and
		// that nothing used to perform. Close() is the HARD teardown: it cancels
		// baseCtx, closes every attachment and the gRPC server, and only then
		// joins its goroutines under a BOUND it is willing to give up on. Going
		// straight there dismantled a still-finishing child's delivery path
		// underneath it, so its terminal notice had nowhere to go -- the parent
		// "always learns of a child death" invariant was decided by a race.
		//
		// BeginDrain closes admission at the verbs that mint new work and leaves
		// the transport alone, so children keep reporting while they wind down.
		// It is bounded per child (drainBound), but that bound is minutes: a
		// shutdown signal sent WHILE it waits is the operator saying not to,
		// and cuts it short -- Close() then overtakes the drain, which the
		// drain accounts for. A signal that ended the session earlier does
		// not: the children still get their drain.
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, shutdownSignals...)
		d := sc.BeginDrain()
		settled := awaitDrain(d.Done(), interrupt)
		signal.Stop(interrupt)
		if !settled {
			clidiag.Warn("ctxloom", "session exit: drain interrupted by a signal; children still running are ended by the coordinator's close")
		}
		// A PARKED child is deliberately not waited on: it keeps its turn, its
		// slot and its session lock. Naming it here is the whole reason the
		// outcome carries it -- an unattended park that nobody is told about is
		// indistinguishable from a child that finished.
		if o := d.Outcome(); len(o.Parked) > 0 || len(o.Interrupted) > 0 {
			if len(o.Parked) > 0 {
				clidiag.Warn("ctxloom", "session exit: %d child(ren) left PARKED on a human and were not waited for: %s",
					len(o.Parked), strings.Join(o.Parked, ", "))
			}
			if len(o.Interrupted) > 0 {
				clidiag.Warn("ctxloom", "session exit: %d child(ren) were still running at the drain bound and were forced: %s",
					len(o.Interrupted), strings.Join(o.Interrupted, ", "))
			}
		}
		sc.RevokeSessionOwner(ownerToken)
		sc.Close()
	}
}

// awaitDrain waits for a coordinator drain to settle, reporting false when a
// shutdown signal arrived first.
func awaitDrain(done <-chan struct{}, interrupt <-chan os.Signal) bool {
	select {
	case <-done:
		return true
	case <-interrupt:
		return false
	}
}

// seedTask handles --seed-task: move one task from the resume source store
// into this freshly minted session's store, marked for active work. Used by
// `ctxloom tasks run` to spin a browsed task into its own session. The task
// already lives in the project log; seeding marks it In Progress under the new
// session.
func (st *runState) seedTask() {
	if runSeedTask != "" {
		seedTaskIntoSession(st.workDir, st.activeHarp, runSeedTask, runSeedStatus)
	}
}

// teardownTransport kills whichever transport this run stood up. See runRun's
// own comment at the deferral site for why it is registered before the
// workspace is prepared rather than after the transport is chosen.
// teardownAll unwinds a run in the ONE order that is safe: the transport
// first, then the workspace it was running in.
//
// A named method rather than two defers because two defers run LIFO —
// workspace first — which removed the scratch tree (config overlays, socket
// dir, credential mount sources) out from under a still-live transport.
// isolation.containerWorkspace.Cleanup states the contract that violated:
// "safe to call once after the run's client is killed". Order is the whole
// invariant here.
//
// Registration stays before startTransport can return early — a defer only
// protects returns reached after it — which is why the release must
// tolerate a launch whose cell was never prepared (the zero Launch).
func (st *runState) teardownAll() {
	st.teardownTransport()
	// The cell is released after the transport that ran in it is gone —
	// removing a scratch tree under a live transport is the order this
	// method exists to forbid. The error is deliberately dropped: a cleanup
	// failure surfaces from INSIDE Cleanup (a streamed warning naming the
	// residue path), and this runs post-gate.
	_ = launch.Discard(context.Background(), st.launch)
}

func (st *runState) teardownTransport() {
	if st.pty != nil {
		st.pty.Kill()
	}
	if st.runnerHandle != nil {
		st.runnerHandle.Kill()
	}
	if st.ownedRun != nil {
		st.ownedRun.cancel()
	}
}

// startTransport starts the run's runner: every launch is an owner-owned
// run of the coordinator this process hosts (StartOwnedRun), its runner
// started through the launch's starter — on a pty for an interactive
// launch, as a plain process for a --one-shot — and handed its Launch over
// StartRun.
//
// The starter records the runner's handle on the state the moment it
// exists, so a later StartOwnedRun failure still tears it down.
func (st *runState) startTransport() error {
	var starter coord.OwnedRunStarter
	if st.launch.Mode == engine.Interactive {
		st.launch.Env = stampTerminalEnv(st.launch.Env)
		starter = st.ptyStarter()
	} else {
		starter = st.processStarter()
	}
	sess, oerr := startOwnedRun(st.ctx, st.sessionCoord, ownedRunLaunch{
		Launch:     st.launch,
		MCPServers: st.managed.ChatMCPServers(),
		OwnerToken: st.ownerToken,
		Rebind:     st.rebind,
	}, starter)
	st.ownedRun = sess
	// Everything recorded since the startup gate — above all a coordinator
	// that could not stand up (a project another live session owns is
	// refused here), and a container that never reached running (the await
	// inside the process starter) — is acted on in this window.
	if ferr := st.gates.close(PhaseTransportStart); ferr != nil {
		return ferr
	}
	if oerr != nil {
		return fmt.Errorf("failed to start the run: %w", oerr)
	}
	return nil
}

// drive runs the session over whichever transport startTransport stood up, and
// is the last thing runRun does.
func (st *runState) drive() error {
	if st.pty != nil {
		return st.driveOwnedInteractive()
	}
	// A --one-shot owner run: collect the run's FINAL answer off the
	// coordinator's event stream, record the oneshot transcript, exit with
	// the run's status.
	return runOneshotViaCoord(st.ctx, st.ownedRun, st.activeHarp, st.backendName, st.prompt, os.Stdout)
}

// sessionIO is the terminal seam set the interactive drive pumps onto the
// pty master. It is one value rather than four returns because they are
// decided together and consumed together, and restore composes onto the
// others.
type sessionIO struct {
	stdin  io.Reader
	stdout io.Writer
	resize <-chan *agent.WindowSize
	// restore unwinds the terminal (raw mode, and the observation layer's
	// scroll region + held output when one engaged). Idempotent, and a no-op
	// for a run that never took the terminal.
	restore func()
}

// prepareSessionIO decides an interactive run's terminal seams: the
// frontend owns the terminal — raw mode + stdin + resize — and the terminal
// layer (adapters/termui) wraps the seams before they are pumped onto the
// runner's pty master.
func (st *runState) prepareSessionIO() sessionIO {
	sio := sessionIO{stdout: os.Stdout, restore: func() {}}
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
			Agent:   st.boundAgent(),
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
	return sio
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

// recordOneshotAnswer closes out a `--one-shot` run (runOneshotViaCoord): it
// records the two-entry canonical transcript and reports the zero-answer case
// as a failure.
//
// transcript.RecordOneshot treats "nothing to record" as a legitimate no-op, so
// without this check `ctxloom run --one-shot ... > out.txt` would produce an
// empty file and exit 0. There is no legitimately-empty one-shot answer — one
// question was asked and none was answered — so it exits nonzero and says so.
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
// pty involved) rather than inlined at the call site above,
// mirroring how transcript.RecordOneshot itself is a standalone function the
// oneshot branch just calls. A blank harp (no session identity) or an
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
// complete. ResolveAndHeal refreshes once, unconditionally — this IS the
// call site that rule exists for: a canonical file existing here is not
// evidence it is complete.
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
	src, err := operations.ResolveAndHeal(context.Background(), App().Engines(), harp)
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
	if operations.EngineExists(App().Engines(), override) && operations.EngineAvailable(App().Engines(), override) {
		return override, nil
	}
	if operations.EngineExists(App().Engines(), override) {
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
	for _, name := range operations.EngineNames(App().Engines()) {
		if isTestOnlyBackend(name) {
			continue
		}
		if operations.EngineAvailable(App().Engines(), name) {
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

	runCmd.Flags().StringVarP(&runLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast); overrides the configured default")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "command", "r", "", "Run a saved command by name")
	runCmd.Flags().StringSliceVarP(&runFragments, "fragment", "f", nil, "Context fragment(s) to include (can be repeated)")
	runCmd.Flags().StringSliceVarP(&runTags, "tag", "t", nil, "Include fragments with this tag (can be repeated)")
	runCmd.Flags().StringVarP(&runProfile, "profile", "p", "", "Profile to use (predefined fragment collection)")
	runCmd.Flags().StringVar(&runAgent, "agent", "", "Run a named local agent binding: its composed profiles, engine, and runtime (excludes -p/-f/-t)")
	runCmd.Flags().StringVar(&runWorkspace, "workspace", "", "Session workspace axis (none|worktree; empty = project default)")
	runCmd.Flags().StringVar(&runPermissions, "permissions", "", "Permission posture: "+strings.Join(agent.PermissionModeNames(), "|")+" (overrides the agent/config default)")
	runCmd.MarkFlagsMutuallyExclusive("agent", "profile")
	runCmd.MarkFlagsMutuallyExclusive("agent", "fragment")
	runCmd.MarkFlagsMutuallyExclusive("agent", "tag")
	_ = runCmd.RegisterFlagCompletionFunc("agent", completeAgentNames)
	_ = runCmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
	_ = runCmd.RegisterFlagCompletionFunc("permissions", completePermissionModes)
	runCmd.Flags().BoolVarP(&runDryRun, "dry-run", "n", false, "Show command that would be executed")
	runCmd.Flags().BoolVar(&runOneShot, "one-shot", false, "Run one turn non-interactively, print the response, and exit")
	runCmd.Flags().BoolVar(&runPlainTerminal, "plain-terminal", false, "Disable ctxloom's terminal layer (the prefix-key agent viewer and the surround status bar) for this session")
	runCmd.Flags().BoolVar(&runNoStartupFindings, "no-startup-findings", false, "Do not deliver this launch's startup findings (what doctor reports about this run's config, companions and local state, and anything a --degraded launch proceeded past) into the agent's context")
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
func confirmUpgrade(path string, applied []string, commit func() error) {
	if runAssumeYes {
		commitUpgrade(path, commit)
		return
	}
	if !isInteractiveTerminal() {
		return // in-memory only — never a silent rewrite
	}

	fmt.Fprintf(os.Stderr, "ctxloom: %s is an older schema (%s).\n", path, strings.Join(applied, ", "))
	if yes, err := promptYesNo("Rewrite it to the current format? [y/N] "); err == nil && yes {
		commitUpgrade(path, commit)
	}
}

// confirmConfigUpgrade offers to persist one config layer's pending
// in-memory schema upgrade; nil means that layer is current.
func confirmConfigUpgrade(p *config.PendingUpgrade, commit func() error) {
	if p == nil {
		return
	}
	confirmUpgrade(p.Path, p.Applied, commit)
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
		confirmUpgrade(p.Path, p.Applied, func() error { return loader.CommitUpgrade(p) })
	}
}

// commitUpgrade persists a pending upgrade, warning (never fatal) on failure —
// the in-memory config is valid regardless.
func commitUpgrade(path string, commit func() error) {
	if err := commit(); err != nil {
		clidiag.Warn("ctxloom", "could not rewrite %s: %v", path, err)
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
