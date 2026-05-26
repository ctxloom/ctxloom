package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

var (
	runPlugin           string
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
)

// resolveResumeIntent decides whether this run resumes a prior session
// and which parts to restore. Flag bypasses win over the picker;
// non-interactive contexts (no TTY, no flags) silently fall through to
// a fresh session.
func resolveResumeIntent(mgr *sessions.Manager, workDir string) (sessions.Decision, error) {
	switch {
	case runResumeSession != "":
		return sessions.Decision{
			Action:         sessions.ResumeAction,
			FromHarp:       runResumeSession,
			RestoreSession: true,
			RestoreTasks:   !runResumeNoTasks,
		}, nil
	case runResumeTasksFrom != "":
		return sessions.Decision{
			Action:         sessions.ResumeAction,
			FromHarp:       runResumeTasksFrom,
			RestoreSession: false,
			RestoreTasks:   true,
		}, nil
	case runResumeNewSession:
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	if mgr == nil || !isInteractiveTerminal() {
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	entries, err := mgr.ListForProject(workDir)
	if err != nil || len(entries) == 0 {
		return sessions.Decision{Action: sessions.NewAction}, nil
	}
	p := &sessions.Picker{
		Entries: entries,
		In:      os.Stdin,
		Out:     os.Stderr,
		Distill: shellOutDistill,
	}
	return p.Run()
}

// shellOutDistill is the picker's `d<N>` callback. It runs
// `ctxloom session distill <harp>` as a child process so the picker
// doesn't need to depend on cobra, the compactor, or any LLM
// machinery itself. Stdout/stderr are piped through to the user.
func shellOutDistill(harpName string) error {
	exe, err := os.Executable()
	if err != nil {
		exe = "ctxloom"
	}
	c := exec.Command(exe, "session", "distill", harpName)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
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
	Annotations: map[string]string{
		AnnotationLLMWrapper: "true",
	},
	Long: `Assemble context from fragments and execute the configured AI plugin.

Fragments are loaded from bundles in .ctxloom/bundles/.

Use --profile/-p to load a predefined set of fragments and variables.
Use --tag/-t to include all fragments with a specific tag.
Additional -f flags will be appended to the profile's fragments.

The AI plugin runs in isolation, ignoring default context files like Claude.md.

Verbosity levels (-v can be repeated):
  -v      Show plugin commands being executed
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

		// Determine which plugin to use
		pluginName := runPlugin
		if pluginName == "" {
			pluginName = cfg.GetDefaultLLMPlugin()
		}

		// Verify the backend exists
		if !backends.Exists(pluginName) {
			return fmt.Errorf("unknown plugin: %s (available: %v)", pluginName, backends.List())
		}

		// Get plugin configuration from config
		pluginCfg := cfg.LM.Plugins[pluginName]

		// Build the prompt - from saved prompt, flag, or remaining args
		// Empty prompt is allowed (starts interactive mode)
		prompt := runPrompt
		if prompt == "" && runSavedPrompt != "" {
			loader := bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled())
			promptObj, err := loader.GetPrompt(runSavedPrompt)
			if err != nil {
				return fmt.Errorf("failed to load prompt: %w", err)
			}
			prompt = promptObj.Content
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

		// Determine model to use: plugin config > global default
		model := pluginCfg.Model
		if model == "" {
			model = cfg.GetDefaultLLMModel()
		}

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
		for k, v := range pluginCfg.Env {
			runEnv[k] = v
		}
		sessMgr, sessMgrErr := sessions.Open("")
		if sessMgrErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: session index open failed: %v\n", sessMgrErr)
		}
		resume, err := resolveResumeIntent(sessMgr, workDir)
		if err != nil {
			return fmt.Errorf("resume intent: %w", err)
		}
		if resume.Action == sessions.QuitAction {
			fmt.Fprintln(os.Stderr, "ctxloom: cancelled")
			return nil
		}
		if sessMgr != nil {
			if entry, err := sessMgr.AssignHarp(workDir, pluginName); err != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: session naming failed: %v\n", err)
			} else {
				runEnv["CTXLOOM_SESSION_HARP"] = entry.HarpName
				if resume.Action == sessions.ResumeAction {
					parts := resumePartsCSV(resume)
					runEnv["CTXLOOM_RESUMED_FROM"] = resume.FromHarp
					runEnv["CTXLOOM_RESUMED_PARTS"] = parts
					fmt.Fprintf(os.Stderr, "ctxloom: starting session %s (resuming from %s: %s)\n",
						entry.HarpName, resume.FromHarp, parts)
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
			fmt.Println("=== Plugin ===")
			fmt.Println(pluginName)
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
		var client *pb.PluginClient
		if pluginCfg.BinaryPath != "" {
			// Use external plugin binary
			client, err = pb.NewPluginClient(pluginCfg.BinaryPath, pluginCfg.Args, runVerbosity)
		} else {
			// Use built-in plugin via self-invocation
			client, err = pb.NewSelfInvokingClient(pluginName, runVerbosity)
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

func init() {
	rootCmd.AddCommand(runCmd)

	runCmd.Flags().StringVarP(&runPlugin, "plugin", "l", "", "LLM to use (default from config)")
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

	// Register completions
	_ = runCmd.RegisterFlagCompletionFunc("plugin", completePluginNames)
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

