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
	"github.com/ctxloom/ctxloom/resources"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .scm directory",
	Annotations: map[string]string{
		AnnotationLLMWrapper: "true",
	},
	Long: `Initialize a new .scm directory in the current working directory.

This creates a marker directory that SCM uses to identify a project root.
All SCM data (profiles, bundles, fragments, prompts) will be stored here.

If no .scm directory exists when running SCM commands, the user home ~/.scm
is used as a fallback.

When run interactively (TTY detected), init will guide you through:
  1. Selecting an AI engine (claude-code, gemini, etc.)
  2. Optionally adding a personal SCM repository as a remote
  3. Launching your AI to help discover and configure profiles

Examples:
  ctxloom init                     # Interactive setup (if TTY)
  ctxloom init --home              # Initialize in ~/.scm
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

// ensureGitignoreEntry adds SCM memory directory to .gitignore if not already present.
// This keeps session logs local (machine-specific, potentially large).
func ensureGitignoreEntry(projectDir string) error {
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	memoryEntry := ".ctxloom/memory/"
	comment := "# SCM memory (local session data, not shared)"

	// Read existing .gitignore if it exists
	var lines []string
	content, err := os.ReadFile(gitignorePath)
	if err == nil {
		lines = strings.Split(string(content), "\n")
		// Check if entry already exists
		for _, line := range lines {
			if strings.TrimSpace(line) == memoryEntry {
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
	defer f.Close()

	// Add newline if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	// Write comment and entry
	if _, err := f.WriteString(fmt.Sprintf("\n%s\n%s\n", comment, memoryEntry)); err != nil {
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

// promptPersonalRepo optionally asks for a personal SCM GitHub repo.
func (p *initPrompts) promptPersonalRepo() (string, error) {
	fmt.Print("\nDo you have a personal SCM repository? (y/N): ")
	input, err := p.readCleanLine()
	if err != nil {
		return "", err
	}

	input = strings.ToLower(input)
	if input != "y" && input != "yes" {
		return "", nil
	}

	fmt.Print("Enter GitHub repo (e.g., 'myuser/scm-profiles'): ")
	repo, err := p.readCleanLine()
	if err != nil {
		return "", err
	}

	return repo, nil
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
  auto_register_scm: true
`, engine, engine))
}

// profileDiscoveryPrompt is the prompt sent to the AI to help discover profiles.
const profileDiscoveryPrompt = `Welcome to SCM! I'll help you discover and set up context profiles, fragments, and prompts for your development workflow.

**First, scan the current directory** for project indicators like:
- go.mod, Cargo.toml, package.json, pyproject.toml, requirements.txt
- Dockerfile, docker-compose.yml, Makefile, justfile
- .github/, .gitlab-ci.yml, and other CI/CD configs
- Framework-specific files (next.config.js, vite.config.ts, etc.)

Based on what you find, suggest matching content from **all configured remotes**.

**Tools to use:**
- Use Bash/Glob to scan the directory structure
- Use list_remotes to see all configured remotes
- Use search_remotes to find matching profiles, bundles (fragments/prompts) across ALL remotes
  - Search by tags: "tag:golang", "tag:react", "tag:docker"
  - Search by text: "security", "testing", "ci-cd"
- Use browse_remote to explore specific remotes in detail

**After scanning**, present your findings:
1. What project type/stack you detected
2. Matching content from each remote:
   - **Profiles**: Development workflow configurations
   - **Bundles**: Collections of fragments (context) and prompts (reusable commands)
3. Ask the user which items to install

**Example workflow:**
1. Detect go.mod → search for "tag:golang" across all remotes
2. Detect Dockerfile → search for "tag:docker" and "tag:container"
3. Present all matches grouped by remote, let user choose

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
			fmt.Fprintf(os.Stderr, "SCM: warning: failed to save terminal state: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "SCM: warning: failed to launch %s: %v\n", engine, err)
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
		fmt.Fprintf(os.Stderr, "SCM: warning: AI session ended: %v\n", err)
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
		fmt.Printf("SCM directory already exists: %s\n", appDir)
	}

	// Determine if interactive mode
	interactive := isInteractiveTerminal() && !initNonInteractive

	// Determine selected engine
	selectedEngine := initEngine
	var personalRepo string

	// If directory already exists, get engine from existing config
	if alreadyExists {
		cfg, err := config.Load()
		if err == nil && cfg.Defaults.LLMPlugin != "" {
			selectedEngine = cfg.Defaults.LLMPlugin
		}
	}

	// Only do setup if directory doesn't exist
	if !alreadyExists {
		// Check engine availability
		if selectedEngine == "" {
			primary, secondary := getAvailableEngines()
			if len(primary) == 0 && len(secondary) == 0 {
				fmt.Fprintln(os.Stderr, "No AI engines detected.")
				fmt.Fprintln(os.Stderr, "")
				fmt.Fprintln(os.Stderr, "Install one of the following to get started:")
				fmt.Fprintln(os.Stderr, "  claude-code:  npm install -g @anthropic-ai/claude-code")
				fmt.Fprintln(os.Stderr, "  gemini:       pip install google-gemini-cli")
				fmt.Fprintln(os.Stderr, "")
				fmt.Fprintln(os.Stderr, "Then run 'ctxloom init' again.")
				return errNoEngines
			}
		}

		if interactive && selectedEngine == "" {
			prompts := newInitPrompts()

			// 1. Engine selection
			engine, err := prompts.promptEngineSelection()
			if err != nil {
				if err == errNoEngines {
					return err // Already printed message above
				}
				fmt.Fprintf(os.Stderr, "SCM: warning: failed to read engine selection: %v\n", err)
				selectedEngine = "claude-code" // fallback
			} else {
				selectedEngine = engine
			}

			// 2. Personal repo (optional)
			repo, err := prompts.promptPersonalRepo()
			if err != nil {
				fmt.Fprintf(os.Stderr, "SCM: warning: failed to read repo selection: %v\n", err)
			} else {
				personalRepo = repo
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
			filepath.Join(appDir, "profiles"),
			filepath.Join(appDir, "bundles"),
		}

		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}

		// Create config.yaml with selected engine and options
		configPath := filepath.Join(appDir, "config.yaml")
		configContent := generateConfig(selectedEngine)
		if err := os.WriteFile(configPath, configContent, 0644); err != nil {
			return fmt.Errorf("failed to create config.yaml: %w", err)
		}

		// Create remotes.yaml with default remote (ctxloom-default)
		remotesPath := filepath.Join(appDir, "remotes.yaml")
		remotesContent, err := resources.GetDefaultRemotes()
		if err != nil {
			return fmt.Errorf("failed to read default remotes: %w", err)
		}
		if err := os.WriteFile(remotesPath, remotesContent, 0644); err != nil {
			return fmt.Errorf("failed to create remotes.yaml: %w", err)
		}

		fmt.Printf("Initialized SCM directory: %s\n", appDir)
		fmt.Printf("Default AI engine: %s\n", selectedEngine)

		// Add personal remote if provided
		if personalRepo != "" {
			cfg, loadErr := config.Load()
			if loadErr != nil {
				fmt.Fprintf(os.Stderr, "SCM: warning: failed to load config for remote: %v\n", loadErr)
			} else {
				_, addErr := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
					Name: "personal",
					URL:  personalRepo,
				})
				if addErr != nil {
					fmt.Fprintf(os.Stderr, "SCM: warning: failed to add personal remote: %v\n", addErr)
				} else {
					fmt.Printf("Added personal remote: %s\n", personalRepo)
				}
			}
		}

		// Apply hooks to register MCP server
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "SCM: warning: failed to load config: %v\n", err)
		} else {
			result, applyErr := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
				Backend:           "all",
				RegenerateContext: false,
			})
			if applyErr != nil {
				fmt.Fprintf(os.Stderr, "SCM: warning: failed to apply hooks: %v\n", applyErr)
			} else {
				fmt.Printf("Applied hooks for: %v\n", result.Backends)
			}
		}

		// Update .gitignore to exclude .ctxloom/memory/ (session logs)
		if err := ensureGitignoreEntry(filepath.Dir(appDir)); err != nil {
			fmt.Fprintf(os.Stderr, "SCM: warning: failed to update .gitignore: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "SCM: warning: %v\n", launchErr)
		}
	}

	return nil
}
