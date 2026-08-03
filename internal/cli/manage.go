package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// manageCmd is the home for everything that mutates the project harness:
// scaffolding (.ctxloom), hooks/statusline, MCP registration, command files,
// .gitignore, and config. Runtime entrypoints (`mcp serve`) and machine
// callbacks (all consolidated under the hidden `hook` namespace) deliberately
// stay out of this user-facing namespace.
var manageCmd = groupNode(&cobra.Command{
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
  ctxloom manage gitignore    Maintain ctxloom's .gitignore entries

MCP registration lives at the top-level 'ctxloom mcp register'/'unregister';
configuration lives at the top-level 'ctxloom config'; the duplicate
'manage init' setup entry point was removed, root 'ctxloom init' is the sole
bootstrap.`,
})

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
// tolerant with respect to per-backend hook errors (collected into
// result.Errors and warned, not fatal) so a partial wire still lands the user
// in a working state; NOT fault tolerant with respect to the .gitignore write,
// which now fails loud.
func runManageInstall(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(appDir)

	if err := checkInstallEngineApplies(ctxloomDirExists(appDir), cmd.Flags().Changed("engine"), manageInstallEngine); err != nil {
		return err
	}

	if manageInstallPrint {
		type manageInstallPlanResult struct {
			CtxloomDir       string `json:"ctxloom_dir"`
			CtxloomDirExists bool   `json:"ctxloom_dir_exists"`
			Engine           string `json:"engine,omitempty"`
			Gitignore        string `json:"gitignore"`
		}
		exists := ctxloomDirExists(appDir)
		plan := manageInstallPlanResult{
			CtxloomDir:       appDir,
			CtxloomDirExists: exists,
			Gitignore:        filepath.Join(projectDir, ".gitignore"),
		}
		if !exists {
			plan.Engine = manageInstallEngine
		}
		return emit(cmd, plan, func() error {
			printInstallPlan(appDir, projectDir)
			return nil
		})
	}

	initialized := false
	if !ctxloomDirExists(appDir) {
		if _, err := operations.InitializeProject(cmd.Context(), operations.InitializeProjectRequest{
			AppDir: appDir,
			Engine: manageInstallEngine,
		}); err != nil {
			return err
		}
		initialized = true
	}

	if err := ensureHarnessGitignore(projectDir); err != nil {
		return err
	}

	// ApplyHooks reloads config from disk itself, so this load is
	// not an input to it — it is the early guard + config-warning echo the
	// command owes the user before doing any work.
	if _, err := GetConfig(); err != nil {
		return err
	}
	// An EXPLICIT --engine scopes the hook apply to that one backend — the flag
	// reads like "install for this engine" and used to wire all five
	// regardless (`.opencode/`, `.kiro/`, etc. materializing in a project that
	// uses only one engine). Omitting the flag keeps the prior "all" default:
	// manageInstallEngine always holds a value (its flag default is
	// "claude-code"), so Changed is the only reliable signal that the user
	// actually asked for one engine — see checkInstallEngineApplies above,
	// which gates on the same Changed() check for the same reason.
	hookBackend := "all"
	if cmd.Flags().Changed("engine") {
		hookBackend = manageInstallEngine
	}
	result, err := operations.ApplyHooks(cmd.Context(), operations.ApplyHooksRequest{
		Backend:           hookBackend,
		RegenerateContext: true,
	})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageInstallResult struct {
		AppDir      string   `json:"app_dir"`
		Initialized bool     `json:"initialized"`
		Gitignore   string   `json:"gitignore"`
		Status      string   `json:"status"`
		Backends    []string `json:"backends"`
		Errors      []string `json:"errors,omitempty"`
	}
	out := manageInstallResult{
		AppDir:      appDir,
		Initialized: initialized,
		Gitignore:   filepath.Join(projectDir, ".gitignore"),
		Status:      result.Status,
		Backends:    result.Backends,
		Errors:      result.Errors,
	}
	return emit(cmd, out, func() error {
		if initialized {
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized ctxloom directory: %s\n", appDir)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v\n", result.Status, result.Backends)
		return nil
	})
}

// checkInstallEngineApplies rejects a `manage install --engine <x>` whose
// engine choice cannot reach anything: the engine is recorded by
// InitializeProject while it scaffolds, so on a project that already has a
// .ctxloom the flag has no effect at all. Re-running install to re-apply hooks
// stays supported — only an EXPLICITLY passed --engine is refused, and only
// when there is nothing left to scaffold.
//
// The refusal names where the engine actually lives, because the flag reads like
// the way to change it and silently was not.
func checkInstallEngineApplies(dirExists, engineRequested bool, engine string) error {
	if !dirExists || !engineRequested {
		return nil
	}
	return fmt.Errorf("--engine %s cannot be applied: .ctxloom already exists, and the engine is only recorded while scaffolding it; change the default engine with `ctxloom llm default <name>` (or remove .ctxloom to re-scaffold)", engine)
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
// artifacts from git. Returns the write failure instead of swallowing it —
// callers must not report success when the file was never updated
// (`manage gitignore install` used to print "Updated <path>" and
// exit 0 even when the write failed).
func ensureHarnessGitignore(projectDir string) error {
	patterns := append(append([]string{}, gitignore.PrivateStatePatterns...), gitignore.TransientArtifactPatterns...)
	if err := gitignore.Ensure(projectDir, gitignore.Comment, patterns...); err != nil {
		return fmt.Errorf("failed to update .gitignore: %w", err)
	}
	return nil
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
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageUninstallResult struct {
		Status   string   `json:"status"`
		Backends []string `json:"backends"`
		Errors   []string `json:"errors,omitempty"`
		Note     string   `json:"note"`
	}
	out := manageUninstallResult{
		Status:   result.Status,
		Backends: result.Backends,
		Errors:   result.Errors,
		Note:     "The .ctxloom directory and its contents were left in place.",
	}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed ctxloom harness from: %v\n", result.Backends)
		fmt.Fprintln(cmd.OutOrStdout(), out.Note)
		return nil
	})
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
	return emit(cmd, result, func() error {
		printHarnessStatus(result)
		return nil
	})
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

// --- manage hooks -----------------------------------------------------------

var manageHooksCmd = groupNode(&cobra.Command{
	Use:   "hooks",
	Short: "Install, uninstall, or inspect ctxloom backend hooks",
})

var (
	manageHooksBackend string
	manageHooksForce   bool
)

var manageHooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Apply ctxloom hooks and regenerate context into backend config",
	Args:  cobra.NoArgs,
	RunE:  runManageHooksInstall,
}

func runManageHooksInstall(cmd *cobra.Command, _ []string) error {
	// Guard + config-warning echo only; ApplyHooks reloads from disk
	// itself.
	if _, err := GetConfig(); err != nil {
		return err
	}
	workDir := projectroot.WorkDir()
	result, err := operations.ApplyHooks(cmd.Context(), operations.ApplyHooksRequest{
		Backend:           manageHooksBackend,
		RegenerateContext: true,
		Force:             manageHooksForce,
	})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageHooksInstallResult struct {
		Status      string   `json:"status"`
		Backends    []string `json:"backends"`
		ProjectRoot string   `json:"project_root"`
		Errors      []string `json:"errors,omitempty"`
	}
	out := manageHooksInstallResult{
		Status:      result.Status,
		Backends:    result.Backends,
		ProjectRoot: workDir,
		Errors:      result.Errors,
	}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v (project root: %s)\n", result.Status, result.Backends, workDir)
		return nil
	})
}

var manageHooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ctxloom hooks, statusline, MCP entries, and command files",
	Args:  cobra.NoArgs,
	RunE:  runManageHooksUninstall,
}

func runManageHooksUninstall(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.RemoveHooks(cmd.Context(), cfg, operations.RemoveHooksRequest{Backend: manageHooksBackend})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageHooksUninstallResult struct {
		Status   string   `json:"status"`
		Backends []string `json:"backends"`
		Errors   []string `json:"errors,omitempty"`
	}
	out := manageHooksUninstallResult{Status: result.Status, Backends: result.Backends, Errors: result.Errors}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v\n", result.Status, result.Backends)
		return nil
	})
}

var manageHooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which backends have ctxloom hooks wired in",
	Args:  cobra.NoArgs,
	RunE:  runManageStatus,
}

// --- manage mcp -------------------------------------------------------------

// setMcpAutoRegister toggles ctxloom's MCP auto-registration and prints the
// resulting state.
// setMcpAutoRegister toggles ctxloom's MCP auto-registration and renders the
// resulting state through emit() (text/json/yaml/toml/markdown).
func setMcpAutoRegister(cmd *cobra.Command, enabled bool) error {
	if _, err := GetConfig(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	result, err := operations.SetMCPAutoRegister(cmd.Context(), config.NewManager(), operations.SetMCPAutoRegisterRequest{Enabled: enabled})
	if err != nil {
		return err
	}
	state := "disabled"
	if result.AutoRegister {
		state = "enabled"
	}

	type mcpAutoRegisterResult struct {
		Status       string `json:"status"`
		AutoRegister bool   `json:"auto_register"`
	}
	out := mcpAutoRegisterResult{Status: state, AutoRegister: result.AutoRegister}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "ctxloom MCP server auto-registration: %s\n", state)
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ctxloom run' or 'ctxloom manage hooks install' to apply changes to backend settings.")
		return nil
	})
}

// --- manage statusline ------------------------------------------------------

var manageStatuslineCmd = groupNode(&cobra.Command{
	Use:   "statusline",
	Short: "Enable or disable ctxloom's HUD statusline",
	Long: `Control whether ctxloom installs and maintains its HUD statusline.

Disable it to keep your own (or no) statusline — ctxloom will stop managing the
statusline and clear any it previously installed. The change takes effect on the
next 'ctxloom manage hooks install' or 'ctxloom run'.`,
})

var manageStatuslineInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Let ctxloom manage the HUD statusline (default)",
	Args:  cobra.NoArgs,
	RunE:  runManageStatuslineInstall,
}

var manageStatuslineUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop managing the HUD statusline; keep your own",
	Args:  cobra.NoArgs,
	RunE:  runManageStatuslineUninstall,
}

func runManageStatuslineInstall(cmd *cobra.Command, _ []string) error {
	return setStatusline(cmd, true)
}

func runManageStatuslineUninstall(cmd *cobra.Command, _ []string) error {
	return setStatusline(cmd, false)
}

// setStatusline persists the statusline preference and prints the resulting state.
// setStatusline persists the statusline preference and renders the resulting
// state through emit() (text/json/yaml/toml/markdown).
func setStatusline(cmd *cobra.Command, enabled bool) error {
	if _, err := GetConfig(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	result, err := operations.SetStatusline(cmd.Context(), config.NewManager(), operations.SetStatuslineRequest{Enabled: enabled})
	if err != nil {
		return err
	}
	state := "disabled"
	if result.Statusline {
		state = "enabled"
	}

	type manageStatuslineResult struct {
		Status     string `json:"status"`
		Statusline bool   `json:"statusline"`
	}
	out := manageStatuslineResult{Status: state, Statusline: result.Statusline}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "ctxloom HUD statusline: %s\n", state)
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ctxloom run' or 'ctxloom manage hooks install' to apply the change.")
		return nil
	})
}

// --- manage gitignore --------------------------------------------------------

var manageGitignoreCmd = groupNode(&cobra.Command{
	Use:   "gitignore",
	Short: "Maintain ctxloom's .gitignore entries",
})

var manageGitignoreInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Add ctxloom's private-state and transient-artifact ignores",
	Args:  cobra.NoArgs,
	RunE:  runManageGitignoreInstall,
}

func runManageGitignoreInstall(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(appDir)
	if err := ensureHarnessGitignore(projectDir); err != nil {
		return err
	}
	path := filepath.Join(projectDir, ".gitignore")

	type manageGitignoreInstallResult struct {
		Status string `json:"status"`
		Path   string `json:"path"`
	}
	return emit(cmd, manageGitignoreInstallResult{Status: "updated", Path: path}, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s\n", path)
		return nil
	})
}

func init() {
	rootCmd.AddCommand(manageCmd)

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
	manageHooksInstallCmd.Flags().BoolVar(&manageHooksForce, "force", false, "Proceed even if the resolved project directory would write Claude Code's user-global settings (not inside a project / $HOME)")
	for _, c := range []*cobra.Command{manageHooksInstallCmd, manageHooksUninstallCmd} {
		c.Flags().StringVar(&manageHooksBackend, "backend", "all", "Backend to target (claude-code, antigravity, or all)")
	}

	// statusline opt-out.
	manageCmd.AddCommand(manageStatuslineCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineInstallCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineUninstallCmd)

	// gitignore.
	manageCmd.AddCommand(manageGitignoreCmd)
	manageGitignoreCmd.AddCommand(manageGitignoreInstallCmd)
}
