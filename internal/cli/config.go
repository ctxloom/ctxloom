package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// configCmd is the top-level home of ctxloom configuration (CLI-primary
// reorg plan, Decision 6: `manage config *` promoted to top-level `config
// *`). The old `manage config` path is kept working as a deprecated alias
// namespace (manage.go's manageConfigCmd), sharing the RunE bodies below.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration",
	Long: `Show or modify ctxloom configuration.

Examples:
  ctxloom config show              # Show full configuration
  ctxloom config get defaults      # Get a specific section
  ctxloom config edit              # Open config.yaml in $EDITOR
  ctxloom config init              # Scaffold a default config.yaml`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show full configuration",
	RunE:  runConfigShow,
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return renderConfigYAML(cfg, cmd.OutOrStdout())
}

// configGetLong is shared by configGetCmd (real home) and
// manageConfigGetCmd (deprecated alias, manage.go).
const configGetLong = `Get a specific configuration section.

Available sections:
  config      Behavioral settings (use_distilled, compaction_chunks)
  llm         Language model configuration (labeled configs + role map)
  mcp         MCP server configuration
  profiles    Profile defaults and definitions`

var configGetCmd = &cobra.Command{
	Use:   "get <section>",
	Short: "Get a configuration section",
	Long:  configGetLong,
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigGet,
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return renderConfigSection(cfg, args[0], cmd.OutOrStdout())
}

// renderConfigYAML marshals cfg to YAML and writes it to out. Extracted
// from configShowCmd's RunE so the marshal + write composition is
// testable without invoking cobra.
func renderConfigYAML(cfg *config.Config, out io.Writer) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	_, err = out.Write(data)
	return err
}

// resolveConfigSection returns the named top-level section of cfg, or an
// error whose message lists the valid section names. The switch is the
// load-bearing surface — adding a new section here is the only place
// the `config get <section>` CLI surface changes.
func resolveConfigSection(cfg *config.Config, name string) (any, error) {
	switch name {
	case "config":
		return cfg.GetSettings(), nil
	case "llm":
		return cfg.GetLMConfig(), nil
	case "mcp":
		return cfg.GetMCPConfig(), nil
	case "profiles":
		return cfg.GetProfilesConfig(), nil
	default:
		return nil, fmt.Errorf("unknown section: %s\n\nAvailable: config, llm, mcp, profiles", name)
	}
}

// renderConfigSection resolves the named section and writes it to out as
// YAML. Extracted from configGetCmd's RunE.
func renderConfigSection(cfg *config.Config, name string, out io.Writer) error {
	data, err := resolveConfigSection(cfg, name)
	if err != nil {
		return err
	}
	output, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal section: %w", err)
	}
	_, err = out.Write(output)
	return err
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open config.yaml in $EDITOR",
	Args:  cobra.NoArgs,
	RunE:  runConfigEdit,
}

func runConfigEdit(cmd *cobra.Command, _ []string) error {
	path := projectConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("no config at %s — run 'ctxloom config init' first", path)
	}
	return openInEditor(path)
}

// configInitLong is shared by configInitCmd (real home) and
// manageConfigInitCmd (deprecated alias, manage.go).
const configInitLong = `Write a default config.yaml AND a default remotes.yaml into the project
.ctxloom directory.

Refuses to overwrite an existing config.yaml, but the accompanying remotes.yaml
is (re)written with defaults — back up a customized remotes.yaml first. For a
fuller project scaffold (hooks, discovery), use 'ctxloom init'.`

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold a default config.yaml (and remotes.yaml)",
	Long:  configInitLong,
	Args:  cobra.NoArgs,
	RunE:  runConfigInit,
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	path := paths.ConfigPath(appDir)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	}
	if _, err := operations.InitializeProject(context.Background(), operations.InitializeProjectRequest{
		AppDir: appDir,
		Engine: configInitEngine,
	}); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", path)
	return nil
}

var configInitEngine string

// projectConfigPath returns the path to the project's config.yaml.
func projectConfigPath() string {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return paths.ConfigPath(config.AppDirName)
	}
	return paths.ConfigPath(appDir)
}

// openInEditor launches the environment-resolved editor (VISUAL → EDITOR →
// nano; multi-word values are split) on path, wired to the current terminal.
// This command edits the config file itself — possibly a broken one — so it
// must not depend on config load; it uses the shared env-only half of the
// editor policy instead of cfg.GetEditorCommand. (The last-resort fallback
// changed from vi to nano to match the config-aware path.)
func openInEditor(path string) error {
	editor, args := config.EditorFromEnv()
	c := exec.Command(editor, append(args, path)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func init() {
	// Top-level (CLI-primary reorg plan, Decision 6): `manage config` used to
	// be the only home; manage.go now wires a deprecated alias namespace
	// instead of re-parenting this same *cobra.Command (a command has exactly
	// one parent).
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configInitCmd)
	configInitCmd.Flags().StringVar(&configInitEngine, "engine", "claude-code", "AI engine to record in the scaffolded config")
}
