package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
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

	// Load the bundle
	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(bundleName)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", bundleName)
	}

	// Initialize registry
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	// Use default remote if not specified
	if remoteName == "" {
		remoteName = registry.GetDefault()
		if remoteName == "" {
			return fmt.Errorf("no remote specified and no default set. Use: ctxloom bundle push <name> <remote>")
		}
	}

	auth := remote.LoadAuth("")

	// Build publish options
	opts := remote.PublishOptions{
		CreatePR: bundlePushPR,
		Branch:   bundlePushBranch,
		Message:  bundlePushMessage,
		ItemType: remote.ItemTypeBundle,
	}

	fmt.Printf("Publishing bundle %q to %s...\n", bundleName, remoteName)

	pm := remote.NewPublishManager(registry, auth)
	result, err := pm.Publish(cmd.Context(), bundle.Path, remoteName, opts)
	if err != nil {
		return err
	}

	if result.PRURL != "" {
		fmt.Printf("Created pull request: %s\n", result.PRURL)
	} else {
		action := "Created"
		if !result.Created {
			action = "Updated"
		}
		fmt.Printf("%s %s\n", action, result.Path)
		fmt.Printf("Commit: %s\n", result.SHA[:7])
	}

	return nil
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
  ctxloom bundle export go-tools ../ctxloom-default/ctxloom/v1/bundles
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

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return fmt.Errorf("no bundles directory found")
	}

	loader := bundles.NewLoader(bundleDirs, false)
	bundle, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("bundle not found: %s", name)
	}

	destDir := ""
	if len(args) > 1 {
		destDir = args[1]
	}
	destPath, err := exportBundleFile(afero.NewOsFs(), bundle.Path, bundleExportOutput, destDir)
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Exported: %s -> %s\n", bundle.Path, destPath)
	return w.Err()
}

// exportBundleFile copies srcPath to a destination chosen by the
// outputFile / destDir pair. Exactly one must be non-empty. The
// destination directory (or parent dir of outputFile) is created if
// missing. Returns the resolved destination path on success.
//
// Extracted from runBundleExport so the (-o <file> vs <dest-dir>)
// dispatch and the missing-arg error are testable against a MemMapFs.
func exportBundleFile(fs afero.Fs, srcPath, outputFile, destDir string) (string, error) {
	srcData, err := afero.ReadFile(fs, srcPath)
	if err != nil {
		return "", fmt.Errorf("failed to read bundle: %w", err)
	}

	var destPath string
	switch {
	case outputFile != "":
		destPath = outputFile
		if dir := filepath.Dir(destPath); dir != "." {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				return "", fmt.Errorf("failed to create destination directory: %w", err)
			}
		}
	case destDir != "":
		if err := fs.MkdirAll(destDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create destination directory: %w", err)
		}
		destPath = filepath.Join(destDir, filepath.Base(srcPath))
	default:
		return "", fmt.Errorf("either -o <file> or <dest-dir> must be specified")
	}

	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
		return "", fmt.Errorf("failed to write bundle: %w", err)
	}
	return destPath, nil
}

var bundleImportForce bool

var bundleImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a bundle from a local file",
	Long: `Import a bundle from a local YAML file into .ctxloom/bundles.

The bundle is copied into the local .ctxloom/bundles directory.
Use --force to overwrite an existing bundle.

Examples:
  ctxloom bundle import ../ctxloom-default/ctxloom/v1/bundles/go-tools.yaml
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

	bundleDir := paths.BundlesPath(cfg.AppPaths[0])
	destPath, bundle, err := importBundleFile(afero.NewOsFs(), srcPath, bundleDir, bundleImportForce)
	if err != nil {
		return err
	}

	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Imported: %s -> %s\n", srcPath, destPath)
	w.Printf("  Version: %s\n", bundle.Version)
	w.Printf("  Fragments: %d, Prompts: %d, MCP: %d\n", len(bundle.Fragments), len(bundle.Prompts), len(bundle.MCP))

	return w.Err()
}

// importBundleFile reads srcPath via fs, validates it parses as a bundle,
// then copies it into bundleDir (creating bundleDir if needed). force=false
// errors when the destination exists. Returns the destination path and the
// parsed bundle (for caller-side summary output) on success.
//
// Extracted from runBundleImport so the validation + overwrite-guard
// decision tree is testable against a MemMapFs without touching real
// .ctxloom/bundles/.
func importBundleFile(fs afero.Fs, srcPath, bundleDir string, force bool) (string, *bundles.Bundle, error) {
	srcData, err := afero.ReadFile(fs, srcPath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read source file: %w", err)
	}

	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
		return "", nil, fmt.Errorf("invalid bundle file: %w", err)
	}

	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return "", nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}

	destPath := filepath.Join(bundleDir, filepath.Base(srcPath))
	if _, err := fs.Stat(destPath); err == nil && !force {
		return "", nil, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}

	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
		return "", nil, fmt.Errorf("failed to write bundle: %w", err)
	}
	return destPath, bundle, nil
}
