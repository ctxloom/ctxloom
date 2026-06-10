package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/iox"
)

var (
	bundlePushPR      bool
	bundlePushBranch  string
	bundlePushMessage string
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
	bundleName := args[0]
	remoteName := ""
	if len(args) > 1 {
		remoteName = args[1]
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	bundle, err := loadBundleForPush(cfg, bundleName)
	if err != nil {
		return err
	}

	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	remoteName, err = resolveDefaultRemote(registry, remoteName, "ctxloom bundle push <name> <remote>")
	if err != nil {
		return err
	}

	opts := remote.PublishOptions{
		CreatePR: bundlePushPR,
		Branch:   bundlePushBranch,
		Message:  bundlePushMessage,
		ItemType: remote.ItemTypeBundle,
	}
	fmt.Printf("Publishing bundle %q to %s...\n", bundleName, remoteName)

	pm := remote.NewPublishManager(registry, remote.LoadAuth(""))
	result, err := pm.Publish(cmd.Context(), bundle.Path, remoteName, opts)
	if err != nil {
		return err
	}

	printPublishResult(result)
	return nil
}

// loadBundleForPush loads the named bundle through the operations read-path,
// returning it for the publish flow (which needs its on-disk Path).
func loadBundleForPush(cfg *config.Config, bundleName string) (*bundles.Bundle, error) {
	bundle, err := operations.GetBundle(cfg, bundleName)
	if err != nil {
		return nil, fmt.Errorf("bundle not found: %s", bundleName)
	}
	return bundle, nil
}

var bundleExportOutput string

var bundleExportCmd = &cobra.Command{
	Use:   "export <name> [dest-dir]",
	Short: "Export a bundle to a file or directory",
	Long: `Export a bundle from .ctxloom/bundles to a file or directory.

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
	Long: `Import a bundle from a local YAML file into .ctxloom/bundles.

The bundle is copied into the local .ctxloom/bundles directory.
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
	w.Printf("  Fragments: %d, Prompts: %d, MCP: %d\n", res.Fragments, res.Prompts, res.MCP)

	return w.Err()
}
