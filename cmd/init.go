package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
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
  1. Selecting an AI engine (claude-code, gemini, etc.)
  2. Optionally adding a personal ctxloom repository as a remote
  3. Launching your AI to help discover and configure profiles

Examples:
  ctxloom init                     # Interactive setup (if TTY)
  ctxloom init --home              # Initialize in ~/.ctxloom
  ctxloom init --engine gemini     # Pre-select engine
  ctxloom init --non-interactive   # Skip all prompts`,
	RunE: runInit,
}

var (
	initHome           bool
	initNonInteractive bool
	initSkipLaunch     bool
	initEngine         string
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "Skip interactive prompts (use defaults)")
	initCmd.Flags().BoolVar(&initSkipLaunch, "skip-launch", false, "Skip auto-launching the AI after init")
	initCmd.Flags().StringVar(&initEngine, "engine", "", "Pre-select AI engine (claude-code, gemini, aider, etc.)")
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// ensureGitignoreEntry adds ctxloom ephemeral directory to .gitignore if not already present.
// This keeps ephemeral data local (synced bundles, session data, context files).
func ensureGitignoreEntry(projectDir string) error {
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	ephemeralEntry := ".ctxloom/ephemeral/"
	comment := "# ctxloom ephemeral data (synced bundles, session data, context files)"

	// Read existing .gitignore if it exists
	var lines []string
	content, err := os.ReadFile(gitignorePath)
	if err == nil {
		lines = strings.Split(string(content), "\n")
		// Check if entry already exists
		for _, line := range lines {
			if strings.TrimSpace(line) == ephemeralEntry {
				return nil // Already present
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// Append the entry
	f, err := os.OpenFile(gitignorePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Add newline if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	// Write comment and entry
	if _, err := fmt.Fprintf(f, "\n%s\n%s\n", comment, ephemeralEntry); err != nil {
		return err
	}

	return nil
}

// initPrompts handles interactive user prompts during init.
type initPrompts struct {
	reader   *bufio.Reader
	oldState *term.State
}

func newInitPrompts() *initPrompts {
	p := &initPrompts{reader: bufio.NewReader(os.Stdin)}

	// If stdin is a terminal, save state and ensure canonical mode
	// This handles cases where parent process left terminal in raw mode
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err == nil {
			p.oldState = oldState
			// Restore to cooked mode by making raw then restoring
			// This is a workaround since there's no "MakeCooked" function
			_, _ = term.MakeRaw(int(os.Stdin.Fd()))
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
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
var primaryEngines = []string{"claude-code", "gemini"}

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

// selectSoleEngine returns (and announces) the only available engine.
func selectSoleEngine(primary, secondary []string) string {
	engine := secondary[0]
	if len(primary) > 0 {
		engine = primary[0]
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
const profileDiscoveryPrompt = `Welcome to ctxloom! I'll help you discover and set up context profiles, fragments, and prompts for your development workflow.

**First, scan the current directory** for project indicators like:
- go.mod, Cargo.toml, package.json, pyproject.toml, requirements.txt
- Dockerfile, docker-compose.yml, Makefile, justfile
- .github/, .gitlab-ci.yml, and other CI/CD configs
- Framework-specific files (next.config.js, vite.config.ts, etc.)

**Surface (read this first):**
- The configured remotes have already been cloned locally during init. Read the
  ` + "`ctxloom://remotes`" + ` MCP resource to see them.
- Use the **search_library** MCP tool to find matching bundles/profiles across
  ALL remotes. It reads the local clones (no network).
  - Search by tag: ` + "`tag:golang`" + `, ` + "`tag:react`" + `, ` + "`tag:docker`" + `
  - Search by text: ` + "`security`" + `, ` + "`testing`" + `, ` + "`ci-cd`" + `
  - Optionally pass item_type ("bundle" or "profile") to narrow.
  - Each result carries a ` + "`pull_ref`" + ` (e.g. ` + "`ctxloom-default/go-developer`" + `) — that is what you install.
- ` + "`search_content`" + ` is for content ALREADY installed in this project; it does
  NOT reach remotes. Use search_library for discovery.

**After scanning**, present your findings:
1. What project type/stack you detected
2. Matching content (grouped by remote):
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and prompts (reusable commands)
3. Ask the user which items to install

**Example workflow:**
1. Detect go.mod → search_library with query "tag:golang"
2. Detect Dockerfile → search_library with "tag:docker" and "tag:container"
3. Present matches grouped by remote, let the user choose

**Install selected items** with the CLI, then sync:
- Profile:  ` + "`ctxloom profile install <pull_ref>`" + ` (e.g. ` + "`ctxloom profile install ctxloom-default/go-developer`" + `)
- Bundle/fragment/prompt:  ` + "`ctxloom install <pull_ref>`" + `
- Then run ` + "`ctxloom remote sync`" + ` so every bundle a profile depends
  on is fetched into the cache.
- To pin a content version, append ` + "`@<git-tag-or-sha>`" + ` to the ref
  (e.g. ` + "`ctxloom-default/go-developer@v1.2.0`" + `). Unpinned installs track the
  remote's default branch.

**Defaults:** the first profile you install is promoted into ` + "`defaults.profiles`" + `
in ` + "`.ctxloom/config.yaml`" + ` so ` + "`ctxloom run`" + ` loads it automatically. To make a
different profile the default later, edit ` + "`defaults.profiles`" + ` in that file (or use
the ` + "`ctxloom profile`" + ` subcommands). Confirm the final ` + "`defaults.profiles`" + ` list
with the user before exiting.

If you'd prefer to skip this setup, just say "skip" and configure manually later.`

// launchEngineWithPrompt starts the AI with the profile discovery prompt.
func launchEngineWithPrompt(ctx context.Context, engine, workDir string) error {
	// Save terminal state before launching subprocess
	// This ensures we can restore it even if the subprocess corrupts it
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		var err error
		oldState, err = term.GetState(int(os.Stdin.Fd()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to save terminal state: %v\n", err)
		}
	}

	// Ensure terminal is restored on any exit path
	restoreTerminal := func() {
		if oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}
	defer restoreTerminal()

	// Set up signal handler to restore terminal on interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		restoreTerminal()
		// Re-raise signal for default handling
		signal.Stop(sigCh)
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}()
	defer signal.Stop(sigCh)

	client, err := pb.NewSelfInvokingClient(engine, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to launch %s: %v\n", engine, err)
		return nil // Fault tolerant - don't fail init
	}
	defer client.Kill()

	req := &pb.RunRequest{
		Prompt: &pb.Fragment{Content: profileDiscoveryPrompt},
		Options: &pb.RunOptions{
			WorkDir:     workDir,
			AutoApprove: true,
			Mode:        pb.ExecutionMode_INTERACTIVE,
		},
	}

	_, err = client.Run(ctx, req, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: AI session ended: %v\n", err)
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

	launchDiscovery(cmd, selectedEngine, appDir, interactive)
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
	if cfg, err := config.Load(); err == nil && cfg.LM.Default != "" {
		return cfg.LM.Default
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

	addPersonalRemotes(cmd, personalRepos)
	cloneConfiguredRemotes(cmd)
	applyInitHooks(cmd)

	// Update .gitignore to exclude .ctxloom/ephemeral/ (synced bundles, session data).
	if err := ensureGitignoreEntry(filepath.Dir(appDir)); err != nil {
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
	fmt.Fprintln(os.Stderr, "  gemini:       pip install google-gemini-cli")
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

// addPersonalRemotes registers the user's personal repos. The first is named
// "personal"; subsequent ones get "personal-2", "personal-3", … so each is a
// distinct, addressable remote. Failures warn and continue.
func addPersonalRemotes(cmd *cobra.Command, repos []string) {
	if len(repos) == 0 {
		return
	}
	cfg, loadErr := config.Load()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for remote: %v\n", loadErr)
		return
	}
	for i, repo := range repos {
		name := "personal"
		if i > 0 {
			name = fmt.Sprintf("personal-%d", i+1)
		}
		// Personal repos are the user's own, so trust them by default: their
		// bundle changes auto-apply without review. The trust is visible here and
		// revokable via `ctxloom remote untrust`.
		if _, addErr := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{Name: name, URL: repo, Trust: true}); addErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to add remote %q (%s): %v\n", name, repo, addErr)
		} else {
			fmt.Printf("Added remote %q: %s (trusted — revoke with: ctxloom remote untrust %s)\n", name, repo, name)
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
func launchDiscovery(cmd *cobra.Command, engine, appDir string, interactive bool) {
	if !interactive || initSkipLaunch {
		return
	}
	fmt.Printf("\nLaunching %s to help you discover profiles...\n", engine)
	fmt.Println("(Use Ctrl+C to exit the AI session when done)")
	fmt.Println()

	workDir := filepath.Dir(appDir)
	if launchErr := launchEngineWithPrompt(cmd.Context(), engine, workDir); launchErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %v\n", launchErr)
	}
}
