package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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

		data, err := yaml.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}

		fmt.Print(string(data))
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <section>",
	Short: "Get a configuration section",
	Long: `Get a specific configuration section.

Available sections:
  defaults    Default settings (LLM plugin, use_distilled, etc.)
  llm         Language model plugin configuration
  mcp         MCP server configuration
  profiles    Profile configurations`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		section := args[0]
		var data interface{}

		switch section {
		case "defaults":
			data = cfg.Defaults
		case "llm":
			data = cfg.LM
		case "mcp":
			data = cfg.MCP
		case "profiles":
			data = cfg.Profiles
		default:
			return fmt.Errorf("unknown section: %s\n\nAvailable: defaults, llm, mcp, profiles", section)
		}

		output, err := yaml.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal section: %w", err)
		}

		fmt.Print(string(output))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
}
