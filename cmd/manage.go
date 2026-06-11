package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// manageCmd is the home for everything that mutates the project harness:
// scaffolding (.ctxloom), hooks/statusline, MCP registration, command files,
// .gitignore, and config. Runtime entrypoints (`mcp serve`) and machine
// callbacks (all consolidated under the hidden `hook` namespace) deliberately
// stay out of this user-facing namespace.
var manageCmd = &cobra.Command{
	Use:   "manage",
	Short: "Install and manage ctxloom's project harness",
	Long: `Install, inspect, and remove ctxloom's integration with a project.

Everything that writes to the project harness lives here: the .ctxloom
directory, backend hooks and statusline, MCP server registration, generated
command files, .gitignore, and configuration.

  ctxloom manage install      Scaffold and wire ctxloom into this project
  ctxloom manage uninstall    Remove ctxloom's hooks, MCP entry, and commands
  ctxloom manage status       Show what ctxloom has wired in
  ctxloom manage hooks        Install/uninstall/inspect backend hooks
  ctxloom manage mcp          Manage MCP registration and server configs
  ctxloom manage config       Show/edit ctxloom configuration
  ctxloom manage gitignore    Maintain ctxloom's .gitignore entries`,
}

// --- manage init (canonical home of `ctxloom init`) ------------------------

var manageInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .ctxloom directory",
	Long:  initCmd.Long,
	RunE:  runInit,
}

// --- manage install / uninstall / status -----------------------------------

var (
	manageInstallEngine string
	manageInstallPrint  bool
)

var manageInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Scaffold .ctxloom and wire hooks, MCP, gitignore, and config",
	Long: `One-shot, non-interactive setup: scaffold the .ctxloom skeleton (if absent),
exclude ctxloom's private state from git, and apply hooks/MCP/statusline to
every supported backend. Unlike 'manage init', it never launches an AI.`,
	Args: cobra.NoArgs,
	RunE: runManageInstall,
}

var manageUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ctxloom's hooks, statusline, MCP entry, and command files",
	Long: `Strip ctxloom-managed hooks, statusline, MCP servers, and generated command
files from every supported backend. Leaves the .ctxloom directory and its
contents (profiles, bundles, config) untouched.`,
	Args: cobra.NoArgs,
	RunE: runManageUninstall,
}

var manageStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show what ctxloom has wired into this project",
	Args:  cobra.NoArgs,
	RunE:  runManageStatus,
}

// runManageInstall scaffolds and wires ctxloom into the current project. Fault
// tolerant: post-scaffold steps warn and continue so a partial wire still lands
// the user in a working state.
func runManageInstall(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(appDir)

	if manageInstallPrint {
		printInstallPlan(appDir, projectDir)
		return nil
	}

	if !ctxloomDirExists(appDir) {
		if _, err := operations.InitializeProject(cmd.Context(), operations.InitializeProjectRequest{
			AppDir: appDir,
			Engine: manageInstallEngine,
		}); err != nil {
			return err
		}
		fmt.Printf("Initialized ctxloom directory: %s\n", appDir)
	}

	ensureHarnessGitignore(projectDir)

	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.ApplyHooks(cmd.Context(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Hooks %s for: %v\n", result.Status, result.Backends)
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", e)
	}
	return nil
}

// printInstallPlan lists the steps `manage install` would take without running them.
func printInstallPlan(appDir, projectDir string) {
	fmt.Println("manage install would:")
	if ctxloomDirExists(appDir) {
		fmt.Printf("  - reuse existing .ctxloom: %s\n", appDir)
	} else {
		fmt.Printf("  - scaffold .ctxloom: %s (engine: %s)\n", appDir, manageInstallEngine)
	}
	fmt.Printf("  - update .gitignore: %s\n", filepath.Join(projectDir, ".gitignore"))
	fmt.Println("  - apply hooks, statusline, and MCP registration for all backends")
}

// ensureHarnessGitignore excludes ctxloom's private state and transient
// artifacts from git. Fault tolerant: a failure warns and continues.
func ensureHarnessGitignore(projectDir string) {
	patterns := append(append([]string{}, gitignore.PrivateStatePatterns...), gitignore.TransientArtifactPatterns...)
	if err := gitignore.Ensure(projectDir, gitignore.Comment, patterns...); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to update .gitignore: %v\n", err)
	}
}

func runManageUninstall(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.RemoveHooks(cmd.Context(), cfg, operations.RemoveHooksRequest{Backend: "all"})
	if err != nil {
		return err
	}
	fmt.Printf("Removed ctxloom harness from: %v\n", result.Backends)
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", e)
	}
	fmt.Println("The .ctxloom directory and its contents were left in place.")
	return nil
}

func runManageStatus(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.HarnessStatus(cmd.Context(), cfg, operations.HarnessStatusRequest{})
	if err != nil {
		return err
	}
	printHarnessStatus(result)
	return nil
}

// printHarnessStatus renders the wiring report.
func printHarnessStatus(r *operations.HarnessStatusResult) {
	fmt.Printf("Project: %s\n", r.WorkDir)
	fmt.Printf("MCP auto-registration: %v\n", r.AutoRegisterMCP)
	fmt.Printf("Statusline (HUD): %v\n\n", r.ManageStatusline)
	for _, b := range r.Backends {
		if !b.SettingsExists {
			fmt.Printf("  %s: not configured\n", b.Backend)
			continue
		}
		fmt.Printf("  %s: hooks=%v statusline=%v mcp=%v\n", b.Backend, b.HooksPresent, b.StatusLine, b.MCPPresent)
	}
	fmt.Println()
	printCompanionStatus()
}

// companionHint describes what a missing companion binary disables and how to
// install it.
type companionHint struct {
	feature string
	install string
}

// companionHints carries the per-companion status text. The companion list
// itself is derived from the embedded built-in bundles (see
// config.BuiltinCompanionBins) so a future builtin's companion shows up here
// automatically; an entry missing from this map gets the generic fallback.
var companionHints = map[string]companionHint{
	"taskloom": {"task tools (task_list/task_add/...)", "brew install ctxloom/tap/taskloom"},
	"ltk":      {"command-redirect pre-tool hook", "brew install ctxloom/tap/ltk"},
}

// hintForCompanion returns the install-hint text for bin, falling back to a
// generic description for companions without a curated entry.
func hintForCompanion(bin string) companionHint {
	if h, ok := companionHints[bin]; ok {
		return h
	}
	return companionHint{
		feature: "its built-in bundle wiring",
		install: "brew install ctxloom/tap/" + bin,
	}
}

// printCompanionStatus reports each companion binary's presence; builtin
// bundle entries for missing ones are skipped at resolve time.
func printCompanionStatus() {
	fmt.Println("Companions:")
	for _, bin := range config.BuiltinCompanionBins() {
		hint := hintForCompanion(bin)
		path, err := exec.LookPath(bin)
		if err != nil {
			fmt.Printf("  %s: NOT FOUND — %s disabled (install: %s)\n", bin, hint.feature, hint.install)
			continue
		}
		fmt.Printf("  %s: %s\n", bin, path)
	}
}

// --- manage hooks -----------------------------------------------------------

var manageHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Install, uninstall, or inspect ctxloom backend hooks",
}

var manageHooksBackend string

var manageHooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Apply ctxloom hooks and regenerate context into backend config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		result, err := operations.ApplyHooks(cmd.Context(), cfg, operations.ApplyHooksRequest{
			Backend:           manageHooksBackend,
			RegenerateContext: true,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Hooks %s for: %v\n", result.Status, result.Backends)
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", e)
		}
		return nil
	},
}

var manageHooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ctxloom hooks, statusline, MCP entries, and command files",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}
		result, err := operations.RemoveHooks(cmd.Context(), cfg, operations.RemoveHooksRequest{Backend: manageHooksBackend})
		if err != nil {
			return err
		}
		fmt.Printf("Hooks %s for: %v\n", result.Status, result.Backends)
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", e)
		}
		return nil
	},
}

var manageHooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which backends have ctxloom hooks wired in",
	Args:  cobra.NoArgs,
	RunE:  runManageStatus,
}

// --- manage mcp -------------------------------------------------------------

var manageMcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage ctxloom MCP registration and configured servers",
}

var manageMcpInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Enable auto-registration of ctxloom's own MCP server",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return setMcpAutoRegister(cmd.Context(), true) },
}

var manageMcpUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Disable auto-registration of ctxloom's own MCP server",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return setMcpAutoRegister(cmd.Context(), false) },
}

// setMcpAutoRegister toggles ctxloom's MCP auto-registration and prints the
// resulting state.
func setMcpAutoRegister(ctx context.Context, enabled bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	result, err := operations.SetMCPAutoRegister(ctx, cfg, operations.SetMCPAutoRegisterRequest{Enabled: enabled})
	if err != nil {
		return err
	}
	state := "disabled"
	if result.AutoRegister {
		state = "enabled"
	}
	fmt.Printf("ctxloom MCP server auto-registration: %s\n", state)
	fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
	return nil
}

var manageMcpServersCmd = &cobra.Command{
	Use:   "servers",
	Short: "List, add, remove, or show configured MCP servers",
}

// --- manage statusline ------------------------------------------------------

var manageStatuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Enable or disable ctxloom's HUD statusline",
	Long: `Control whether ctxloom installs and maintains its HUD statusline.

Disable it to keep your own (or no) statusline — ctxloom will stop managing the
statusline and clear any it previously installed. The change takes effect on the
next 'ctxloom manage hooks install' or 'ctxloom run'.`,
}

var manageStatuslineInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Let ctxloom manage the HUD statusline (default)",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return setStatusline(cmd.Context(), true) },
}

var manageStatuslineUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop managing the HUD statusline; keep your own",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return setStatusline(cmd.Context(), false) },
}

// setStatusline persists the statusline preference and prints the resulting state.
func setStatusline(ctx context.Context, enabled bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	result, err := operations.SetStatusline(ctx, cfg, operations.SetStatuslineRequest{Enabled: enabled})
	if err != nil {
		return err
	}
	state := "disabled"
	if result.Statusline {
		state = "enabled"
	}
	fmt.Printf("ctxloom HUD statusline: %s\n", state)
	fmt.Println("Run 'ctxloom run' or 'ctxloom manage hooks install' to apply the change.")
	return nil
}

// --- manage config / gitignore ----------------------------------------------

var manageGitignoreCmd = &cobra.Command{
	Use:   "gitignore",
	Short: "Maintain ctxloom's .gitignore entries",
}

var manageGitignoreInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Add ctxloom's private-state and transient-artifact ignores",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		appDir, err := resolveAppDir(false)
		if err != nil {
			return err
		}
		projectDir := filepath.Dir(appDir)
		ensureHarnessGitignore(projectDir)
		fmt.Printf("Updated %s\n", filepath.Join(projectDir, ".gitignore"))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(manageCmd)

	// manage init (canonical) shares runInit + the init flag set with the
	// top-level `ctxloom init` alias.
	manageCmd.AddCommand(manageInitCmd)
	bindInitFlags(manageInitCmd)

	// Orchestrators.
	manageCmd.AddCommand(manageInstallCmd)
	manageCmd.AddCommand(manageUninstallCmd)
	manageCmd.AddCommand(manageStatusCmd)
	manageInstallCmd.Flags().StringVar(&manageInstallEngine, "engine", "claude-code", "AI engine to record when scaffolding")
	manageInstallCmd.Flags().BoolVar(&manageInstallPrint, "print", false, "Print the steps that would run, without executing")

	// hooks.
	manageCmd.AddCommand(manageHooksCmd)
	manageHooksCmd.AddCommand(manageHooksInstallCmd)
	manageHooksCmd.AddCommand(manageHooksUninstallCmd)
	manageHooksCmd.AddCommand(manageHooksStatusCmd)
	for _, c := range []*cobra.Command{manageHooksInstallCmd, manageHooksUninstallCmd} {
		c.Flags().StringVar(&manageHooksBackend, "backend", "all", "Backend to target (claude-code, antigravity, or all)")
	}

	// mcp: own-server registration + configured-server CRUD (re-parented).
	manageCmd.AddCommand(manageMcpCmd)
	manageMcpCmd.AddCommand(manageMcpInstallCmd)
	manageMcpCmd.AddCommand(manageMcpUninstallCmd)
	manageMcpCmd.AddCommand(manageMcpServersCmd)
	manageMcpServersCmd.AddCommand(mcpListCmd)
	manageMcpServersCmd.AddCommand(mcpAddCmd)
	manageMcpServersCmd.AddCommand(mcpRemoveCmd)
	manageMcpServersCmd.AddCommand(mcpShowCmd)

	// statusline opt-out.
	manageCmd.AddCommand(manageStatuslineCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineInstallCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineUninstallCmd)

	// config (re-parented wholesale).
	manageCmd.AddCommand(configCmd)

	// gitignore.
	manageCmd.AddCommand(manageGitignoreCmd)
	manageGitignoreCmd.AddCommand(manageGitignoreInstallCmd)
}
