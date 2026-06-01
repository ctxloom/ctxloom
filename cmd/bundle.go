package cmd

import (
	"github.com/spf13/cobra"
)

var bundleCmd = &cobra.Command{
	Use:    "bundle",
	Short:  "Manage ctxloom bundles",
	Hidden: true, // Use fragment/prompt commands for content management
	Long: `Manage ctxloom bundles - versioned collections of fragments, prompts, and MCP servers.

Bundles are the primary content unit in ctxloom. They group related context fragments,
prompts, and optional MCP server configurations with a single version.

Examples:
  ctxloom bundle list                  # List all installed bundles
  ctxloom bundle show go-tools         # Show bundle contents
  ctxloom bundle create my-bundle      # Create a new bundle
  ctxloom bundle export go-tools ./out # Export bundle to directory
  ctxloom bundle import ./my-bundle.yaml # Import bundle from file`,
}

func init() {
	rootCmd.AddCommand(bundleCmd)
	bundleCmd.AddCommand(bundleListCmd)
	bundleCmd.AddCommand(bundleShowCmd)
	bundleCmd.AddCommand(bundleViewCmd)
	bundleCmd.AddCommand(bundleCreateCmd)
	bundleCmd.AddCommand(bundleEditCmd)
	bundleCmd.AddCommand(bundleDeleteCmd)
	bundleCmd.AddCommand(bundlePushCmd)
	bundleCmd.AddCommand(bundleExportCmd)
	bundleCmd.AddCommand(bundleImportCmd)
	bundleCmd.AddCommand(bundleDistillCmd)

	// Fragment/prompt management lives under the top-level `fragment` and
	// `prompt` commands (full CRUD, routed through operations); the former
	// duplicate `bundle fragment`/`bundle prompt` subtrees were removed. MCP
	// editing has no top-level home, so it stays under `bundle mcp`.
	bundleCmd.AddCommand(bundleMCPCmd)
	bundleMCPCmd.AddCommand(bundleMCPEditCmd)

	bundleCreateCmd.Flags().StringVarP(&bundleCreateDesc, "description", "d", "", "Bundle description")
	bundleDeleteCmd.Flags().BoolVarP(&bundleDeleteForce, "force", "f", false, "Skip confirmation prompt")
	bundlePushCmd.Flags().BoolVar(&bundlePushPR, "pr", false, "Create a pull request instead of pushing directly")
	bundlePushCmd.Flags().StringVar(&bundlePushBranch, "branch", "", "Target branch (default: repository default)")
	bundlePushCmd.Flags().StringVarP(&bundlePushMessage, "message", "m", "", "Commit message")
	bundleImportCmd.Flags().BoolVarP(&bundleImportForce, "force", "f", false, "Overwrite existing bundle")
	bundleExportCmd.Flags().StringVarP(&bundleExportOutput, "output", "o", "", "Output file path")
	bundleViewCmd.Flags().BoolVarP(&bundleViewDistilled, "distilled", "d", false, "Show distilled version if available")

	// bundleEditCmd flags
	bundleEditCmd.Flags().StringVarP(&bundleEditDesc, "description", "d", "", "New description")
	bundleEditCmd.Flags().StringVar(&bundleEditVersion, "version", "", "New version")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddTags, "add-tag", nil, "Tag(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveTags, "remove-tag", nil, "Tag(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddFragment, "add-fragment", nil, "Fragment(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveFragment, "remove-fragment", nil, "Fragment(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddPrompt, "add-prompt", nil, "Prompt(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemovePrompt, "remove-prompt", nil, "Prompt(s) to remove")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditAddMCP, "add-mcp", nil, "MCP server(s) to add")
	bundleEditCmd.Flags().StringSliceVar(&bundleEditRemoveMCP, "remove-mcp", nil, "MCP server(s) to remove")

	bundleDistillCmd.Flags().BoolVarP(&bundleDistillForce, "force", "f", false, "Re-distill even if unchanged")
	bundleDistillCmd.Flags().BoolVarP(&bundleDistillDryRun, "dry-run", "n", false, "Preview what would be distilled")
	bundleDistillCmd.Flags().StringVarP(&bundleDistillPlugin, "plugin", "l", "", "LLM to use (default from config)")
}
