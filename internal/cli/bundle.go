package cli

import (
	"github.com/spf13/cobra"
)

var bundleCmd = &cobra.Command{
	Use:    "bundle",
	Short:  "Manage ctxloom bundles",
	Hidden: true, // Use fragment/skill commands for content management
	Long: `Manage ctxloom bundles - versioned collections of fragments, skills, and MCP servers.

Bundles are the primary content unit in ctxloom. They group related context fragments,
skills, and optional MCP server configurations with a single version.

Examples:
  ctxloom bundle list                  # List all installed bundles
  ctxloom bundle show go-tools         # Show bundle contents
  ctxloom bundle create my-bundle      # Create a new bundle
  ctxloom bundle export go-tools ./out # Export bundle to directory
  ctxloom bundle import ./my-bundle.yaml # Import bundle from file
  ctxloom bundle move go-tools --to ctxloom-default # Relocate a bundle (signature and all)`,
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
	bundleCmd.AddCommand(bundleMoveCmd)
	bundleCmd.AddCommand(bundleDistillCmd)

	// Fragment/prompt management lives under the top-level `fragment` and
	// `prompt` commands (full CRUD, routed through operations); the former
	// duplicate `bundle fragment`/`bundle prompt` subtrees were removed. MCP
	// editing has no top-level home, so it stays under `bundle mcp`.
	bundleCmd.AddCommand(bundleMCPCmd)
	bundleMCPCmd.AddCommand(bundleMCPEditCmd)

	// Bundle hold/unhold — dependency management over the active lockfile.
	// (Per-item content review lives in the top-level `ctxloom review`.)
	bundleCmd.AddCommand(bundleHoldCmd)
	bundleCmd.AddCommand(bundleUnholdCmd)

	bundleCreateCmd.Flags().StringVarP(&bundleCreateDesc, "description", "d", "", "Bundle description")
	bundleDeleteCmd.Flags().BoolVarP(&bundleDeleteForce, "force", "f", false, "Skip confirmation prompt")
	bundlePushCmd.Flags().BoolVar(&bundlePushPR, "pr", false, "Create a pull request instead of pushing directly")
	bundlePushCmd.Flags().StringVarP(&bundlePushMessage, "message", "m", "", "Commit message")
	bundlePushCmd.Flags().BoolVar(&bundlePushSign, "sign", false, "sign the published bundle (spec §3.1)")
	bundlePushCmd.Flags().BoolVar(&bundlePushNoSign, "no-sign", false, "don't sign, even if sign.default is true")
	bundleImportCmd.Flags().BoolVarP(&bundleImportForce, "force", "f", false, "Overwrite existing bundle")
	bundleExportCmd.Flags().StringVarP(&bundleExportOutput, "output", "o", "", "Output file path")
	bundleMoveCmd.Flags().StringVar(&bundleMoveTo, "to", "", "Destination: a configured remote name, or a local directory / ctxloom project checkout (a remote name wins)")
	bundleMoveCmd.Flags().BoolVarP(&bundleMoveForce, "force", "f", false, "Overwrite an existing bundle at a local destination")
	bundleMoveCmd.Flags().StringVarP(&bundleMoveMessage, "message", "m", "", "Commit message (remote destination only)")
	_ = bundleMoveCmd.MarkFlagRequired("to")
	bundleViewCmd.Flags().BoolVarP(&bundleViewDistilled, "distilled", "d", false, "Show distilled version if available")
	bundleShowCmd.Flags().BoolVarP(&bundleShowInteractive, "interactive", "i", false, "Review per-item effective trust and trust/blacklist individual hooks (interactive terminal only)")

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
	bundleDistillCmd.Flags().StringVarP(&bundleDistillLLM, "llm", "l", "", "config label to use (e.g. claude-code, claude-fast, antigravity); overrides the configured default")
}
