package cli

import (
	"github.com/spf13/cobra"
)

// Bare `ctxloom bundle` lists the installed bundles: the collection is the
// one thing the noun is about, and reading it touches nothing.
var bundleCmd = groupNodeDefault(&cobra.Command{
	Use:   "bundle",
	Short: "Manage ctxloom bundles",
	// Unhidden: every command's help already assumed 'bundle' as the
	// underlying unit (push/sign/hold/mcp have no other home), so hiding the
	// noun itself from --help was the stale part.
	Long: `Manage ctxloom bundles - versioned collections of fragments, commands, and MCP servers.

Bundles are the primary content unit in ctxloom. They group related context fragments,
commands, and optional MCP server configurations with a single version.

REFERENCE FORMAT

An item reference is <bundle>#<kind>/<name>. The kinds are fragments, commands
(prompts is accepted as an alias), mcp, hooks, and skills; a hook's name is
<event>/<index>.

The bundle half is either a CANONICAL URI or a plain bundle NAME:

  ctxloom+local:<name>                        a bundle in this project's tree
  ctxloom+companion:<binary>                  a companion binary's loadout
  ctxloom+git://<host>/<repo>//bundles/<name> a bundle in a remote repository
  ctxloom+file://<abs-repo>//bundles/<name>   the same, in a repo on this disk

A plain name is looked up, and is refused when it names more than one bundle —
the URI in that refusal says which is which. Every form needs shell quoting,
because '#' starts a comment in most shells.`,
	Example: `  ctxloom bundle list                  # List all installed bundles
  ctxloom bundle show go-tools         # Show bundle contents
  ctxloom bundle create my-bundle      # Create a new bundle
  ctxloom bundle export go-tools ./out # Export bundle to directory
  ctxloom bundle import ./my-bundle.yaml # Import bundle from file
  ctxloom bundle move go-tools --to ctxloom-default # Relocate a bundle (signature and all)`,
}, "list")

func init() {
	rootCmd.AddCommand(bundleCmd)
	bundleCmd.AddCommand(bundleListCmd)
	bundleCmd.AddCommand(bundleShowCmd)
	bundleCmd.AddCommand(bundleViewCmd)
	bundleCmd.AddCommand(bundleCreateCmd)
	bundleCmd.AddCommand(bundleEditCmd)
	bundleCmd.AddCommand(bundleRemoveCmd)
	bundleCmd.AddCommand(bundlePushCmd)
	bundleCmd.AddCommand(bundleExportCmd)
	bundleCmd.AddCommand(bundleImportCmd)
	bundleCmd.AddCommand(bundleMoveCmd)
	bundleCmd.AddCommand(bundleDistillCmd)

	// Fragment/prompt management lives under the top-level `fragment` and
	// `prompt` commands (full CRUD, routed through operations); the former
	// duplicate `bundle fragment`/`bundle prompt` subtrees were removed.
	// Bundle-scoped MCP editing moved to the MCP-server noun as
	// `mcp server edit <bundle>#mcp/<name>` (verb-spine reorg §5), which
	// deleted the `bundle mcp` group node too.

	// Bundle hold/unhold — dependency management over the active lockfile.

	// Real home of the deprecated top-level `ctxloom sign` (flags registered
	// in sign.go alongside its shared RunE).
	bundleCmd.AddCommand(bundleSignCmd)

	// Each command's flags are defined ALONGSIDE the command, in its own file
	// (the shape registerPushFlags already had); this is only the wiring, so
	// adding a flag never means editing a file the command does not live in.
	registerBundleCreateFlags(bundleCreateCmd)
	registerBundleRemoveFlags(bundleRemoveCmd)
	registerBundleEditFlags(bundleEditCmd)
	registerPushFlags(bundlePushCmd)
	registerBundleImportFlags(bundleImportCmd)
	registerBundleExportFlags(bundleExportCmd)
	registerBundleMoveFlags(bundleMoveCmd)
	registerBundleViewFlags(bundleViewCmd)
	registerBundleShowFlags(bundleShowCmd)
	registerBundleDistillFlags(bundleDistillCmd)
}
