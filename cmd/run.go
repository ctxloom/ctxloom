package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/tasks"
	"github.com/ctxloom/ctxloom/internal/upgrade"
)

var (
	runLLM              string
	runPrompt           string
	runFragments        []string
	runTags             []string
	runProfile          string
	runSavedPrompt      string
	runDryRun           bool
	runSuppressWarnings bool
	runPrint            bool
	runVerbosity        int
	runAssumeYes        bool
	runResumeSession    string
	runResumeTasksFrom  string
	runResumeNewSession bool
	runResumeNoTasks    bool
	runSeedTask         string
	runSeedStatus       string
)

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
		backend, historyForBackend(backend), newAdoptFunc(workDir, backend))
}

// resolveResumeIntentWith is the IoC seam: takes the resume flags, the project's
// already-read indexed sessions, the work directory, a "stdin is a TTY" boolean,
// and the backend's name + session history + adopt callback. All side-effect
// surfaces are arguments, so the decision tree is trivially unit-testable across
// the flag matrix. indexed may be empty and adopt may be nil (e.g. unknown
// backend); the picker then simply shows no rows / refuses adoption.
func resolveResumeIntentWith(flags resumeFlags, indexed []sessions.Entry, workDir string, isTTY bool,
	backend string, hist backends.SessionHistory, adopt sessions.AdoptFunc) (sessions.Decision, error) {
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
	entries := buildPickerEntries(indexed, rawTranscripts(hist, workDir), backend)
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

// historyForBackend returns the named backend's session history, or nil when
// the backend is unknown — so raw-transcript scanning degrades to "no raw rows"
// rather than failing the picker.
func historyForBackend(name string) backends.SessionHistory {
	if name == "" || !backends.Exists(name) {
		return nil
	}
	b := backends.Get(name)
	if b == nil {
		return nil
	}
	return b.History()
}

// rawTranscripts lists the backend's raw transcripts for workDir, best-effort.
func rawTranscripts(hist backends.SessionHistory, workDir string) []backends.SessionMeta {
	if hist == nil {
		return nil
	}
	metas, err := hist.ListSessions(workDir)
	if err != nil {
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
func buildPickerEntries(indexed []sessions.Entry, raw []backends.SessionMeta, backend string) []sessions.Entry {
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

// osExecutable and osStat are seams the tests override to drive the
// resolveSelfExecutable decision tree without depending on the real
// filesystem or the real binary path.
var (
	osExecutable = os.Executable
	osStat       = os.Stat
)

// resolveSelfExecutable returns the path to use when re-invoking ctxloom
// from inside a running ctxloom process. Prefers the OS-reported absolute
// path (one syscall: `readlink /proc/self/exe` on Linux, `_NSGetExecutablePath`
// on macOS, `GetModuleFileNameW` on Windows), and falls back to bare
// `"ctxloom"` (a PATH lookup) in two cases:
//
//  1. The OS call itself errored (rare; AIX, some sandboxed environments).
//  2. The returned path no longer points at a live binary. On Linux,
//     `os.Executable()` returns a literal `"<path> (deleted)"` string after
//     the binary is replaced via unlink+recreate (the typical `go install`
//     upgrade pattern). exec'ing that path fails at .Run() time, which is
//     recoverable but ugly. We strip the suffix, stat the result, and only
//     use it if the file still exists.
func resolveSelfExecutable() string {
	const fallback = "ctxloom"
	exe, err := osExecutable()
	if err != nil {
		return fallback
	}
	// Linux: "/path/ctxloom (deleted)" after the running inode is unlinked.
	if trimmed, ok := strings.CutSuffix(exe, " (deleted)"); ok {
		exe = trimmed
	}
	if _, err := osStat(exe); err != nil {
		return fallback
	}
	return exe
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
	res, err := operations.SetTaskStatus(operations.TaskContext{
		WorkDir:     workDir,
		ProjectID:   os.Getenv("CTXLOOM_PROJECT_ID"),
		SessionHarp: activeHarp,
	}, harpID, status)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: seed task %s: %v\n", harpID, err)
		return
	}
	if res.Warning != "" {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", res.Warning)
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

Fragments are loaded from bundles in .ctxloom/bundles/.

Use --profile/-p to load a predefined set of fragments and variables.
Use --tag/-t to include all fragments with a specific tag.
Additional -f flags will be appended to the profile's fragments.

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
		// Load configuration
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		// If loading upgraded an older config schema in memory, offer to persist
		// it (interactive + consented only; never a silent rewrite).
		confirmUpgrade(cfg.PendingUpgrade, cfg.CommitUpgrade)
		// Profiles can carry an older schema too (e.g. bare bundle refs); offer to
		// persist those rewrites the same way.
		confirmProfileUpgrades(cfg)

		// Determine which LLM config to use. An explicit --llm override names a
		// config label, validated up front (friction); no override uses the
		// primary role's label. The label resolves to a backend type + model;
		// the backend name (not the label) drives session naming and transport.
		label, err := resolveRunLLM(cfg, runLLM)
		if err != nil {
			return err
		}
		backendName, labelModel := cfg.ResolveLLM(label)
		// An ad-hoc --llm naming a backend type (not a configured label)
		// resolves to that backend directly rather than the built-in default.
		if _, configured := cfg.LM.Configs[label]; !configured && backends.Exists(label) {
			backendName, labelModel = label, ""
		}
		llmBinary, llmArgs := llmBinaryArgsFor(cfg, label)
		llmEnv := llmEnvFor(cfg, label)

		// Build the prompt - from saved prompt, flag, or remaining args
		// Empty prompt is allowed (starts interactive mode)
		prompt := runPrompt
		if prompt == "" && runSavedPrompt != "" {
			promptRes, err := operations.GetPrompt(cmd.Context(), cfg, operations.GetPromptRequest{Name: runSavedPrompt})
			if err != nil {
				return fmt.Errorf("failed to load prompt: %w", err)
			}
			prompt = promptRes.Content
		}
		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}

		// Assemble context using operations
		ctx := context.Background()

		// Auto-sync remote dependencies on startup if enabled (graceful failure).
		// Mirrors the behavior of `ctxloom mcp` so the run path doesn't hard-fail
		// on missing parent profiles or bundles that sync would have fetched.
		// In a TTY, confirm with the user before installing anything new.
		if cfg.Sync.ShouldAutoSync() && confirmSyncInstall(ctx, cfg) {
			syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
			result, syncErr := operations.SyncOnStartup(syncCtx, cfg)
			syncCancel()
			if syncErr != nil {
				if !errors.Is(syncErr, context.Canceled) {
					fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", syncErr)
				}
			} else {
				writeSyncSummary(os.Stderr, result)
			}
		}

		// Surface untrusted bundle changes that sync left pending. Trusted
		// remotes auto-apply; the rest wait for an explicit CLI review (this
		// replaces the former in-chat review gate — startup is never blocked).
		if pending := operations.PendingBundleChanges(cfg); !pending.IsEmpty() {
			fmt.Fprintf(os.Stderr, "ctxloom: %d bundle change(s) pending review — run `ctxloom bundle review`\n", len(pending.All()))
		}

		ctxResult, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{
			Profile:   runProfile,
			Fragments: runFragments,
			Tags:      runTags,
		})
		if err != nil {
			return fmt.Errorf("failed to assemble context: %w", err)
		}

		// If user explicitly requested fragments (-f flags) but none loaded, that's an error
		if len(runFragments) > 0 && len(ctxResult.FragmentsLoaded) == 0 {
			return fmt.Errorf("no fragments loaded: all requested fragments not found")
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

		// Determine work directory: git root if in repo, current directory otherwise
		workDir := ""
		if root, err := gitutil.FindRoot("."); err == nil {
			workDir = root
		} else if cwd, err := os.Getwd(); err == nil {
			workDir = cwd
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
			fmt.Fprintf(os.Stderr, "ctxloom: warning: session index open failed: %v\n", upErr)
		} else {
			confirmUpgrade(pending, commit)
		}
		var activeHarp string
		if entry, err := operations.AssignSession(workDir, backendName); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: session naming failed: %v\n", err)
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
							fmt.Fprintf(os.Stderr, "ctxloom: warning: could not distill %s for resume essence: %v\n", resume.FromHarp, dErr)
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
			// non-TTY (CI, piped) so we don't pollute pipelines.
			if isInteractiveTerminal() {
				fmt.Fprintf(os.Stderr, "\033]2;ctxloom · %s\007", entry.HarpName)
			}
		}
		// Resolve the project's stable identity (ADR 0025) and export it into the
		// session env. Fault-tolerant — any failure warns and leaves
		// CTXLOOM_PROJECT_ID unset; the task store degrades rather than blocking.
		if pid, warning, err := operations.ResolveProjectIdentity(workDir); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: project identity unresolved: %v\n", err)
		} else {
			runEnv["CTXLOOM_PROJECT_ID"] = pid
			if warning != "" {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", warning)
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
					fmt.Fprintf(os.Stderr, "ctxloom: warning: session end-mark failed: %v\n", err)
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

		// Build request
		req := &pb.RunRequest{
			Fragments: protoFragments,
			Prompt:    promptFragment,
			Options: &pb.RunOptions{
				WorkDir:     workDir,
				AutoApprove: true,
				Mode:        mode,
				Env:         runEnv,
				Verbosity:   uint32(runVerbosity * 16), // Each -v adds 16 to verbosity level
				Model:       model,                     // e.g., "opus", "sonnet", "haiku"
			},
		}

		// Dry run mode - show the assembled context and prompt
		if runDryRun {
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
			fmt.Println("\n=== Assembled Context ===")
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
			fmt.Printf("Would write to: %s/[hash].md\n", filepath.Join(workDir, backends.SCMContextSubdir))
			return nil
		}

		// Create plugin client
		var client *pb.LLMRunner
		if llmBinary != "" {
			// Use external plugin binary
			client, err = pb.NewLLMRunner(llmBinary, llmArgs, runVerbosity)
		} else {
			// Use built-in plugin via self-invocation
			client, err = pb.NewSelfInvokingClient(backendName, runVerbosity)
		}
		if err != nil {
			return fmt.Errorf("failed to start plugin: %w", err)
		}
		defer client.Kill()

		// Run the AI plugin
		exitCode, err := client.Run(context.Background(), req, os.Stdout, os.Stderr)
		if err != nil {
			return fmt.Errorf("AI plugin failed: %w", err)
		}

		if exitCode != 0 {
			return &ExitError{Code: int(exitCode)}
		}

		return nil
	},
}

// resolveRunLLM picks the config label for this run. An explicit override
// (--llm) names a label and is validated up front (friction-up-front): it must
// be a configured label, or — as a convenience — a registered backend type
// whose binary is installed (treated as an ad-hoc label resolving to that
// backend). With no override, the primary role's label is used (the graceful
// path).
func resolveRunLLM(cfg *config.Config, override string) (string, error) {
	if override == "" {
		return cfg.PrimaryLabel(), nil
	}
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

	runCmd.Flags().StringVarP(&runLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast, gemini-code, gemini-fast); overrides the configured default")
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt to send to the AI (alternative to positional args)")
	runCmd.Flags().StringVarP(&runSavedPrompt, "run-prompt", "r", "", "Run a saved prompt by name")
	runCmd.Flags().StringSliceVarP(&runFragments, "fragment", "f", nil, "Context fragment(s) to include (can be repeated)")
	runCmd.Flags().StringSliceVarP(&runTags, "tag", "t", nil, "Include fragments with this tag (can be repeated)")
	runCmd.Flags().StringVarP(&runProfile, "profile", "p", "", "Profile to use (predefined fragment collection)")
	runCmd.Flags().BoolVarP(&runDryRun, "dry-run", "n", false, "Show command that would be executed")
	runCmd.Flags().BoolVarP(&runSuppressWarnings, "quiet", "q", false, "Suppress warnings (e.g., variable redefinition)")
	runCmd.Flags().BoolVar(&runPrint, "print", false, "Print response and exit (non-interactive mode)")
	runCmd.Flags().CountVarP(&runVerbosity, "verbose", "v", "Increase verbosity (can be repeated: -v, -vv, -vvv)")
	runCmd.Flags().BoolVarP(&runAssumeYes, "yes", "y", false, "Assume yes for the install-on-startup prompt")

	// Phase 3 resume flags. When none are passed, the interactive picker
	// runs (TTY only); piped/non-interactive invocations fall through to
	// a fresh session.
	runCmd.Flags().StringVar(&runResumeSession, "session", "", "Resume the named harp session (essence + tasks). Skips the picker.")
	runCmd.Flags().StringVar(&runResumeTasksFrom, "tasks-from", "", "Start a fresh session but hydrate tasks from the named harp session. Skips the picker.")
	runCmd.Flags().BoolVar(&runResumeNewSession, "new-session", false, "Start a fresh session without resume. Skips the picker.")
	runCmd.Flags().BoolVar(&runResumeNoTasks, "no-tasks", false, "When combined with --session, skip task restoration (essence only).")

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
	fmt.Fprint(os.Stderr, "Rewrite it to the current format? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer == "y" || answer == "yes" {
		commitUpgrade(p, commit)
	}
}

// confirmProfileUpgrades offers to persist any older-schema rewrites that loading
// the configured profiles applied in memory (e.g. bare bundle refs qualified with
// their remote). It resolves each default profile through one loader — which
// loads parents too — so every pending rewrite is surfaced, then prompts per file
// via the shared confirmUpgrade path. No pending means every profile was current.
func confirmProfileUpgrades(cfg *config.Config) {
	loader := cfg.GetProfileLoader()
	for _, name := range cfg.Profiles.Defaults {
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
		fmt.Fprintf(os.Stderr, "ctxloom: warning: could not rewrite %s: %v\n", p.Path, err)
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
	fmt.Fprint(os.Stderr, "Proceed? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctxloom: skipping sync")
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return true
	}
	fmt.Fprintln(os.Stderr, "ctxloom: skipping sync")
	return false
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
