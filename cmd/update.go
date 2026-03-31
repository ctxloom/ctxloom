package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/remote"
)

var (
	updateDryRunFlag  bool
	updateForceFlag   bool
	updateCleanupFlag bool
)

var updateCmd = &cobra.Command{
	Use:    "update [reference]",
	Short:  "Check for and apply updates to installed items",
	Hidden: true, // Use 'ctxloom remote update' instead
	Long: `Check for and apply updates to installed remote items.

Without arguments, updates all items in the lockfile.
With a reference, updates only that specific item.

By default, updates are applied. Use --dry-run to only show what would be updated.

Examples:
  ctxloom update                        # Update all items
  ctxloom update --dry-run              # Show available updates without applying
  ctxloom update ctxloom-default/core        # Update specific item
  ctxloom update --force                # Skip confirmation prompts
  ctxloom update --cleanup              # Also remove items deleted from remote`,
	RunE: runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	auth := remote.LoadAuth("")
	lockManager := remote.NewLockfileManager(".ctxloom")

	// If specific reference provided, update just that
	if len(args) > 0 {
		return updateSingleItem(cmd, args[0], registry, auth, lockManager)
	}

	// Otherwise, update all from lockfile
	return updateAllItems(cmd, registry, auth, lockManager)
}

func updateSingleItem(cmd *cobra.Command, refStr string, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	ref, err := remote.ParseReference(refStr)
	if err != nil {
		return fmt.Errorf("invalid reference: %w", err)
	}

	rem, err := registry.Get(ref.Remote)
	if err != nil {
		return err
	}

	fetcher, err := remote.NewFetcher(rem.URL, auth)
	if err != nil {
		return fmt.Errorf("failed to create fetcher: %w", err)
	}

	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return fmt.Errorf("invalid remote URL: %w", err)
	}

	// Get latest SHA
	branch, err := fetcher.GetDefaultBranch(cmd.Context(), owner, repo)
	if err != nil {
		return fmt.Errorf("failed to get default branch: %w", err)
	}

	latestSHA, err := fetcher.ResolveRef(cmd.Context(), owner, repo, branch)
	if err != nil {
		return fmt.Errorf("failed to resolve ref: %w", err)
	}

	// Check lockfile for current SHA
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	// Try all item types (bundles and profiles only)
	var currentSHA string
	var itemType remote.ItemType
	for _, it := range []remote.ItemType{remote.ItemTypeBundle, remote.ItemTypeProfile} {
		entry, ok := lockfile.GetEntry(it, refStr)
		if ok {
			currentSHA = entry.SHA
			itemType = it
			break
		}
	}

	switch currentSHA {
	case "":
		fmt.Printf("%s not found in lockfile, checking latest version...\n", refStr)
		// Default to bundle if not in lockfile
		itemType = remote.ItemTypeBundle
	case latestSHA:
		fmt.Printf("%s is up to date (SHA: %s)\n", refStr, formatSHA(latestSHA))
		return nil
	default:
		fmt.Printf("%s has update available:\n", refStr)
		fmt.Printf("  Current: %s\n", formatSHA(currentSHA))
		fmt.Printf("  Latest:  %s\n", formatSHA(latestSHA))
	}

	if updateDryRunFlag {
		fmt.Println("\nRun without --dry-run to apply update.")
		return nil
	}

	// Apply update
	puller := remote.NewPuller(registry, auth)
	opts := remote.PullOptions{
		ItemType: itemType,
		Force:    updateForceFlag,
	}

	result, err := puller.Pull(cmd.Context(), refStr, opts)
	if err != nil {
		return err
	}

	fmt.Printf("\nUpdated %s → %s\n", refStr, formatSHA(result.SHA))
	return nil
}

func updateAllItems(cmd *cobra.Command, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Install items with: ctxloom install <remote>/<name>")
		return nil
	}

	entries := lockfile.AllEntries()

	type updateItem struct {
		Type       remote.ItemType
		Ref        string
		CurrentSHA string
		LatestSHA  string
	}

	var profileUpdates []updateItem
	var bundleUpdates []updateItem
	var emptyEntries []struct {
		Type remote.ItemType
		Ref  string
	}

	fmt.Printf("Checking %d items for updates...\n\n", len(entries))

	for _, e := range entries {
		// Track entries with empty SHA (incomplete lockfile entries)
		if e.Entry.SHA == "" {
			emptyEntries = append(emptyEntries, struct {
				Type remote.ItemType
				Ref  string
			}{Type: e.Type, Ref: e.Ref})
			continue
		}

		ref, err := remote.ParseReference(e.Ref)
		if err != nil {
			continue
		}

		rem, err := registry.Get(ref.Remote)
		if err != nil {
			fmt.Printf("  %s: remote not found\n", e.Ref)
			continue
		}

		fetcher, err := remote.NewFetcher(rem.URL, auth)
		if err != nil {
			continue
		}

		owner, repo, err := remote.ParseRepoURL(rem.URL)
		if err != nil {
			continue
		}

		branch, err := fetcher.GetDefaultBranch(cmd.Context(), owner, repo)
		if err != nil {
			continue
		}

		latestSHA, err := fetcher.ResolveRef(cmd.Context(), owner, repo, branch)
		if err != nil {
			continue
		}

		if latestSHA != e.Entry.SHA {
			info := updateItem{
				Type:       e.Type,
				Ref:        e.Ref,
				CurrentSHA: e.Entry.SHA,
				LatestSHA:  latestSHA,
			}
			// Separate profiles from bundles
			if e.Type == remote.ItemTypeProfile {
				profileUpdates = append(profileUpdates, info)
			} else {
				bundleUpdates = append(bundleUpdates, info)
			}
		}
	}

	// Handle entries with empty SHAs
	if len(emptyEntries) > 0 {
		if updateCleanupFlag {
			fmt.Printf("Cleaning up %d entries with empty SHA...\n", len(emptyEntries))
			for _, entry := range emptyEntries {
				// Remove local file if it exists
				localPath := filepath.Join(".ctxloom", entry.Type.DirName(), strings.Replace(entry.Ref, "/", string(filepath.Separator), 1)+".yaml")
				if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
					fmt.Printf("  Warning: failed to remove %s: %v\n", localPath, err)
				}
				// Remove from lockfile
				lockfile.RemoveEntry(entry.Type, entry.Ref)
			}
			// Save updated lockfile
			if err := lockManager.Save(lockfile); err != nil {
				fmt.Printf("  Warning: failed to update lockfile: %v\n", err)
			} else {
				fmt.Printf("  Removed %d entries with empty SHA from lockfile\n\n", len(emptyEntries))
			}
		} else {
			fmt.Printf("Found %d entries with empty SHA (use --cleanup to remove)\n\n", len(emptyEntries))
		}
	}

	totalUpdates := len(profileUpdates) + len(bundleUpdates)
	if totalUpdates == 0 {
		fmt.Println("All items are up to date!")
		// Still check for bundle issues even when no updates
		return checkBundleIssues(lockfile, updateCleanupFlag)
	}

	fmt.Printf("Found %d items with updates available:\n\n", totalUpdates)

	if len(profileUpdates) > 0 {
		fmt.Println("Profiles:")
		for _, u := range profileUpdates {
			fmt.Printf("  %s\n", u.Ref)
			fmt.Printf("    Current: %s → Latest: %s\n", formatSHA(u.CurrentSHA), formatSHA(u.LatestSHA))
		}
	}
	if len(bundleUpdates) > 0 {
		fmt.Println("Bundles:")
		for _, u := range bundleUpdates {
			fmt.Printf("  %s\n", u.Ref)
			fmt.Printf("    Current: %s → Latest: %s\n", formatSHA(u.CurrentSHA), formatSHA(u.LatestSHA))
		}
	}

	if updateDryRunFlag {
		fmt.Println("\nRun without --dry-run to apply updates.")
		return nil
	}

	// Apply updates - profiles first, then bundles
	fmt.Println("\nApplying updates...")
	puller := remote.NewPuller(registry, auth)

	updated := 0
	failed := 0
	var removedFromRemote []updateItem

	// Update profiles first (they may change bundle references)
	if len(profileUpdates) > 0 {
		fmt.Println("\n--- Updating profiles first ---")
		for _, u := range profileUpdates {
			fmt.Printf("\nUpdating %s...\n", u.Ref)

			opts := remote.PullOptions{
				ItemType: u.Type,
				Force:    updateForceFlag,
				Cascade:  true, // Pull new bundles referenced by profile
			}

			result, err := puller.Pull(cmd.Context(), u.Ref, opts)
			if err != nil {
				if strings.Contains(err.Error(), "cancelled") {
					fmt.Println("  Skipped")
				} else if strings.Contains(err.Error(), "file not found") || strings.Contains(err.Error(), "404") {
					fmt.Println("  Removed from remote (no longer exists)")
					removedFromRemote = append(removedFromRemote, u)
				} else {
					fmt.Printf("  Error: %v\n", err)
					failed++
				}
				continue
			}

			fmt.Printf("  Updated to %s\n", formatSHA(result.SHA))
			updated++
		}
	}

	// Update bundles
	if len(bundleUpdates) > 0 {
		fmt.Println("\n--- Updating bundles ---")
		for _, u := range bundleUpdates {
			fmt.Printf("\nUpdating %s...\n", u.Ref)

			opts := remote.PullOptions{
				ItemType: u.Type,
				Force:    updateForceFlag,
			}

			result, err := puller.Pull(cmd.Context(), u.Ref, opts)
			if err != nil {
				if strings.Contains(err.Error(), "cancelled") {
					fmt.Println("  Skipped")
				} else if strings.Contains(err.Error(), "file not found") || strings.Contains(err.Error(), "404") {
					fmt.Println("  Removed from remote (no longer exists)")
					removedFromRemote = append(removedFromRemote, u)
				} else {
					fmt.Printf("  Error: %v\n", err)
					failed++
				}
				continue
			}

			fmt.Printf("  Updated to %s\n", formatSHA(result.SHA))
			updated++
		}
	}

	// Handle items removed from remote
	if len(removedFromRemote) > 0 {
		fmt.Printf("\n--- Items removed from remote ---\n")
		fmt.Println("The following items no longer exist on the remote:")
		for _, item := range removedFromRemote {
			fmt.Printf("  - %s %s\n", item.Type, item.Ref)
		}

		if updateCleanupFlag {
			fmt.Println("\nCleaning up local files...")
			cleaned := 0
			for _, item := range removedFromRemote {
				// Delete local file
				localPath := filepath.Join(".ctxloom", item.Type.DirName(), strings.Replace(item.Ref, "/", string(filepath.Separator), 1)+".yaml")
				if err := os.Remove(localPath); err != nil {
					if !os.IsNotExist(err) {
						fmt.Printf("  Warning: failed to remove %s: %v\n", localPath, err)
					}
				} else {
					fmt.Printf("  Removed: %s\n", localPath)
					cleaned++
				}

				// Remove from lockfile
				lockfile.RemoveEntry(item.Type, item.Ref)
			}

			// Save updated lockfile
			if cleaned > 0 {
				if err := lockManager.Save(lockfile); err != nil {
					fmt.Printf("  Warning: failed to update lockfile: %v\n", err)
				} else {
					fmt.Printf("  Updated lockfile (removed %d entries)\n", cleaned)
				}
			}
		} else {
			fmt.Println("\nUse --cleanup to remove these local files automatically.")
		}
	}

	fmt.Printf("\nUpdated: %d, Failed: %d\n", updated, failed)

	return checkBundleIssues(lockfile, updateCleanupFlag)
}

// checkBundleIssues analyzes bundle references and optionally cleans up broken ones.
func checkBundleIssues(lockfile *remote.Lockfile, cleanup bool) error {
	analysis := analyzeUpdateBundleReferences(lockfile)

	// Show warnings first
	for _, warn := range analysis.Warnings {
		fmt.Printf("Warning: %s\n", warn)
	}

	// Show invalid references
	if len(analysis.Invalid) > 0 {
		fmt.Printf("\n--- Invalid bundle references ---\n")
		fmt.Println("The following bundle references are malformed:")
		for _, inv := range analysis.Invalid {
			fmt.Printf("  - %s\n", inv)
		}
	}

	// Show missing bundles
	if len(analysis.Missing) > 0 {
		fmt.Printf("\n--- Missing bundles ---\n")
		fmt.Println("The following bundles are referenced but not installed:")
		for _, missing := range analysis.Missing {
			fmt.Printf("  - %s\n", missing)
		}

		if cleanup && len(analysis.MissingByFile) > 0 {
			fmt.Println("\nCleaning up broken bundle references from profiles...")
			cleaned, err := cleanupMissingBundleReferences(analysis.MissingByFile)
			if err != nil {
				fmt.Printf("  Warning: %v\n", err)
			} else if cleaned > 0 {
				fmt.Printf("  Removed %d broken references from profiles\n", cleaned)
			}
		} else if !cleanup {
			fmt.Println("\nUse --cleanup to remove these broken references, or install with: ctxloom install bundle <name>")
		}
	}

	// Show orphaned bundles
	if len(analysis.Orphans) > 0 {
		fmt.Printf("\n--- Orphaned bundles ---\n")
		fmt.Println("The following bundles are no longer referenced by any profile:")
		for _, orphan := range analysis.Orphans {
			fmt.Printf("  - %s\n", orphan)
		}
		fmt.Println("\nTo remove orphaned bundles, delete them manually from .ctxloom/bundles/")
		fmt.Println("Then run 'ctxloom remote lock' to update the lockfile.")
	}

	return nil
}

// updateBundleAnalysis contains the results of analyzing bundle references.
type updateBundleAnalysis struct {
	Orphans       []string            // Bundles in lockfile but not referenced by any profile
	Missing       []string            // Bundles referenced by profiles but not in lockfile
	Invalid       []string            // Invalid bundle references that couldn't be parsed
	Warnings      []string            // Non-fatal warnings encountered during analysis
	MissingByFile map[string][]string // Map of profile path -> missing bundle references (for cleanup)
}

// analyzeUpdateBundleReferences checks for orphaned, missing, and invalid bundle references.
func analyzeUpdateBundleReferences(lockfile *remote.Lockfile) *updateBundleAnalysis {
	result := &updateBundleAnalysis{
		MissingByFile: make(map[string][]string),
	}

	// Collect all bundle references from all profiles
	referencedBundles := make(map[string]bool)
	// Track which bundles are referenced by which profile files (for cleanup)
	bundlesByFile := make(map[string]map[string]bool)

	// Scan local profile files
	profileDir := filepath.Join(".ctxloom", "profiles")
	err := filepath.Walk(profileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Warn but continue walking
			result.Warnings = append(result.Warnings, fmt.Sprintf("error accessing %s: %v", path, err))
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("error reading %s: %v", path, err))
			return nil
		}

		var profile struct {
			Bundles []string `yaml:"bundles"`
		}
		if err := yaml.Unmarshal(content, &profile); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("invalid YAML in %s: %v", path, err))
			return nil
		}

		if bundlesByFile[path] == nil {
			bundlesByFile[path] = make(map[string]bool)
		}

		for _, bundle := range profile.Bundles {
			if bundle == "" {
				continue
			}

			// Strip any item path suffix (e.g., #fragments/name)
			bundleRef := bundle
			if idx := strings.Index(bundle, "#"); idx != -1 {
				bundleRef = bundle[:idx]
			}

			// Validate the reference format (should contain "/")
			if !strings.Contains(bundleRef, "/") {
				result.Invalid = append(result.Invalid, fmt.Sprintf("%s (in %s)", bundle, filepath.Base(path)))
				continue
			}

			// Normalize canonical URLs to local names for comparison
			localRef, err := remote.ToLocalRef(bundleRef)
			if err != nil {
				result.Invalid = append(result.Invalid, fmt.Sprintf("%s (in %s): %v", bundle, filepath.Base(path), err))
				continue
			}

			referencedBundles[localRef] = true
			bundlesByFile[path][bundle] = true // Keep original reference for cleanup
		}
		return nil
	})
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("error walking profiles directory: %v", err))
	}

	// Find bundles in lockfile not referenced by any profile (orphans)
	for ref := range lockfile.Bundles {
		if !referencedBundles[ref] {
			result.Orphans = append(result.Orphans, ref)
		}
	}

	// Find bundles referenced by profiles but not in lockfile (missing)
	// Also track which profile files have missing references
	missingSet := make(map[string]bool)
	for ref := range referencedBundles {
		if _, exists := lockfile.Bundles[ref]; !exists {
			// Check if the bundle file exists locally at the local name path
			bundlePath := filepath.Join(".ctxloom", "bundles", strings.Replace(ref, "/", string(filepath.Separator), 1)+".yaml")
			if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
				missingSet[ref] = true
				result.Missing = append(result.Missing, ref)
			}
		}
	}

	// Build MissingByFile map - track original bundle strings that are missing
	for path, bundles := range bundlesByFile {
		for bundle := range bundles {
			// Normalize to check if it's missing
			bundleRef := bundle
			if idx := strings.Index(bundle, "#"); idx != -1 {
				bundleRef = bundle[:idx]
			}
			localRef, err := remote.ToLocalRef(bundleRef)
			if err != nil {
				continue
			}
			if missingSet[localRef] {
				result.MissingByFile[path] = append(result.MissingByFile[path], bundle)
			}
		}
	}

	return result
}

// cleanupMissingBundleReferences removes missing bundle references from profile files.
func cleanupMissingBundleReferences(missingByFile map[string][]string) (int, error) {
	cleaned := 0

	for path, missingBundles := range missingByFile {
		content, err := os.ReadFile(path)
		if err != nil {
			return cleaned, fmt.Errorf("failed to read %s: %w", path, err)
		}

		// Parse the profile
		var profile map[string]interface{}
		if err := yaml.Unmarshal(content, &profile); err != nil {
			return cleaned, fmt.Errorf("failed to parse %s: %w", path, err)
		}

		// Get current bundles
		bundlesRaw, ok := profile["bundles"]
		if !ok {
			continue
		}

		bundlesList, ok := bundlesRaw.([]interface{})
		if !ok {
			continue
		}

		// Build set of bundles to remove
		toRemove := make(map[string]bool)
		for _, b := range missingBundles {
			toRemove[b] = true
		}

		// Filter out missing bundles
		var newBundles []interface{}
		removedCount := 0
		for _, b := range bundlesList {
			bundleStr, ok := b.(string)
			if !ok {
				newBundles = append(newBundles, b)
				continue
			}
			if toRemove[bundleStr] {
				removedCount++
				continue
			}
			newBundles = append(newBundles, b)
		}

		if removedCount == 0 {
			continue
		}

		// Update bundles in profile
		profile["bundles"] = newBundles

		// Write back
		newContent, err := yaml.Marshal(profile)
		if err != nil {
			return cleaned, fmt.Errorf("failed to marshal %s: %w", path, err)
		}

		if err := os.WriteFile(path, newContent, 0644); err != nil {
			return cleaned, fmt.Errorf("failed to write %s: %w", path, err)
		}

		cleaned += removedCount
		fmt.Printf("  Cleaned %d broken references from %s\n", removedCount, filepath.Base(path))
	}

	return cleaned, nil
}

func formatSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func init() {
	rootCmd.AddCommand(updateCmd)

	updateCmd.Flags().BoolVar(&updateDryRunFlag, "dry-run", false,
		"Show available updates without applying")
	updateCmd.Flags().BoolVarP(&updateForceFlag, "force", "f", false,
		"Skip confirmation prompts when applying updates")
	updateCmd.Flags().BoolVar(&updateCleanupFlag, "cleanup", false,
		"Remove local files for items deleted from remote")
}
