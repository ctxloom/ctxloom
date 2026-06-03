package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or modify ctxloom configuration",
	Long: `Show or modify ctxloom configuration.

Examples:
  ctxloom config show              # Show full configuration
  ctxloom config get defaults      # Get a specific section`,
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

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
}
