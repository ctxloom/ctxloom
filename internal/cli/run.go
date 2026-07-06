package cli

import (
	"bufio"
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

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

var (
	runLLM              string
	runAgent            string
	runWorkspace        string
	runPermissions      string
	runPrompt           string
	runFragments        []string
	runTags             []string
	runProfile          string
	runSavedPrompt      string
	runDryRun           bool
	runPrint            bool
	runStructured       bool
	runVerbosity        int
	runAssumeYes        bool
	runResumeSession    string
	runResumeTasksFrom  string
	runResumeNewSession bool
	runResumeNoTasks    bool
	runSeedTask         string
	runSeedStatus       string
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

// resumeFlags bundles the four CLI flags that govern resume behavior
// so resolveResumeIntent can be tested without setting package globals.
type resumeFlags struct {
	Session    string // --session <harp>
	TasksFrom  string // --tasks-from <harp>
	NewSession bool   // --new-session
	NoTasks    bool   // --no-tasks (modifier on --session)
}

// resolveResumeIntent decides whether this run resumes a prior session
// and which parts to restore. Flag bypasses win over the picker;
// non-interactive contexts (no TTY, no flags) silently fall through to
// a fresh session.
func resolveResumeIntent(workDir, backend string) (sessions.Decision, error) {
	flags := resumeFlags{
		Session:    runResumeSession,
		TasksFrom:  runResumeTasksFrom,
		NewSession: runResumeNewSession,
		NoTasks:    runResumeNoTasks,
	}
	// Best-effort read of the project's indexed sessions: a failure just
	// narrows the picker, never blocks launch (CLAUDE.md fault tolerance).
	indexed, _ := operations.ListSessionsForProject(workDir)
	return resolveResumeIntentWith(flags, indexed, workDir, isInteractiveTerminal(),
		backend, sessionSourceForBackend(backend), newAdoptFunc(workDir, backend))
}

// resolveResumeIntentWith is the IoC seam: takes the resume flags, the project's
// already-read indexed sessions, the work directory, a "stdin is a TTY" boolean,
// and the backend's name + session history + adopt callback. All side-effect
// surfaces are arguments, so the decision tree is trivially unit-testable across
// the flag matrix. indexed may be empty and adopt may be nil (e.g. unknown
// backend); the picker then simply shows no rows / refuses adoption.
func resolveResumeIntentWith(flags resumeFlags, indexed []sessions.Entry, workDir string, isTTY bool,
	backend string, source pb.SessionSource, adopt sessions.AdoptFunc) (sessions.Decision, error) {
	switch {
	case flags.Session != "":
		return sessions.Decision{
			Action:         sessions.ResumeAction,
			FromHarp:       flags.Session,
			RestoreSession: true,
			RestoreTasks:   !flags.NoTasks,
		}, nil
	case flags.TasksFrom != "":
		return sessions.Decision{
			Action:         sessions.ResumeAction,
			FromHarp:       flags.TasksFrom,
			RestoreSession: false,
			RestoreTasks:   true,
		}, nil
	case flags.NewSession:
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	if !isTTY {
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	// Combine indexed harp sessions with raw, not-yet-adopted backend
	// transcripts (e.g. sessions started outside `ctxloom run`). The raw read is
	// best-effort: a failure just narrows the picker, never blocks launch.
	entries := buildPickerEntries(indexed, rawTranscripts(source), backend)
	if len(entries) == 0 {
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	p := &sessions.Picker{
		Entries: entries,
		In:      os.Stdin,
		Out:     os.Stderr,
		Distill: shellOutDistill,
		Adopt:   adopt,
	}
	return p.Run()
}

// sessionSourceForBackend returns a gRPC transcript reader for the named
// backend, or nil when the backend is unknown — so raw-transcript scanning
// degrades to "no raw rows" rather than failing the picker. The reader fetches
// over the agent server (self-situated), not the host filesystem, so the picker
// works the same for a remote agent.
func sessionSourceForBackend(name string) pb.SessionSource {
	if name == "" || !backends.Exists(name) {
		return nil
	}
	return pb.NewSessionReader(name, runVerbosity)
}

// rawTranscriptsTimeout bounds the pre-launch raw-transcript scan. The read
// is best-effort picker input; a hung backend plugin must narrow the picker,
// never stall the launch.
const rawTranscriptsTimeout = 5 * time.Second

// rawTranscripts lists the backend's raw transcripts via the agent server,
// best-effort and deadline-bounded. The agent self-situates its workspace; no
// dir is passed. Any failure (including timeout) warns and returns nil so the
// picker degrades to indexed sessions only.
func rawTranscripts(source pb.SessionSource) []agent.SessionMeta {
	if source == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), rawTranscriptsTimeout)
	defer cancel()
	metas, err := source.ListSessions(ctx)
	if err != nil {
		clidiag.Warn("ctxloom", "listing raw backend transcripts failed (%v); the resume picker shows indexed sessions only", err)
		return nil
	}
	return metas
}

// newAdoptFunc builds the picker's adopt callback: assign a fresh harp for the
// chosen raw transcript and bind the transcript's session id / path to it, so
// the downstream resume path distills and injects it like any other harp. The
// index write is delegated to operations; an open failure surfaces as an error
// to the picker rather than blocking launch.
func newAdoptFunc(workDir, backend string) sessions.AdoptFunc {
	return func(sessionID, transcriptPath string) (string, error) {
		return operations.AdoptRawSession(workDir, backend, sessionID, transcriptPath)
	}
}

// buildPickerEntries merges indexed harp sessions with raw, not-yet-adopted
// backend transcripts into one most-recent-first list for the picker. A raw
// transcript already represented in the index — matched by transcript path or
// session id — is dropped so a session never appears twice. Surviving raw
// transcripts become Entry values with an empty HarpName (the picker's "raw"
// sentinel), carrying the session id / path the adopt step needs.
func buildPickerEntries(indexed []sessions.Entry, raw []agent.SessionMeta, backend string) []sessions.Entry {
	known := make(map[string]struct{}, len(indexed)*2)
	for _, e := range indexed {
		if e.TranscriptPath != "" {
			known[filepath.Clean(e.TranscriptPath)] = struct{}{}
		}
		if e.SessionID != "" {
			known["id:"+e.SessionID] = struct{}{}
		}
	}
	out := append([]sessions.Entry(nil), indexed...)
	for _, m := range raw {
		if m.ID == "" && m.Path == "" {
			continue
		}
		if m.Path != "" {
			if _, ok := known[filepath.Clean(m.Path)]; ok {
				continue
			}
		}
		if m.ID != "" {
			if _, ok := known["id:"+m.ID]; ok {
				continue
			}
		}
		out = append(out, sessions.Entry{
			Backend:        backend,
			SessionID:      m.ID,
			TranscriptPath: m.Path,
			StartedAt:      m.StartTime,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// execCommand is the seam tests override to avoid actually shelling
// out. Production points it at exec.Command; tests substitute a fake
// that records the arguments and returns a harmless exec.Cmd (e.g.,
// /bin/true) so .Run() succeeds without side effects.
var execCommand = exec.Command

// shellOutDistill is the picker's `d<N>` callback. It runs
// `ctxloom session distill <harp>` as a child process so the picker
// doesn't need to depend on cobra, the compactor, or any LLM
// machinery itself. Stdout/stderr are piped through to the user.
func shellOutDistill(harpName string) error {
	exe := resolveSelfExecutable()
	c := execCommand(exe, "session", "distill", harpName)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
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
// change rather than a move between per-session stores. All failures warn and
// return — seeding never blocks the launch.
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
		clidiag.Warn("ctxloom", "seed task %s: %v", harpID, err)
		return
	}
	if res.Warning != "" {
		clidiag.Warn("ctxloom", "%s", res.Warning)
	}
	fmt.Fprintf(os.Stderr, "ctxloom: seeded task %s into %s (%s)\n", res.Task.HarpID, activeHarp, res.Task.Status)
}

func resumePartsCSV(d sessions.Decision) string {
	parts := make([]string, 0, 2)
	if d.RestoreSession {
		parts = append(parts, "session")
	}
	if d.RestoreTasks {
		parts = append(parts, "tasks")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

var runCmd = &cobra.Command{
	Use:   "run [flags] [prompt...]",
	Short: "Assemble context and run AI",
	Long: `Assemble context from fragments and execute the configured LLM.

Fragments are loaded from installed bundles: local bundles in
.ctxloom/cache/bundles/ plus remote bundles pinned in the lockfile.

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

Examples:
  ctxloom run -f coding-standards "review this code"
  ctxloom run -p developer "explain the architecture"
  ctxloom run -p reviewer -f extra-rules "review this PR"
  ctxloom run -t security "check for vulnerabilities"
  ctxloom run -vv -p developer "debug mode"`,
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

		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		// config.Load downgrades unreadable/malformed/schema-invalid files to
		// warnings (CLAUDE.md fault tolerance) — surface them so a corrupted
		// config.yaml never silently launches an empty-context session.
		printConfigWarnings(os.Stderr, cfg.Warnings)
		// If loading upgraded an older config schema in memory, offer to persist
		// it (interactive + consented only; never a silent rewrite).
		confirmUpgrade(cfg.PendingUpgrade, cfg.CommitUpgrade)
		// Profiles can carry an older schema too (e.g. bare bundle refs); offer to
		// persist those rewrites the same way.
		confirmProfileUpgrades(cfg)

		// Build the prompt - from saved prompt, flag, or remaining args
		// Empty prompt is allowed (starts interactive mode)
		prompt := runPrompt
		if prompt == "" && runSavedPrompt != "" {
			promptRes, err := operations.GetSkill(cmd.Context(), cfg, operations.GetSkillRequest{Name: runSavedPrompt})
			if err != nil {
				return fmt.Errorf("failed to load prompt: %w", err)
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
		if cfg.Sync.ShouldAutoSync() && !runDryRun && confirmSyncInstall(ctx, cfg) {
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
			agentRuntime = cfg.Runtime
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
		} else if runProfile == "" && len(runFragments) == 0 && len(runTags) == 0 {
			// Bare launch: no --agent and no explicit context selection. Bind the
			// always-bound DEFAULT AGENT (cfg.DefaultAgent) exactly like --agent —
			// its composed profiles become the context and its engine + runtime +
			// permissions the transport (profiles.defaults was retired). Unlike
			// --agent (a HARD error on an unknown name), a missing/empty/unresolvable
			// default_agent must NEVER block startup: warn and continue with empty
			// context at the project-default label + runtime (CLAUDE.md fault
			// tolerance; mirrors acp's openACPEngineChat degrade).
			if rs, rerr := operations.ResolveAgent(ctx, cfg, cfg.DefaultAgent, runLLM); rerr != nil {
				strictness.Fail(strictness.ClassRef, "set a default agent (ctxloom agent default <name>) or pass --degraded to launch anyway", "default agent %q unavailable; continuing with empty context: %v", cfg.DefaultAgent, rerr)
				ctxResult = &operations.AssembleContextResult{}
				var lerr error
				// resolveRunLLM: --llm override, else the project primary label.
				label, lerr = resolveRunLLM(cfg, runLLM, "")
				if lerr != nil {
					return lerr
				}
				backendName, labelModel = operations.ResolveBackend(cfg, label)
				agentRuntime = cfg.Runtime
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
			sessionWorkspace = cfg.Workspace
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

		// Phase 3 session resolution: optional pre-launch resume picker,
		// followed by a fresh harp assignment for the new session.
		runEnv := map[string]string{}
		for k, v := range llmEnv {
			runEnv[k] = v
		}
		resume, err := resolveResumeIntent(workDir, backendName)
		if err != nil {
			return fmt.Errorf("resume intent: %w", err)
		}
		if resume.Action == sessions.QuitAction {
			fmt.Fprintln(os.Stderr, "ctxloom: cancelled")
			return nil
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
			if resume.Action == sessions.ResumeAction {
				parts := resumePartsCSV(resume)
				runEnv["CTXLOOM_RESUMED_FROM"] = resume.FromHarp
				runEnv["CTXLOOM_RESUMED_PARTS"] = parts
				fmt.Fprintf(os.Stderr, "ctxloom: starting session %s (resuming from %s: %s)\n",
					entry.HarpName, resume.FromHarp, parts)
				// Ensure the resumed session is distilled before launch so
				// the SessionStart hook can inject its essence. Distilling
				// here keeps the LLM call on the acceptable startup path
				// rather than on /clear.
				if resumePartsIncludeSession(parts) {
					if _, essErr := readHarpEssence(resume.FromHarp); essErr != nil {
						if dErr := shellOutDistill(resume.FromHarp); dErr != nil {
							clidiag.Warn("ctxloom", "could not distill %s for resume essence: %v", resume.FromHarp, dErr)
						}
					}
				}
			} else {
				fmt.Fprintf(os.Stderr, "ctxloom: starting session %s\n", entry.HarpName)
			}
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
		if pid, warning, err := taskops.ResolveProjectIdentity(workDir); err != nil {
			clidiag.Warn("ctxloom", "project identity unresolved: %v", err)
		} else {
			runEnv["CTXLOOM_PROJECT_ID"] = pid
			if warning != "" {
				clidiag.Warn("ctxloom", "%s", warning)
			}
		}

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
		// MCP/hooks/skill exports are gated at their own choke, TR5) AND
		// ctxResult.Profiles — the SELECTED profile set (from -p, or the resolved
		// defaults) that AssembleContext scoped context to. Passing the profiles
		// here scopes the managed mcp/skills/hooks to the SAME profiles, so
		// `run -p X` no longer leaks the default profile's MCP or every pulled
		// bundle's skills into X's session.
		// Launch-time permission posture. Precedence: --permissions flag > agent
		// binding > engine-label config > built-in default (claude-code → bypass
		// while container isolation isn't relied on; others prompt). A headless
		// ONESHOT upgrades a would-block posture to bypass or it would hang with no
		// human to answer the engine.
		labelPerm := cfg.LM.Configs[label].Permissions
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
			ManagedConfig: pb.ManagedConfigToProto(backends.AssembleManagedConfig(backendName, workDir, execGate.Gate(), ctxResult.Profiles)),
		}
		// Advisory: tell the user if a bundle executable was withheld (content-free).
		execGate.WarnWithheld()

		// Create plugin client. The isolation axes (runAxes, default none/host) decide
		// WHERE the top-level run's workspace lives and HOW its plugin is spawned.
		// The external-plugin-binary path is spawned directly — isolation wraps the
		// built-in serve transport, not a user-supplied binary — and stays none.
		var client pb.Client
		if llmBinary != "" {
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
			prepared, ws := isolation.Prepare(ctx, runAxes, backendName, operations.IsolationImageConfig(cfg, backendName), workDir, activeHarp)
			// The permission posture is resolved once from config/CLI/agent and is
			// authoritative regardless of how the isolation boundary degrades: a
			// container that failed to launch does NOT drop a configured bypass —
			// that is the point of the host stopgap.
			policy := prepared
			// Tear the workspace down after the client is killed (kill the plugin/
			// container before removing its scratch — WIP-safe). Registered before
			// client.Kill so it runs after, and before the gate below so an abort on
			// a container→worktree degrade still tears the prepared worktree down.
			// none's cleanup is a noop.
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
			// Spawn through the policy, carrying the resolved label so serve
			// configures exactly this entry (not the first map-ordered entry of the
			// same type).
			client, err = policy.SpawnClient(backendName, label, runVerbosity, ws)
			if err != nil {
				return fmt.Errorf("failed to start plugin: %w", err)
			}
		}
		defer client.Kill()

		// --structured: drive the session as a structured turn REPL (the gRPC
		// WatchSession + user_message interface) instead of owning the terminal.
		if runStructured {
			return runStructuredREPL(ctx, client, req, outputFormatOf(cmd), os.Stdin, os.Stdout)
		}

		// For an interactive run the frontend owns the terminal: raw mode + stdin
		// + resize are pumped over the bidi Run stream to the controller's pty.
		// Oneshot runs need none of that.
		var stdin io.Reader
		var resize <-chan *pb.WindowSize
		restoreTerm := func() {}
		if mode == pb.ExecutionMode_INTERACTIVE {
			stdin, resize, restoreTerm = interactiveTerminal(ctx)
			// Deferred so a panic inside client.Run can't strand the shell
			// in raw mode. restoreTerm is idempotent; the inline call below
			// still restores before any normal-path output.
			defer restoreTerm()
		}

		// Run the AI plugin
		exitCode, err := client.Run(ctx, req, stdin, os.Stdout, os.Stderr, resize)
		restoreTerm()
		if err != nil {
			return fmt.Errorf("AI plugin failed: %w", err)
		}

		if exitCode != 0 {
			return &ExitError{Code: int(exitCode)}
		}

		return nil
	},
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
	if _, configured := cfg.LM.Configs[override]; configured {
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
	for label := range cfg.LM.Configs {
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
	runCmd.Flags().StringVarP(&runSavedPrompt, "run-prompt", "r", "", "Run a saved prompt by name")
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
	runCmd.MarkFlagsMutuallyExclusive("structured", "print")
	runCmd.Flags().CountVarP(&runVerbosity, "verbose", "v", "Increase verbosity (can be repeated: -v, -vv, -vvv)")
	runCmd.Flags().BoolVarP(&runAssumeYes, "yes", "y", false, "Assume yes for the install-on-startup prompt")

	// Phase 3 resume flags. When none are passed, the interactive picker
	// runs (TTY only); piped/non-interactive invocations fall through to
	// a fresh session.
	runCmd.Flags().StringVar(&runResumeSession, "session", "", "Resume the named harp session (essence + tasks). Skips the picker.")
	runCmd.Flags().StringVar(&runResumeTasksFrom, "tasks-from", "", "Start a fresh session but hydrate tasks from the named harp session. Skips the picker.")
	runCmd.Flags().BoolVar(&runResumeNewSession, "new-session", false, "Start a fresh session without resume. Skips the picker.")
	runCmd.Flags().BoolVar(&runResumeNoTasks, "no-tasks", false, "When combined with --session, skip task restoration (essence only).")
	// Contradictory resume intents are rejected up front rather than silently
	// resolved by flag precedence.
	runCmd.MarkFlagsMutuallyExclusive("session", "new-session")
	runCmd.MarkFlagsMutuallyExclusive("tasks-from", "no-tasks")

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
