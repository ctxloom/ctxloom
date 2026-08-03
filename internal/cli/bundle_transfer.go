package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	bundlePushPR      bool
	bundlePushMessage string
	bundlePushSign    bool
	bundlePushNoSign  bool
)

// registerPushFlags binds the publish flags onto a command that pushes a
// bundle. Every such command shares these four variables and this one
// registration, so an ALIAS of `bundle push` cannot present a different flag
// set, a different default, or different help text for the identical publish
// (they all reach the same pushBundle). Only one command runs per invocation,
// so one set of variables backing several FlagSets is exactly the point.
func registerPushFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&bundlePushPR, "pr", false, "Create a pull request instead of pushing directly")
	cmd.Flags().StringVarP(&bundlePushMessage, "message", "m", "", "Commit message")
	// The help text is normative about WHERE a signature comes from: publishing
	// carries the sidecar `ctxloom bundle sign` wrote, so --sign is sugar for
	// signing first and --no-sign means "publish bare", not "skip a signing
	// step this publish would otherwise have done".
	cmd.Flags().BoolVar(&bundlePushSign, "sign", false, "sign the bundle first, then publish that signature (same as `ctxloom bundle sign <name>` before pushing)")
	cmd.Flags().BoolVar(&bundlePushNoSign, "no-sign", false, "publish unsigned: do not carry an existing signature, and do not sign even if sign.default is true")
}

var bundlePushCmd = &cobra.Command{
	Use:   "push <name> [remote]",
	Short: "Publish a bundle to a remote repository",
	Long: `Publish a local bundle to a remote repository.

By default, publishes directly to the default branch. Use --pr to create
a pull request instead.

If no remote is specified, uses the default remote.

SIGNATURES: a signature belongs to the bundle, not to the publish.
'ctxloom bundle sign' writes a detached <name>.yaml.sig sibling, and push
CARRIES it — so the key that signs never has to be on the machine that
publishes, and CI can ship signed content it cannot itself forge. A
signature that no longer covers the bundle (edited after signing) stops
the push rather than shipping a pair every consumer reads as tampering.
Publishing unsigned is fine and supported; consumers review it.

Examples:
  ctxloom bundle push my-bundle
  ctxloom bundle push my-bundle ctxloom-default
  ctxloom bundle push my-bundle --pr
  ctxloom bundle sign my-bundle && ctxloom bundle push my-bundle  # sign here, publish there
  ctxloom bundle push my-bundle --sign                            # the same thing in one command
  ctxloom bundle push my-bundle --no-sign                         # publish bare
  ctxloom bundle push my-bundle ctxloom-default --message "Add my bundle"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runBundlePush,
}

func runBundlePush(cmd *cobra.Command, args []string) error {
	remoteOverride := ""
	if len(args) > 1 {
		remoteOverride = args[1]
	}
	return pushBundle(cmd, args[0], remoteOverride, bundlePushPR, bundlePushMessage, bundlePushSign, bundlePushNoSign)
}

var bundleExportOutput string

var bundleExportCmd = &cobra.Command{
	Use:   "export <name> [dest-dir]",
	Short: "Export a bundle to a file or directory",
	Long: `Export a local bundle from .ctxloom/content/bundles to a file or directory.

Useful for publishing bundles to a shared repository like ctxloom-default.
The bundle is copied as-is, preserving all content including distilled versions.

Use -o to specify an output file path directly.

Examples:
  ctxloom bundle export go-tools ../ctxloom-default/ctxloom/bundles
  ctxloom bundle export my-bundle ./exports
  ctxloom bundle export my-bundle -o exported.yaml`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runBundleExport,
}

func runBundleExport(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	destDir := ""
	if len(args) > 1 {
		destDir = args[1]
	}
	res, err := operations.ExportBundle(cmd.Context(), cfg, operations.ExportBundleRequest{
		Name:       name,
		OutputFile: bundleExportOutput,
		DestDir:    destDir,
	})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Exported: %s -> %s\n", res.Source, res.Dest)
		return w.Err()
	})
}

var bundleImportForce bool

var bundleImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a bundle from a local file",
	Long: `Import a bundle from a local YAML file into .ctxloom/content/bundles.

The bundle is copied into the local .ctxloom/content/bundles directory.
Use --force to overwrite an existing bundle.

Examples:
  ctxloom bundle import ../ctxloom-default/ctxloom/bundles/go-tools.yaml
  ctxloom bundle import ./my-bundle.yaml --force`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleImport,
}

func runBundleImport(cmd *cobra.Command, args []string) error {
	srcPath := args[0]

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	res, err := operations.ImportBundle(cmd.Context(), cfg, operations.ImportBundleRequest{
		SourcePath: srcPath,
		Force:      bundleImportForce,
	})
	if err != nil {
		return err
	}

	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Imported: %s -> %s\n", res.Source, res.Dest)
		w.Printf("  Version: %s\n", res.Version)
		w.Printf("  Fragments: %d, Commands: %d, MCP: %d\n", res.Fragments, res.Commands, res.MCP)
		return w.Err()
	})
}

// registerBundleImportFlags defines `bundle import`'s flags.
func registerBundleImportFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&bundleImportForce, "force", "f", false, "Overwrite existing bundle")
}

// registerBundleExportFlags defines `bundle export`'s flags.
func registerBundleExportFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&bundleExportOutput, "output", "o", "", "Output file path")
}
