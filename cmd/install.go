package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/operations"
	"github.com/SophisticatedContextManager/scm/internal/remote"
)

var (
	installForce     bool
	installDryRun    bool
	installBlind     bool
	installNoCascade bool
)

var installCmd = &cobra.Command{
	Use:    "install [reference]",
	Short:  "Install bundles/profiles from remotes or lockfile",
	Hidden: true, // Use 'scm fragment install', 'scm prompt install', or 'scm profile install'
	Long: `Install bundles and profiles from remote sources or lockfile.

With no arguments, installs all items from the lockfile (.scm/lock.yaml).
This is similar to 'npm ci' - it performs a reproducible installation
using exact versions specified in the lockfile.

With a reference argument, installs a specific bundle or profile.
The type is auto-detected from the reference format or by checking
if a local bundle/profile with that name exists.

Reference formats:
  scm-github/core                                    # Uses default remote
  https://github.com/owner/repo@v1/bundles/core      # Full URL
  github:bundles/core@v1.0.0                         # Legacy format

Examples:
  scm install                           # Install all from lockfile
  scm install scm-github/core           # Install a bundle
  scm install scm-github/developer      # Install a profile
  scm install --force                   # Skip confirmation prompts
  scm install --dry-run                 # Show what would be installed`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstall,
}

// runInstall handles both lockfile installation and specific item installation.
func runInstall(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// No arguments = install from lockfile
	if len(args) == 0 {
		return runInstallFromLockfile(cmd, cfg)
	}

	// With argument = install specific item
	reference := args[0]
	return runInstallItem(cmd, cfg, reference)
}

// runInstallFromLockfile installs all dependencies from the lockfile.
func runInstallFromLockfile(cmd *cobra.Command, cfg *config.Config) error {
	if installDryRun {
		return runInstallDryRun(cfg)
	}

	result, err := operations.InstallDependencies(cmd.Context(), cfg, operations.InstallDependenciesRequest{
		Force: installForce,
	})
	if err != nil {
		return err
	}

	if result.Status == "empty" {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Generate one with: scm lock")
		fmt.Println("Or install specific items with: scm install <remote>/<name>")
		return nil
	}

	fmt.Printf("Installing %d items from lockfile...\n\n", result.Total)

	// Show errors if any
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "Error: %s\n", e)
	}

	if result.Failed > 0 {
		fmt.Println()
	}
	fmt.Printf("Installed: %d, Failed: %d\n", result.Installed, result.Failed)

	if result.Failed > 0 {
		return fmt.Errorf("some items failed to install")
	}

	return nil
}

// runInstallDryRun shows what would be installed without actually installing.
func runInstallDryRun(cfg *config.Config) error {
	baseDir := cfg.SCMDir
	if baseDir == "" {
		baseDir = ".scm"
	}

	lockManager := remote.NewLockfileManager(baseDir)
	lockfile, err := lockManager.Load()
	if err != nil {
		return fmt.Errorf("failed to load lockfile: %w", err)
	}

	entries := lockfile.AllEntries()
	if len(entries) == 0 {
		fmt.Println("No entries in lockfile.")
		return nil
	}

	fmt.Printf("Would install %d items:\n\n", len(entries))

	for _, e := range entries {
		shortSHA := e.Entry.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Printf("  [%s] %s @ %s\n", e.Type, e.Ref, shortSHA)
	}

	return nil
}

// runInstallItem installs a specific bundle or profile.
func runInstallItem(cmd *cobra.Command, cfg *config.Config, reference string) error {
	if installDryRun {
		fmt.Printf("Would install: %s\n", reference)
		return nil
	}

	// Auto-detect type from reference
	itemType := detectItemType(cfg, reference)

	// Cascade is enabled by default for profiles unless --no-cascade
	cascade := (itemType == "profile") && !installNoCascade

	result, err := operations.PullItem(cmd.Context(), cfg, operations.PullItemRequest{
		Reference: reference,
		ItemType:  itemType,
		Force:     installForce,
		Blind:     installBlind,
		Cascade:   cascade,
	})
	if err != nil {
		return err
	}

	action := "Installed"
	if result.Overwritten {
		action = "Updated"
	}

	fmt.Printf("%s %s → %s\n", action, reference, result.LocalPath)
	shortSHA := result.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	fmt.Printf("SHA: %s\n", shortSHA)

	if len(result.CascadePulled) > 0 {
		fmt.Printf("Cascade installed %d bundles\n", len(result.CascadePulled))
	}

	return nil
}

// detectItemType attempts to determine if a reference is a bundle or profile.
// It checks:
// 1. URL path patterns (/bundles/ vs /profiles/)
// 2. Local existence (if no remote prefix, check local dirs)
// 3. Default to bundle
func detectItemType(cfg *config.Config, reference string) string {
	// Check URL patterns
	if strings.Contains(reference, "/bundles/") {
		return "bundle"
	}
	if strings.Contains(reference, "/profiles/") {
		return "profile"
	}

	// For remote references like "scm-github/foo", we can't easily tell
	// Try to infer from local existence if it's a simple name
	if !strings.Contains(reference, "/") && !strings.Contains(reference, ":") {
		// Simple name - check if it exists locally as a profile or bundle
		for _, scmPath := range cfg.SCMPaths {
			profilePath := filepath.Join(scmPath, "profiles", reference+".yaml")
			if _, err := os.Stat(profilePath); err == nil {
				return "profile"
			}
			bundlePath := filepath.Join(scmPath, "bundles", reference+".yaml")
			if _, err := os.Stat(bundlePath); err == nil {
				return "bundle"
			}
		}
	}

	// Default to bundle - the pull operation will validate
	return "bundle"
}

func init() {
	rootCmd.AddCommand(installCmd)

	installCmd.Flags().BoolVarP(&installForce, "force", "f", false,
		"Skip confirmation prompts (content still displayed)")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false,
		"Show what would be installed without installing")
	installCmd.Flags().BoolVar(&installBlind, "blind", false,
		"Skip security review display (implies --force)")
	installCmd.Flags().BoolVar(&installNoCascade, "no-cascade", false,
		"Don't install referenced bundles (profiles only)")
}
