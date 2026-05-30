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
	"github.com/ctxloom/ctxloom/internal/paths"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/resources"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .ctxloom directory",
	Annotations: map[string]string{
		AnnotationLLMWrapper: "true",
	},
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

	// Strip escape sequences (CSI sequences: ESC [ ... letter)
	// Pattern: \x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]
	var clean strings.Builder
	i := 0
	for i < len(input) {
		if input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '[' {
			// Skip CSI sequence
			i += 2
			for i < len(input) {
				c := input[i]
				i++
				// CSI sequence ends with a letter (0x40-0x7e)
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			continue
		}
		// Keep only printable ASCII and basic whitespace
		if input[i] >= 0x20 && input[i] <= 0x7e {
			clean.WriteByte(input[i])
		}
		i++
	}

	return strings.TrimSpace(clean.String()), nil
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
	totalEngines := len(primary) + len(secondary)

	// If no engines available, abort with instructions
	if totalEngines == 0 {
		return "", errNoEngines
	}

	// If only one engine is available, use it without prompting
	if totalEngines == 1 {
		if len(primary) > 0 {
			fmt.Printf("\nUsing %s (only available engine)\n", primary[0])
			return primary[0], nil
		}
		fmt.Printf("\nUsing %s (only available engine)\n", secondary[0])
		return secondary[0], nil
	}

	// Show selection menu
	fmt.Println("\nSelect your AI engine (press Enter for recommended):")
	for i, engine := range primary {
		label := engine
		if i == 0 {
			label += " (Recommended)"
		}
		fmt.Printf("  %d) %s\n", i+1, label)
	}

	// Show "more options" only if there are secondary engines
	hasMoreOptions := len(secondary) > 0
	if hasMoreOptions {
		fmt.Printf("  %d) more options...\n", len(primary)+1)
	}

	maxOption := len(primary)
	if hasMoreOptions {
		maxOption++
	}

	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		// Empty input = use recommended (first primary)
		if input == "" {
			return primary[0], nil
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > maxOption {
			fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", maxOption)
			continue
		}

		// Primary engine selected
		if num <= len(primary) {
			return primary[num-1], nil
		}

		// "more options" selected - show all engines
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
func generateConfig(engine string) []byte {
	return []byte(fmt.Sprintf(`# ctxloom Configuration
# See https://github.com/ctxloom/ctxloom for documentation

# Language model plugin configuration
llm:
  plugins:
    %s: {}

# Default settings
defaults:
  llm_plugin: %s
  use_distilled: true

# MCP server configuration
mcp:
  auto_register_ctxloom: true
`, engine, engine))
}

// profileDiscoveryPrompt is the prompt sent to the AI to help discover profiles.
// It uses only the real ctxloom surface: the search_remotes MCP tool (which
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
- Use the **search_remotes** MCP tool to find matching bundles/profiles across
  ALL remotes. It reads the local clones (no network).
  - Search by tag: ` + "`tag:golang`" + `, ` + "`tag:react`" + `, ` + "`tag:docker`" + `
  - Search by text: ` + "`security`" + `, ` + "`testing`" + `, ` + "`ci-cd`" + `
  - Optionally pass item_type ("bundle" or "profile") to narrow.
  - Each result carries a ` + "`pull_ref`" + ` (e.g. ` + "`ctxloom-default/go-developer`" + `) — that is what you install.
- ` + "`search_content`" + ` is for content ALREADY installed in this project; it does
  NOT reach remotes. Use search_remotes for discovery.

**After scanning**, present your findings:
1. What project type/stack you detected
2. Matching content (grouped by remote):
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and prompts (reusable commands)
3. Ask the user which items to install

**Example workflow:**
1. Detect go.mod → search_remotes with query "tag:golang"
2. Detect Dockerfile → search_remotes with "tag:docker" and "tag:container"
3. Present matches grouped by remote, let the user choose

**Install selected items** with the CLI, then sync:
- Profile:  ` + "`ctxloom profile install <pull_ref>`" + ` (e.g. ` + "`ctxloom profile install ctxloom-default/go-developer`" + `)
- Bundle/fragment/prompt:  ` + "`ctxloom install <pull_ref>`" + `
- Then call the **sync_dependencies** MCP tool so every bundle a profile depends
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
	var appDir string

	if initHome {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		appDir = filepath.Join(home, config.AppDirName)
	} else {
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		appDir = filepath.Join(pwd, config.AppDirName)
	}

	// Check if already exists
	alreadyExists := false
	if info, err := os.Stat(appDir); err == nil && info.IsDir() {
		alreadyExists = true
		fmt.Printf("ctxloom directory already exists: %s\n", appDir)
	}

	// Determine if interactive mode
	interactive := isInteractiveTerminal() && !initNonInteractive

	// Determine selected engine
	selectedEngine := initEngine
	var personalRepos []string

	// If directory already exists, get engine from existing config
	if alreadyExists {
		cfg, err := config.Load()
		if err == nil && cfg.Defaults.LLMPlugin != "" {
			selectedEngine = cfg.Defaults.LLMPlugin
		}
	}

	// Only do setup if directory doesn't exist
	if !alreadyExists {
		// Check engine availability (warn but don't fail - fault tolerant)
		noEnginesAvailable := false
		if selectedEngine == "" {
			primary, secondary := getAvailableEngines()
			if len(primary) == 0 && len(secondary) == 0 {
				noEnginesAvailable = true
				fmt.Fprintln(os.Stderr, "ctxloom: warning: no AI engines detected")
				fmt.Fprintln(os.Stderr, "Install one of the following to use ctxloom:")
				fmt.Fprintln(os.Stderr, "  claude-code:  npm install -g @anthropic-ai/claude-code")
				fmt.Fprintln(os.Stderr, "  gemini:       pip install google-gemini-cli")
				fmt.Fprintln(os.Stderr, "")
				// Continue with placeholder - don't fail init
				selectedEngine = "claude-code"
			}
		}
		_ = noEnginesAvailable // used for skipping interactive prompts

		if interactive && selectedEngine == "" {
			prompts := newInitPrompts()

			// 1. Engine selection
			engine, err := prompts.promptEngineSelection()
			if err != nil {
				if err == errNoEngines {
					return err // Already printed message above
				}
				fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to read engine selection: %v\n", err)
				selectedEngine = "claude-code" // fallback
			} else {
				selectedEngine = engine
			}

			// 2. Personal repos (optional, may be several)
			repos, err := prompts.promptPersonalRepos()
			if err != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to read repo selection: %v\n", err)
			} else {
				personalRepos = repos
			}
		}

		// Default to first available engine if not selected
		if selectedEngine == "" {
			primary, _ := getAvailableEngines()
			if len(primary) > 0 {
				selectedEngine = primary[0]
			} else {
				selectedEngine = "claude-code" // shouldn't reach here due to check above
			}
		}

		// Create the directory structure
		dirs := []string{
			appDir,
			filepath.Join(appDir, paths.ProfilesDir),
			paths.BundlesPath(appDir),
		}

		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}

		// Create config.yaml with selected engine and options
		configPath := paths.ConfigPath(appDir)
		configContent := generateConfig(selectedEngine)
		if err := os.WriteFile(configPath, configContent, 0644); err != nil {
			return fmt.Errorf("failed to create config.yaml: %w", err)
		}

		// Create remotes.yaml with default remote (ctxloom-default)
		remotesPath := paths.RemotesPath(appDir)
		remotesContent, err := resources.GetDefaultRemotes()
		if err != nil {
			return fmt.Errorf("failed to read default remotes: %w", err)
		}
		if err := os.WriteFile(remotesPath, remotesContent, 0644); err != nil {
			return fmt.Errorf("failed to create remotes.yaml: %w", err)
		}

		fmt.Printf("Initialized ctxloom directory: %s\n", appDir)
		fmt.Printf("Default AI engine: %s\n", selectedEngine)

		// Add personal remotes if provided. The first is named "personal";
		// subsequent ones get "personal-2", "personal-3", … so each is a
		// distinct, addressable remote.
		if len(personalRepos) > 0 {
			cfg, loadErr := config.Load()
			if loadErr != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for remote: %v\n", loadErr)
			} else {
				for i, repo := range personalRepos {
					name := "personal"
					if i > 0 {
						name = fmt.Sprintf("personal-%d", i+1)
					}
					_, addErr := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
						Name: name,
						URL:  repo,
					})
					if addErr != nil {
						fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to add remote %q (%s): %v\n", name, repo, addErr)
					} else {
						fmt.Printf("Added remote %q: %s\n", name, repo)
					}
				}
			}
		}

		// Eagerly clone every configured remote so discovery (search_remotes,
		// browse) can read them offline. Fault-tolerant: per-remote failures
		// warn and continue.
		if cfg, loadErr := config.Load(); loadErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config for cloning remotes: %v\n", loadErr)
		} else if cloneRes, cloneErr := operations.EnsureRemoteClones(cmd.Context(), cfg); cloneErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to clone remotes: %v\n", cloneErr)
		} else if len(cloneRes.Cloned) > 0 {
			fmt.Printf("Cloned remotes for discovery: %s\n", strings.Join(cloneRes.Cloned, ", "))
		}

		// Apply hooks to register MCP server
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config: %v\n", err)
		} else {
			result, applyErr := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
				Backend:           "all",
				RegenerateContext: false,
			})
			if applyErr != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to apply hooks: %v\n", applyErr)
			} else {
				fmt.Printf("Applied hooks for: %v\n", result.Backends)
			}
		}

		// Update .gitignore to exclude .ctxloom/ephemeral/ (synced bundles, session data)
		if err := ensureGitignoreEntry(filepath.Dir(appDir)); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to update .gitignore: %v\n", err)
		}
	}

	// Default to first available engine if still not set (for existing dirs without config)
	if selectedEngine == "" {
		primary, _ := getAvailableEngines()
		if len(primary) > 0 {
			selectedEngine = primary[0]
		} else {
			selectedEngine = "claude-code"
		}
	}

	// Auto-launch AI with profile discovery prompt (interactive only)
	if interactive && !initSkipLaunch {
		if alreadyExists {
			fmt.Printf("\nLaunching %s to help you discover profiles...\n", selectedEngine)
		} else {
			fmt.Printf("\nLaunching %s to help you discover profiles...\n", selectedEngine)
		}
		fmt.Println("(Use Ctrl+C to exit the AI session when done)")
		fmt.Println()

		workDir := filepath.Dir(appDir)
		if launchErr := launchEngineWithPrompt(cmd.Context(), selectedEngine, workDir); launchErr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: %v\n", launchErr)
		}
	}

	return nil
}
