package cmd

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
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/resources"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .ctxloom directory",
	Long: `Initialize a new .ctxloom directory in the current working directory.

This creates a marker directory that ctxloom uses to identify a project root.
All ctxloom data (profiles, bundles, fragments, prompts) will be stored here.

If no .ctxloom directory exists when running ctxloom commands, the user home ~/.ctxloom
is used as a fallback.

When run interactively (TTY detected), init will guide you through:
  1. Selecting an AI engine (claude-code, antigravity, etc.)
  2. Optionally adding a personal ctxloom repository as a remote
  3. Launching your AI to help discover and configure profiles

Examples:
  ctxloom init                     # Interactive setup (if TTY)
  ctxloom init --home              # Initialize in ~/.ctxloom
  ctxloom init --engine antigravity # Pre-select engine
  ctxloom init --non-interactive   # Skip all prompts`,
	RunE: runInit,
}

var (
	initHome           bool
	initNonInteractive bool
	initSkipLaunch     bool
	initEngine         string
	initRemotes        []string
	initForge          string
)

func init() {
	rootCmd.AddCommand(initCmd)
	bindInitFlags(initCmd)
}

// bindInitFlags binds the init flag set to cmd. Shared by the top-level `init`
// alias and `manage init` so both surfaces accept the same options against the
// same package-level flag vars (a cobra command has exactly one parent, so the
// alias must be a distinct command sharing the operation, not the *cobra.Command).
func bindInitFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
	cmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "Skip interactive prompts (use defaults and flags)")
	cmd.Flags().BoolVar(&initSkipLaunch, "skip-launch", false, "Skip auto-launching the AI after init")
	cmd.Flags().StringVar(&initEngine, "engine", "", "Pre-select AI engine (claude-code, antigravity, etc.)")
	cmd.Flags().StringArrayVar(&initRemotes, "remote", nil, "Personal ctxloom repo to add as a trusted remote — its bundle changes apply without review (owner/repo or URL); repeatable")
	cmd.Flags().StringVar(&initForge, "forge", "", "Bind every --remote to this forge (github, git, or a configured forges: label) instead of resolving by URL host")
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// stdinIsPiped reports whether stdin is NOT a terminal — i.e. content is being
// piped or redirected in. Used to decide when a command should read its task
// from stdin (e.g. `… | ctxloom run --print`).
func stdinIsPiped() bool {
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// stderrIsTerminal reports whether stderr is a terminal. Checked before
// writing terminal escape sequences to stderr so `2>log` captures don't
// collect raw escape bytes.
func stderrIsTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// initPrompts handles interactive user prompts during init.
type initPrompts struct {
	reader   *bufio.Reader
	oldState *term.State
}

func newInitPrompts() *initPrompts {
	return newInitPromptsFrom(os.Stdin)
}

// newInitPromptsFrom is newInitPrompts reading from r instead of os.Stdin
// directly — used after a discovery run, when stdin is owned by the shared
// handoff and must be read through a lease (see stdinHandoff).
func newInitPromptsFrom(r io.Reader) *initPrompts {
	p := &initPrompts{reader: bufio.NewReader(r)}

	// If stdin is a terminal, save state and ensure canonical mode
	// This handles cases where parent process left terminal in raw mode
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err == nil {
			p.oldState = oldState
			// Restore to cooked mode by making raw then restoring
			// This is a workaround since there's no "MakeCooked" function
			_, _ = term.MakeRaw(int(os.Stdin.Fd()))
			if rerr := term.Restore(int(os.Stdin.Fd()), oldState); rerr != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to restore terminal state: %v\n", rerr)
			}
		}
	}

	return p
}

// readCleanLine reads a line and strips terminal escape sequences and control chars.
// This handles focus events (^[[I, ^[[O), cursor movements, etc.
func (p *initPrompts) readCleanLine() (string, error) {
	input, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	// Strip CSI escape sequences, keep only printable ASCII.
	var clean strings.Builder
	for i := 0; i < len(input); {
		if isCSIStart(input, i) {
			i = skipCSISequence(input, i)
			continue
		}
		if isPrintableASCII(input[i]) {
			clean.WriteByte(input[i])
		}
		i++
	}

	return strings.TrimSpace(clean.String()), nil
}

// isCSIStart reports whether a CSI escape (ESC '[') begins at input[i].
func isCSIStart(input string, i int) bool {
	return input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '['
}

// skipCSISequence returns the index just past the CSI sequence starting at i
// (which points at the ESC). CSI sequences end with a final byte 0x40–0x7e.
func skipCSISequence(input string, i int) int {
	i += 2 // past ESC '['
	for i < len(input) {
		c := input[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

// isPrintableASCII reports whether b is a printable ASCII byte.
func isPrintableASCII(b byte) bool {
	return b >= 0x20 && b <= 0x7e
}

// primaryEngines are shown first in the selection menu (curated list).
var primaryEngines = []string{"claude-code", "antigravity"}

// getAvailableEngines returns engines filtered by what's actually installed.
// Primary engines come first, then secondary engines, all sorted.
func getAvailableEngines() (primary, secondary []string) {
	primarySet := make(map[string]bool)
	for _, e := range primaryEngines {
		primarySet[e] = true
	}

	// Check which primary engines are available
	for _, name := range primaryEngines {
		if backends.IsAvailable(name) {
			primary = append(primary, name)
		}
	}

	// Get secondary engines (all others except mock)
	for _, name := range backends.List() {
		if name == "mock" || primarySet[name] {
			continue
		}
		if backends.IsAvailable(name) {
			secondary = append(secondary, name)
		}
	}

	// Sort secondary for consistent ordering
	sort.Strings(secondary)
	return primary, secondary
}

// errNoEngines is returned when no AI engines are installed.
var errNoEngines = fmt.Errorf("no AI engines installed")

// promptEngineSelection prompts the user to select an AI engine.
// Returns the selected engine name, or the default if only one is available.
func (p *initPrompts) promptEngineSelection() (string, error) {
	primary, secondary := getAvailableEngines()

	switch len(primary) + len(secondary) {
	case 0:
		return "", errNoEngines
	case 1:
		return selectSoleEngine(primary, secondary), nil
	}

	maxOption := printEngineMenu(primary, secondary)
	return p.readEngineChoice(primary, secondary, maxOption)
}

// selectSoleEngine returns (and announces) the only available engine. Exactly
// one of primary/secondary is non-empty here (the sole-engine switch case), so
// the primary list is consulted before indexing secondary — indexing secondary
// unconditionally panics when the only engine is a primary one.
func selectSoleEngine(primary, secondary []string) string {
	var engine string
	if len(primary) > 0 {
		engine = primary[0]
	} else {
		engine = secondary[0]
	}
	fmt.Printf("\nUsing %s (only available engine)\n", engine)
	return engine
}

// printEngineMenu prints the numbered engine menu (with a "more options" entry
// when secondary engines exist) and returns the highest valid option number.
func printEngineMenu(primary, secondary []string) int {
	fmt.Println("\nSelect your AI engine (press Enter for recommended):")
	for i, engine := range primary {
		label := engine
		if i == 0 {
			label += " (Recommended)"
		}
		fmt.Printf("  %d) %s\n", i+1, label)
	}

	maxOption := len(primary)
	if len(secondary) > 0 {
		fmt.Printf("  %d) more options...\n", len(primary)+1)
		maxOption++
	}
	return maxOption
}

// readEngineChoice loops on input until a valid selection is made: empty picks
// the recommended (first primary), a primary number picks that engine, and the
// "more options" entry shows the full list.
func (p *initPrompts) readEngineChoice(primary, secondary []string, maxOption int) (string, error) {
	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			return primary[0], nil
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > maxOption {
			fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", maxOption)
			continue
		}

		if num <= len(primary) {
			return primary[num-1], nil
		}
		return p.promptAllEngines(primary, secondary)
	}
}

// promptAllEngines shows all available engines.
func (p *initPrompts) promptAllEngines(primary, secondary []string) (string, error) {
	allEngines := append(primary, secondary...)

	fmt.Println("\nAll installed engines:")
	for i, engine := range allEngines {
		fmt.Printf("  %d) %s\n", i+1, engine)
	}

	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			continue
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > len(allEngines) {
			fmt.Printf("Please enter a number between 1 and %d\n", len(allEngines))
			continue
		}

		return allEngines[num-1], nil
	}
}

// promptPersonalRepos optionally asks for one or more personal ctxloom repos.
// Returns the repos in entry order; an empty slice if the user has none.
// The trust consequence is stated BEFORE entry, not after: adding a repo here
// marks it trusted, and the user must know that while deciding what to type.
func (p *initPrompts) promptPersonalRepos() ([]string, error) {
	fmt.Print("\nDo you have any personal ctxloom repositories? (y/N): ")
	input, err := p.readCleanLine()
	if err != nil {
		return nil, err
	}

	input = strings.ToLower(input)
	if input != "y" && input != "yes" {
		return nil, nil
	}

	fmt.Println("Repos you add here are marked TRUSTED: their bundle changes apply on pull")
	fmt.Println("without a review prompt. Only add repos you control or trust; revoke any")
	fmt.Println("time with `ctxloom remote untrust <name>`.")
	fmt.Println("Enter GitHub repos (e.g., 'myuser/ctxloom-profiles'), one per line. Blank line when done.")
	var repos []string
	for {
		fmt.Printf("  repo %d (blank to finish): ", len(repos)+1)
		repo, err := p.readCleanLine()
		if err != nil {
			return repos, err
		}
		if repo == "" {
			break
		}
		repos = append(repos, repo)
	}

	return repos, nil
}

// generateConfig creates a config.yaml with the selected engine and options.

// profileDiscoveryPrompt is the prompt sent to the AI to help discover profiles.
// It uses only the real ctxloom surface: the search_library MCP tool (which
// reads the local clones init just made), the ctxloom://remotes resource, and
// CLI install commands. Do not reference list_remotes / browse_remote /
// list_profiles / create_profile / update_profile — those are not MCP tools.
var profileDiscoveryPrompt = resources.MustGetPromptText("profile-discovery")

// launchEngineWithPrompt starts the AI with the profile discovery prompt.
// Errors (failed launch, errored session) are returned for the caller to
// degrade on — init never fails because of them, but a clean return is the
// signal that offering a relaunch into `ctxloom run` is safe.
func launchEngineWithPrompt(ctx context.Context, engine, workDir string) error {
	client, err := pb.NewSelfInvokingClient(engine, 0)
	if err != nil {
		return fmt.Errorf("failed to launch %s: %w", engine, err)
	}
	defer client.Kill()

	req := &pb.RunStart{
		Prompt: &pb.Fragment{Content: profileDiscoveryPrompt},
		Options: &pb.RunOptions{
			WorkDir:     workDir,
			AutoApprove: true,
			Mode:        pb.ExecutionMode_INTERACTIVE,
		},
	}

	// The discovery session is interactive, so the frontend must own the
	// terminal exactly as `ctxloom run` does: raw-mode keystrokes and resize
	// events are pumped over the bidi Run stream into the agent's pty (the
	// plugin subprocess never inherits our terminal). Off a TTY this degrades
	// to a non-interactive stream — warn and continue (CLAUDE.md).
	stdin, resize, restoreTerm := interactiveTerminal(ctx)
	defer restoreTerm()
	if stdin == nil {
		fmt.Fprintln(os.Stderr, "ctxloom: warning: stdin is not a terminal; discovery session will not accept input")
	}

	// Unlike one-shot `ctxloom run`, init keeps prompting on stdin after this
	// run ends (offerSessionRelaunch). The client's stdin pump would otherwise
	// stay parked in os.Stdin.Read and swallow the user's first answer line,
	// so the pump reads through a detachable lease on the shared handoff and
	// is detached as soon as the run returns — any byte it had in flight is
	// then delivered to the relaunch prompt instead of being lost.
	var runStdin io.Reader
	if stdin != nil {
		lease := sharedStdinHandoff().Attach()
		defer lease.Detach()
		runStdin = lease
	}

	// Restore the terminal before dying on an interrupt delivered from
	// outside (in raw mode a user's ^C is just bytes forwarded to the agent,
	// not a SIGINT to us). restoreTerm is idempotent, so this races safely
	// with the deferred and inline calls.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		restoreTerm()
		// Re-raise signal for default handling
		signal.Stop(sigCh)
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}()
	defer signal.Stop(sigCh)

	_, err = client.Run(ctx, req, runStdin, os.Stdout, os.Stderr, resize)
	restoreTerm()
	if err != nil {
		return fmt.Errorf("AI session ended: %w", err)
	}

	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	appDir, err := resolveAppDir(initHome)
	if err != nil {
		return err
	}

	alreadyExists := ctxloomDirExists(appDir)
	if alreadyExists {
		fmt.Printf("ctxloom directory already exists: %s\n", appDir)
	}

	interactive := isInteractiveTerminal() && !initNonInteractive
	selectedEngine := initEngine
	if alreadyExists {
		selectedEngine = engineFromExistingConfig(selectedEngine)
	} else {
		selectedEngine, err = setupNewCtxloomDir(cmd, appDir, selectedEngine, interactive)
		if err != nil {
			return err
		}
	}

	// Ensure a concrete engine for the launch step (covers existing dirs whose
	// config did not name one).
	primary, _ := getAvailableEngines()
	selectedEngine = pickDefaultEngine(selectedEngine, primary)

	if launchDiscovery(cmd, selectedEngine, appDir, interactive) {
		return offerSessionRelaunch()
	}
	return nil
}

// resolveAppDir returns the .ctxloom directory to operate on: under the user's
// home when home is true, else under the current working directory.
func resolveAppDir(home bool) (string, error) {
	if home {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		return filepath.Join(dir, config.AppDirName), nil
	}
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	return filepath.Join(pwd, config.AppDirName), nil
}

// ctxloomDirExists reports whether appDir already exists as a directory.
func ctxloomDirExists(appDir string) bool {
	info, err := os.Stat(appDir)
	return err == nil && info.IsDir()
}

// engineFromExistingConfig returns the engine recorded in an existing config,
// falling back to current when the config is unreadable or names none.
func engineFromExistingConfig(current string) string {
	if cfg, err := config.Load(); err == nil {
		if backend := cfg.GetDefaultLLM(); backend != "" {
			return backend
		}
	}
	return current
}

// pickDefaultEngine resolves the engine to use: an explicit selection wins;
// otherwise the first available primary engine; otherwise "claude-code" so init
// never dead-ends without an engine.
func pickDefaultEngine(selected string, primary []string) string {
	if selected != "" {
		return selected
	}
	if len(primary) > 0 {
		return primary[0]
	}
	return "claude-code"
}

// setupNewCtxloomDir performs first-time setup for a non-existent .ctxloom dir:
// resolve the engine (with interactive prompts), write the skeleton, register
// personal/discovery remotes, apply hooks, and update .gitignore. Returns the
// resolved engine. Per CLAUDE.md fault tolerance, post-scaffold steps warn and
// continue; only directory/config creation failures are fatal.
func setupNewCtxloomDir(cmd *cobra.Command, appDir, selectedEngine string, interactive bool) (string, error) {
	engine, personalRepos, err := resolveSetupEngine(selectedEngine, interactive)
	if err != nil {
		return "", err
	}

	if err := writeInitialConfig(appDir, engine); err != nil {
		return "", err
	}
	fmt.Printf("Initialized ctxloom directory: %s\n", appDir)
	fmt.Printf("Default AI engine: %s\n", engine)
	fmt.Println("Seeded remote \"ctxloom-default\" (official curated repo, trusted — its bundle")
	fmt.Println("changes apply on pull without review; revoke with: ctxloom remote untrust ctxloom-default)")

	// Remotes from --remote flags are added alongside any the interactive prompt
	// collected, so a fully non-interactive run can still register personal repos.
	addPersonalRemotes(cmd, append(append([]string{}, initRemotes...), personalRepos...), initForge)
	cloneConfiguredRemotes(cmd)
	pullSeededDependencies(cmd)
	applyInitHooks(cmd)

	// Exclude ctxloom's private working state from version control.
	if err := gitignore.Ensure(filepath.Dir(appDir), gitignore.Comment, gitignore.PrivateStatePatterns...); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to update .gitignore: %v\n", err)
	}

	return engine, nil
}

// resolveSetupEngine decides which engine to install with. It warns (but does
// not fail) when no engines are detected, runs the interactive engine/repo
// prompts when applicable, and finally falls back to the first available
// primary engine. Returns errNoEngines only when the interactive selection
// reports none installed.
func resolveSetupEngine(selected string, interactive bool) (engine string, repos []string, err error) {
	if selected == "" && noEnginesInstalled() {
		warnNoEnginesDetected()
		selected = "claude-code"
	}

	if interactive && selected == "" {
		selected, repos, err = promptForEngineAndRepos()
		if err != nil {
			return "", nil, err
		}
	}

	primary, _ := getAvailableEngines()
	return pickDefaultEngine(selected, primary), repos, nil
}

// noEnginesInstalled reports whether neither a primary nor a secondary engine
// is available.
func noEnginesInstalled() bool {
	primary, secondary := getAvailableEngines()
	return len(primary) == 0 && len(secondary) == 0
}

// warnNoEnginesDetected prints install guidance to stderr. Init continues with a
// placeholder engine (fault tolerant).
func warnNoEnginesDetected() {
	fmt.Fprintln(os.Stderr, "ctxloom: warning: no AI engines detected")
	fmt.Fprintln(os.Stderr, "Install one of the following to use ctxloom:")
	fmt.Fprintln(os.Stderr, "  claude-code:  npm install -g @anthropic-ai/claude-code")
	fmt.Fprintln(os.Stderr, "  antigravity:  curl -fsSL https://antigravity.google/cli/install.sh | bash")
	fmt.Fprintln(os.Stderr, "  codex:        npm install -g @openai/codex")
	fmt.Fprintln(os.Stderr, "")
}

// promptForEngineAndRepos runs the interactive engine selection and optional
// personal-repo prompts. errNoEngines propagates (the prompt already explained
// it); other prompt failures warn and fall back rather than aborting init.
func promptForEngineAndRepos() (engine string, repos []string, err error) {
	prompts := newInitPrompts()

	engine, err = prompts.promptEngineSelection()
	if err != nil {
		if err == errNoEngines {
			return "", nil, err
		}
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to read engine selection: %v\n", err)
		engine = "claude-code"
	}

	repos, repoErr := prompts.promptPersonalRepos()
	if repoErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to read repo selection: %v\n", repoErr)
		repos = nil
	}
	return engine, repos, nil
}

// writeInitialConfig delegates project bootstrap (the .ctxloom skeleton +
// config.yaml + default remotes.yaml) to the operations core.
func writeInitialConfig(appDir, engine string) error {
	_, err := operations.InitializeProject(context.Background(), operations.InitializeProjectRequest{
		AppDir: appDir,
		Engine: engine,
	})
	return err
}

// personalRemoteRequests builds the AddRemote requests for the user's personal
// repos. The first is named "personal"; subsequent ones get "personal-2",
// "personal-3", … so each is a distinct, addressable remote. Personal repos are
// the user's own, so they are trusted by default. A non-empty forge binds every
// remote to that forge (github, git, or a configured label) instead of letting
// resolution fall back to URL-host matching.
func personalRemoteRequests(repos []string, forge string) []operations.AddRemoteRequest {
	reqs := make([]operations.AddRemoteRequest, 0, len(repos))
	for i, repo := range repos {
		name := "personal"
		if i > 0 {
			name = fmt.Sprintf("personal-%d", i+1)
		}
		reqs = append(reqs, operations.AddRemoteRequest{Name: name, URL: repo, Trust: true, Forge: forge})
	}
	return reqs
}

// addPersonalRemotes registers the user's personal repos. Failures warn and
// continue (an unknown --forge label rolls that single remote back). Trust is
// visible in the output and revokable via `ctxloom remote untrust`.
func addPersonalRemotes(cmd *cobra.Command, repos []string, forge string) {
	if len(repos) == 0 {
		return
	}
	cfg, loadErr := config.Load()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for remote: %v\n", loadErr)
		return
	}
	for _, req := range personalRemoteRequests(repos, forge) {
		if _, addErr := operations.AddRemote(cmd.Context(), cfg, req); addErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to add remote %q (%s): %v\n", req.Name, req.URL, addErr)
		} else {
			fmt.Printf("Added remote %q: %s (trusted — revoke with: ctxloom remote untrust %s)\n", req.Name, req.URL, req.Name)
		}
	}
}

// cloneConfiguredRemotes eagerly clones every configured remote so discovery
// (search_library, browse) can read them offline. Fault-tolerant: per-remote
// failures warn and continue.
func cloneConfiguredRemotes(cmd *cobra.Command) {
	cfg, loadErr := config.Load()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for cloning remotes: %v\n", loadErr)
		return
	}
	cloneRes, cloneErr := operations.EnsureRemoteClones(cmd.Context(), cfg)
	if cloneErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to clone remotes: %v\n", cloneErr)
		return
	}
	if len(cloneRes.Cloned) > 0 {
		fmt.Printf("Cloned remotes for discovery: %s\n", strings.Join(cloneRes.Cloned, ", "))
	}
}

// pullSeededDependencies pulls and locks the remote dependencies the fresh
// config references — most importantly the seeded default profile, which
// resolves only through a lockfile entry. Without this step the very first
// `ctxloom run` after init fails to assemble. Fault-tolerant: failures warn
// and continue (offline init still completes; run degrades per assembly).
func pullSeededDependencies(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for dependency pull: %v\n", err)
		return
	}
	result, syncErr := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
		Lock:       true,
		ApplyHooks: false, // applyInitHooks runs right after
	})
	if syncErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to pull seeded dependencies: %v\n", syncErr)
		return
	}
	if result.Installed > 0 {
		fmt.Printf("Pulled %d seeded dependencies\n", result.Installed)
	}
}

// applyInitHooks registers the ctxloom MCP server with every backend. Failures
// warn and continue (fault tolerant).
func applyInitHooks(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config: %v\n", err)
		return
	}
	result, applyErr := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: false,
	})
	if applyErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to apply hooks: %v\n", applyErr)
		return
	}
	fmt.Printf("Applied hooks for: %v\n", result.Backends)
}

// launchDiscovery auto-launches the engine with a profile-discovery prompt in
// interactive mode (unless --skip-launch). A launch failure warns, never fatal.
// Returns true only when a discovery session ran and ended cleanly — the
// signal that offering a relaunch into `ctxloom run` is safe; an errored
// session must not chain into a possibly-broken setup.
func launchDiscovery(cmd *cobra.Command, engine, appDir string, interactive bool) bool {
	if !interactive || initSkipLaunch {
		return false
	}
	fmt.Printf("\nLaunching %s to help you discover profiles...\n", engine)
	fmt.Println("(Exit the AI session when done — ctxloom will then offer to start your configured session)")
	fmt.Println()

	workDir := filepath.Dir(appDir)
	if launchErr := launchEngineWithPrompt(cmd.Context(), engine, workDir); launchErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %v\n", launchErr)
		return false
	}
	return true
}

// wantsRelaunch interprets the answer to the post-discovery relaunch prompt.
// Empty input means yes (the default); only an explicit n/no declines, so a
// typo lands the user in their session rather than silently back at the shell.
func wantsRelaunch(input string) bool {
	answer := strings.ToLower(strings.TrimSpace(input))
	return answer != "n" && answer != "no"
}

// printRunHint tells the user how to pick up the new configuration later.
func printRunHint() {
	fmt.Println("Run `ctxloom run` when ready — it picks up everything init installed.")
}

// offerSessionRelaunch asks whether to start the configured session now and,
// on yes, runs `ctxloom run` as a child on this terminal. Discovery installs
// profiles, hooks, and MCP servers that the discovery session itself cannot
// see; a fresh run assembles them into the launch config. Declining — or any
// failure on the way to the launch — degrades to a hint (fault tolerant);
// only the child session's own exit code propagates, as ExitError, matching
// `ctxloom run` itself.
func offerSessionRelaunch() error {
	fmt.Print("\nStart your session now to pick up the new configuration? (Y/n): ")
	// Read through the shared stdin handoff: the discovery run's stdin pump
	// read through it too, so a byte it had in flight when the run ended is
	// delivered here rather than lost (see stdinHandoff). The lease is
	// detached before the relaunch so the child owns os.Stdin uncontested —
	// demand-driven reads leave no fd read outstanding once the answer line
	// is consumed.
	lease := sharedStdinHandoff().Attach()
	input, err := newInitPromptsFrom(lease).readCleanLine()
	lease.Detach()
	if err != nil || !wantsRelaunch(input) {
		printRunHint()
		return nil
	}

	run := exec.Command(resolveSelfExecutable(), "run")
	run.Stdin = os.Stdin
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if runErr := run.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return &ExitError{Code: childExitCode(exitErr)}
		}
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to start session: %v\n", runErr)
		printRunHint()
	}
	return nil
}
