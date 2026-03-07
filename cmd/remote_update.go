package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/remote"
)

var updateApply bool

var remoteUpdateCmd = &cobra.Command{
	Use:   "update [reference]",
	Short: "Check for and apply updates to remote items",
	Long: `Check for updates to installed remote items.

Without arguments, checks all items in the lockfile for updates.
With a reference, checks only that specific item.

Examples:
  scm remote update                       # Check all for updates
  scm remote update alice/security        # Check specific item
  scm remote update --apply               # Apply all available updates
  scm remote update alice/security --apply # Update specific item`,
	RunE: runRemoteUpdate,
}

func runRemoteUpdate(cmd *cobra.Command, args []string) error {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	auth := remote.LoadAuth("")
	lockManager := remote.NewLockfileManager(".scm")

	// If specific reference provided, update just that
	if len(args) > 0 {
		return updateSingle(cmd, args[0], registry, auth, lockManager)
	}

	// Otherwise, check lockfile
	return updateAll(cmd, registry, auth, lockManager)
}

func updateSingle(cmd *cobra.Command, refStr string, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
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
		fmt.Printf("%s is up to date (SHA: %s)\n", refStr, shortSHA(latestSHA))
		return nil
	default:
		fmt.Printf("%s has update available:\n", refStr)
		fmt.Printf("  Current: %s\n", shortSHA(currentSHA))
		fmt.Printf("  Latest:  %s\n", shortSHA(latestSHA))
	}

	if !updateApply {
		fmt.Println("\nRun with --apply to update.")
		return nil
	}

	// Apply update
	puller := remote.NewPuller(registry, auth)
	opts := remote.PullOptions{
		ItemType: itemType,
	}

	result, err := puller.Pull(cmd.Context(), refStr, opts)
	if err != nil {
		return err
	}

	fmt.Printf("\nUpdated %s → %s\n", refStr, shortSHA(result.SHA))
	return nil
}

func updateAll(cmd *cobra.Command, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Generate one with: scm remote lock")
		return nil
	}

	entries := lockfile.AllEntries()
	fmt.Printf("Checking %d items for updates...\n\n", len(entries))

	type updateInfo struct {
		Type      remote.ItemType
		Ref       string
		CurrentSHA string
		LatestSHA  string
	}

	var updates []updateInfo

	for _, e := range entries {
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
			updates = append(updates, updateInfo{
				Type:       e.Type,
				Ref:        e.Ref,
				CurrentSHA: e.Entry.SHA,
				LatestSHA:  latestSHA,
			})
		}
	}

	if len(updates) == 0 {
		fmt.Println("All items are up to date!")
		return nil
	}

	fmt.Printf("Found %d items with updates available:\n\n", len(updates))

	for _, u := range updates {
		fmt.Printf("  %s %s\n", u.Type, u.Ref)
		fmt.Printf("    Current: %s → Latest: %s\n", shortSHA(u.CurrentSHA), shortSHA(u.LatestSHA))
	}

	if !updateApply {
		fmt.Println("\nRun with --apply to update all items.")
		return nil
	}

	// Apply updates
	fmt.Println("\nApplying updates...")
	puller := remote.NewPuller(registry, auth)

	updated := 0
	failed := 0

	for _, u := range updates {
		fmt.Printf("\nUpdating %s...\n", u.Ref)

		opts := remote.PullOptions{
			ItemType: u.Type,
		}

		result, err := puller.Pull(cmd.Context(), u.Ref, opts)
		if err != nil {
			if strings.Contains(err.Error(), "cancelled") {
				fmt.Println("  Skipped")
			} else {
				fmt.Printf("  Error: %v\n", err)
				failed++
			}
			continue
		}

		fmt.Printf("  Updated to %s\n", shortSHA(result.SHA))
		updated++
	}

	fmt.Printf("\nUpdated: %d, Failed: %d\n", updated, failed)

	// Regenerate lockfile
	fmt.Println("\nRun 'scm remote lock' to update the lockfile.")

	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func init() {
	remoteCmd.AddCommand(remoteUpdateCmd)

	remoteUpdateCmd.Flags().BoolVar(&updateApply, "apply", false,
		"Apply available updates")
}
