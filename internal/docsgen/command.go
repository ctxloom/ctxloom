package docsgen

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewCommand builds the `gendocs` command every documented product shares: the
// same flags, the same generators, the same output conventions. ctxloom runs it
// as the scripts/gendocs entrypoint (its cobra tree lives in an importable
// package); taskloom and ltk keep their trees in `package main`, so they mount
// it as a hidden subcommand compiled only under `-tags docsgen` (their shipped
// binaries never carry it). One generator, three products, no forks.
//
// Nothing is generated unless a destination flag says so, and each flag names
// the directory to write into.
func NewCommand(p *Product) *cobra.Command {
	var (
		manDir       string
		markdownDir  string
		mcpDir       string
		configDir    string
		configSchema string
	)

	cmd := &cobra.Command{
		Use:    "gendocs",
		Short:  "Generate " + p.Bin + "'s reference documentation from its sources of truth",
		Hidden: true, // never advertised, and never documented: hidden commands are skipped by the doc generators
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if manDir == "" && markdownDir == "" && mcpDir == "" && configDir == "" {
				return fmt.Errorf("at least one of --man, --markdown, --mcp, --config is required")
			}

			p.PrepareTree()
			out := cmd.OutOrStdout()

			if manDir != "" {
				if err := GenMan(p, manDir); err != nil {
					return fmt.Errorf("generate man pages: %w", err)
				}
				fmt.Fprintf(out, "%s man pages generated in %s\n", p.Bin, manDir)
			}
			if markdownDir != "" {
				if err := GenMarkdown(p, markdownDir); err != nil {
					return fmt.Errorf("generate markdown pages: %w", err)
				}
				fmt.Fprintf(out, "%s markdown pages generated in %s\n", p.Bin, markdownDir)
			}
			if mcpDir != "" {
				if err := GenMCPTools(cmd.Context(), p, mcpDir); err != nil {
					return fmt.Errorf("generate MCP tools reference: %w", err)
				}
				fmt.Fprintf(out, "%s MCP tools reference generated in %s\n", p.Bin, mcpDir)
			}
			if configDir != "" {
				if configSchema == "" {
					return fmt.Errorf("--config needs --config-schema (%s declares no config schema)", p.Bin)
				}
				if err := GenConfig(p, configSchema, configDir); err != nil {
					return fmt.Errorf("generate config reference: %w", err)
				}
				fmt.Fprintf(out, "%s config reference generated in %s\n", p.Bin, configDir)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&manDir, "man", "", "directory to write man pages into")
	f.StringVar(&markdownDir, "markdown", "", "directory to write website markdown pages into")
	f.StringVar(&mcpDir, "mcp", "", "directory to write the MCP tools reference (mcp-tools.md) into")
	f.StringVar(&configDir, "config", "", "directory to write the config reference (config.md) into")
	f.StringVar(&configSchema, "config-schema", p.ConfigSchema, "path to the config JSON Schema rendered by --config")

	return cmd
}
