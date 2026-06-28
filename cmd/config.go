package cmd

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

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration",
	Long: `Show or modify ctxloom configuration.

Examples:
  ctxloom manage config show              # Show full configuration
  ctxloom manage config get defaults      # Get a specific section
  ctxloom manage config edit              # Open config.yaml in $EDITOR
  ctxloom manage config init              # Scaffold a default config.yaml`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show full configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		return renderConfigYAML(cfg, cmd.OutOrStdout())
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <section>",
	Short: "Get a configuration section",
	Long: `Get a specific configuration section.

Available sections:
  config      Behavioral settings (use_distilled, compaction_chunks)
  llm         Language model configuration (labeled configs + role map)
  mcp         MCP server configuration
  profiles    Profile defaults and definitions`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		return renderConfigSection(cfg, args[0], cmd.OutOrStdout())
	},
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
		return cfg.Settings, nil
	case "llm":
		return cfg.LM, nil
	case "mcp":
		return cfg.MCP, nil
	case "profiles":
		return cfg.Profiles, nil
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
	RunE: func(cmd *cobra.Command, _ []string) error {
		path := projectConfigPath()
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("no config at %s — run 'ctxloom manage config init' first", path)
		}
		return openInEditor(path)
	},
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold a default config.yaml (and remotes.yaml)",
	Long: `Write a default config.yaml AND a default remotes.yaml into the project
.ctxloom directory.

Refuses to overwrite an existing config.yaml, but the accompanying remotes.yaml
is (re)written with defaults — back up a customized remotes.yaml first. For a
fuller project scaffold (hooks, discovery), use 'ctxloom manage init'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
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
	},
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
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configInitCmd)
	configInitCmd.Flags().StringVar(&configInitEngine, "engine", "claude-code", "AI engine to record in the scaffolded config")
}
