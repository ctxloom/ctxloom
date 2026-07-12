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

var bundlePushCmd = &cobra.Command{
	Use:   "push <name> [remote]",
	Short: "Publish a bundle to a remote repository",
	Long: `Publish a local bundle to a remote repository.

By default, publishes directly to the default branch. Use --pr to create
a pull request instead.

If no remote is specified, uses the default remote.

Examples:
  ctxloom bundle push my-bundle
  ctxloom bundle push my-bundle ctxloom-default
  ctxloom bundle push my-bundle --pr
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
	Long: `Export a local bundle from .ctxloom/cache/bundles to a file or directory.

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

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Exported: %s -> %s\n", res.Source, res.Dest)
	return w.Err()
}

var bundleImportForce bool

var bundleImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a bundle from a local file",
	Long: `Import a bundle from a local YAML file into .ctxloom/cache/bundles.

The bundle is copied into the local .ctxloom/cache/bundles directory.
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

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Imported: %s -> %s\n", res.Source, res.Dest)
	w.Printf("  Version: %s\n", res.Version)
	w.Printf("  Fragments: %d, Skills: %d, MCP: %d\n", res.Fragments, res.Skills, res.MCP)

	return w.Err()
}
