package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

var updateApply bool
var updateForce bool
var updateCleanup bool
var updateBlind bool

var remoteUpdateCmd = &cobra.Command{
	Use:   "update [reference]",
	Short: "Check for and apply updates to remote items",
	Long: `Check for updates to installed remote items.

Without arguments, checks all items in the lockfile for updates.
With a reference, checks only that specific item.

Examples:
  ctxloom remote update                       # Check all for updates
  ctxloom remote update alice/security        # Check specific item
  ctxloom remote update --apply               # Apply all available updates
  ctxloom remote update alice/security --apply # Update specific item
  ctxloom remote update --apply --force       # Apply all updates without prompts
  ctxloom remote update --apply --cleanup     # Also remove items deleted from remote`,
	RunE: runRemoteUpdate,
}

func runRemoteUpdate(cmd *cobra.Command, args []string) error {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	auth := remote.LoadAuth("")
	lockManager := remote.NewLockfileManager(".ctxloom")

	cfg := loadConfigOrFallback(GetConfig, os.Stderr)

	// If specific reference provided, update just that
	if len(args) > 0 {
		return updateSingle(cmd, cfg, args[0], registry, auth, lockManager)
	}

	// Otherwise, check lockfile
	return updateAll(cmd, cfg, registry, auth, lockManager)
}

func updateSingle(cmd *cobra.Command, cfg *config.Config, refStr string, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	ref, err := remote.ParseReference(refStr)
	if err != nil {
		return fmt.Errorf("invalid reference: %w", err)
	}

	rem, err := registry.Get(ref.Remote)
	if err != nil {
		return err
	}

	// Refresh the local clone so we can detect updates.
	cache := operations.NewRepoCache(cfg)
	if forgeType, _, ferr := remote.DetectForge(rem.URL); ferr == nil {
		if _, uerr := cache.UpdateRepo(cmd.Context(), rem.URL, forgeType); uerr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: fetch %s: %v\n", rem.URL, uerr)
		}
	}

	fetcher, err := operations.GetCachedFetcher(cfg, rem.URL)
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
	puller := remote.NewPuller(registry, auth, remote.WithFetcherFactory(operations.NewCachedFetcherFactory(cfg)))
	opts := remote.PullOptions{
		ItemType: itemType,
		Force:    updateForce,
		Blind:    updateBlind,
	}

	result, err := puller.Pull(cmd.Context(), refStr, opts)
	if err != nil {
		return err
	}

	fmt.Printf("\nUpdated %s → %s\n", refStr, shortSHA(result.SHA))
	return nil
}

func updateAll(cmd *cobra.Command, cfg *config.Config, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Generate one with: ctxloom remote lock")
		return nil
	}

	fmt.Printf("Checking %d items for updates...\n\n", len(lockfile.AllEntries()))

	// Refresh every unique remote once (one git fetch per repo, not 2×N API
	// calls), then resolve the latest SHA for each entry.
	refreshRemoteRepos(cmd.Context(), cfg, registry, lockfile)
	profileUpdates, bundleUpdates, skippedEmpty := detectUpdates(cmd.Context(), os.Stdout, cfg, registry, auth, lockfile)

	if skippedEmpty > 0 {
		fmt.Printf("Skipped %d entries with empty SHA (run 'ctxloom remote lock' to clean up)\n\n", skippedEmpty)
	}

	totalUpdates := len(profileUpdates) + len(bundleUpdates)
	if totalUpdates == 0 {
		fmt.Println("All items are up to date!")
		return nil
	}

	fmt.Printf("Found %d items with updates available:\n\n", totalUpdates)

	printAvailableUpdates(os.Stdout, profileUpdates, bundleUpdates)

	if !updateApply {
		fmt.Println("\nRun with --apply to update all items.")
		return nil
	}

	// Apply updates — profiles first (they may change bundle references), then bundles.
	fmt.Println("\nApplying updates...")
	puller := remote.NewPuller(registry, auth, remote.WithFetcherFactory(operations.NewCachedFetcherFactory(cfg)))
	updated, failed, removedFromRemote := applyUpdates(cmd.Context(), os.Stdout, puller, profileUpdates, bundleUpdates)

	reportRemovedFromRemote(os.Stdout, afero.NewOsFs(), ".ctxloom", removedFromRemote, lockfile, lockManager)

	fmt.Printf("\nUpdated: %d, Failed: %d\n", updated, failed)

	// Bundle-reference issues (orphans/missing/invalid) only matter when a
	// profile changed and may have altered its bundle list.
	if len(profileUpdates) > 0 {
		reportBundleIssues(os.Stdout, analyzeBundleReferences(lockfile))
	}

	reportMissingDefaults(os.Stdout, checkDefaultProfiles())

	fmt.Println("\nRun 'ctxloom remote lock' to update the lockfile.")

	return nil
}

// updateInfo is a single available update detected by detectUpdates.
type updateInfo struct {
	Type       remote.ItemType
	Ref        string
	CurrentSHA string
	LatestSHA  string
}

// pullRunner is the slice of *remote.Puller that the apply phase needs, declared
// here (consumer side) so applyUpdates can be unit-tested with a fake.
type pullRunner interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// refreshRemoteRepos fetches each unique remote git repo once so subsequent ref
// resolution reads from a fresh clone. Best-effort: every failure is warned to
// stderr and skipped — a stale repo just risks missing an update, never a crash.
func refreshRemoteRepos(ctx context.Context, cfg *config.Config, registry *remote.Registry, lockfile *remote.Lockfile) {
	cache := operations.NewRepoCache(cfg)
	fetched := map[string]struct{}{}
	for _, e := range lockfile.AllEntries() {
		if e.Entry.SHA == "" {
			continue
		}
		ref, err := remote.ParseReference(e.Ref)
		if err != nil {
			continue
		}
		rem, err := registry.Get(ref.Remote)
		if err != nil {
			continue
		}
		if _, ok := fetched[rem.URL]; ok {
			continue
		}
		fetched[rem.URL] = struct{}{}
		forgeType, _, ferr := remote.DetectForge(rem.URL)
		if ferr != nil {
			continue
		}
		if _, uerr := cache.UpdateRepo(ctx, rem.URL, forgeType); uerr != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: fetch %s: %v\n", rem.URL, uerr)
		}
	}
}

// detectUpdates resolves the latest SHA for every lockfile entry and buckets the
// changed ones into profile vs bundle updates. Entries with an empty SHA are
// counted in skipped and not checked; per-entry resolution failures are skipped
// (the refresh pass already surfaced fetch errors). "remote not found" is
// reported because it signals a misconfigured registry, not a transient error.
func detectUpdates(ctx context.Context, out io.Writer, cfg *config.Config, registry *remote.Registry, auth remote.AuthConfig, lockfile *remote.Lockfile) (profileUpdates, bundleUpdates []updateInfo, skipped int) {
	cachedFactory := operations.NewCachedFetcherFactory(cfg)
	fetcherByURL := map[string]remote.Fetcher{}
	fetcherFor := func(url string) (remote.Fetcher, error) {
		if f, ok := fetcherByURL[url]; ok {
			return f, nil
		}
		f, err := cachedFactory(url, auth)
		if err != nil {
			return nil, err
		}
		fetcherByURL[url] = f
		return f, nil
	}

	for _, e := range lockfile.AllEntries() {
		if e.Entry.SHA == "" {
			skipped++
			continue
		}
		ref, err := remote.ParseReference(e.Ref)
		if err != nil {
			continue
		}
		rem, err := registry.Get(ref.Remote)
		if err != nil {
			fmt.Fprintf(out, "  %s: remote not found\n", e.Ref)
			continue
		}
		fetcher, err := fetcherFor(rem.URL)
		if err != nil {
			continue
		}
		latest, ok := latestRemoteSHA(ctx, fetcher, rem.URL)
		if !ok || latest == e.Entry.SHA {
			continue
		}
		info := updateInfo{Type: e.Type, Ref: e.Ref, CurrentSHA: e.Entry.SHA, LatestSHA: latest}
		if e.Type == remote.ItemTypeProfile {
			profileUpdates = append(profileUpdates, info)
		} else {
			bundleUpdates = append(bundleUpdates, info)
		}
	}
	return profileUpdates, bundleUpdates, skipped
}

// latestRemoteSHA returns the SHA of the remote's default branch, or ok=false if
// any step fails. Split out so detectUpdates stays flat.
func latestRemoteSHA(ctx context.Context, fetcher remote.Fetcher, url string) (sha string, ok bool) {
	owner, repo, err := remote.ParseRepoURL(url)
	if err != nil {
		return "", false
	}
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return "", false
	}
	sha, err = fetcher.ResolveRef(ctx, owner, repo, branch)
	if err != nil {
		return "", false
	}
	return sha, true
}

// printAvailableUpdates lists pending profile and bundle updates; empty sections
// are omitted.
func printAvailableUpdates(out io.Writer, profileUpdates, bundleUpdates []updateInfo) {
	if len(profileUpdates) > 0 {
		fmt.Fprintln(out, "Profiles:")
		for _, u := range profileUpdates {
			fmt.Fprintf(out, "  %s\n", u.Ref)
			fmt.Fprintf(out, "    Current: %s → Latest: %s\n", shortSHA(u.CurrentSHA), shortSHA(u.LatestSHA))
		}
	}
	if len(bundleUpdates) > 0 {
		fmt.Fprintln(out, "Bundles:")
		for _, u := range bundleUpdates {
			fmt.Fprintf(out, "  %s\n", u.Ref)
			fmt.Fprintf(out, "    Current: %s → Latest: %s\n", shortSHA(u.CurrentSHA), shortSHA(u.LatestSHA))
		}
	}
}

// applyUpdates pulls profile updates first (Cascade=true, since a profile may
// newly reference bundles) then bundle updates, returning counts plus the items
// the remote no longer has (for cleanup). Per-item errors are classified and
// reported, never fatal.
func applyUpdates(ctx context.Context, out io.Writer, p pullRunner, profileUpdates, bundleUpdates []updateInfo) (updated, failed int, removed []updateInfo) {
	pu, pf, pr := applyUpdateBatch(ctx, out, p, "\n--- Updating profiles first ---", profileUpdates, true)
	bu, bf, br := applyUpdateBatch(ctx, out, p, "\n--- Updating bundles ---", bundleUpdates, false)
	removed = append(pr, br...)
	return pu + bu, pf + bf, removed
}

// applyUpdateBatch pulls one batch under a header. cascade is set on profile
// pulls so their newly-referenced bundles come along.
func applyUpdateBatch(ctx context.Context, out io.Writer, p pullRunner, header string, updates []updateInfo, cascade bool) (updated, failed int, removed []updateInfo) {
	if len(updates) == 0 {
		return 0, 0, nil
	}
	fmt.Fprintln(out, header)
	for _, u := range updates {
		fmt.Fprintf(out, "\nUpdating %s...\n", u.Ref)
		result, err := p.Pull(ctx, u.Ref, remote.PullOptions{
			ItemType: u.Type,
			Force:    updateForce,
			Blind:    updateBlind,
			Cascade:  cascade,
		})
		if err != nil {
			switch classifyPullError(err) {
			case pullOutcomeSkipped:
				fmt.Fprintln(out, "  Skipped")
			case pullOutcomeRemoved:
				fmt.Fprintln(out, "  Removed from remote (no longer exists)")
				removed = append(removed, u)
			case pullOutcomeFailed:
				fmt.Fprintf(out, "  Error: %v\n", err)
				failed++
			}
			continue
		}
		fmt.Fprintf(out, "  Updated to %s\n", shortSHA(result.SHA))
		updated++
	}
	return updated, failed, removed
}

// reportRemovedFromRemote lists items the remote dropped and, when --cleanup is
// set, deletes their local files (under appDir on fs) and prunes the lockfile.
// Without --cleanup it just prints a hint. Removal failures are warned, never
// fatal. fs/appDir are seam'd so the cleanup branch is testable against a
// MemMapFs (production passes afero.NewOsFs() and ".ctxloom").
func reportRemovedFromRemote(out io.Writer, fs afero.Fs, appDir string, removed []updateInfo, lockfile *remote.Lockfile, lockManager *remote.LockfileManager) {
	if len(removed) == 0 {
		return
	}
	fmt.Fprintf(out, "\n--- Items removed from remote ---\n")
	fmt.Fprintln(out, "The following items no longer exist on the remote:")
	for _, item := range removed {
		fmt.Fprintf(out, "  - %s %s\n", item.Type, item.Ref)
	}

	if !updateCleanup {
		fmt.Fprintln(out, "\nUse --cleanup to remove these local files automatically.")
		return
	}

	fmt.Fprintln(out, "\nCleaning up local files...")
	cleaned := 0
	for _, item := range removed {
		localPath := filepath.Join(appDir, item.Type.DirName(), strings.Replace(item.Ref, "/", string(filepath.Separator), 1)+".yaml")
		if err := fs.Remove(localPath); err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(out, "  Warning: failed to remove %s: %v\n", localPath, err)
			}
		} else {
			fmt.Fprintf(out, "  Removed: %s\n", localPath)
			cleaned++
		}
		lockfile.RemoveEntry(item.Type, item.Ref)
	}

	if cleaned > 0 {
		if err := lockManager.Save(lockfile); err != nil {
			fmt.Fprintf(out, "  Warning: failed to update lockfile: %v\n", err)
		} else {
			fmt.Fprintf(out, "  Updated lockfile (removed %d entries)\n", cleaned)
		}
	}
}

// reportBundleIssues prints orphan/missing/invalid bundle findings plus any
// non-fatal warnings. Silent when there is nothing to report.
func reportBundleIssues(out io.Writer, analysis *bundleAnalysis) {
	for _, warn := range analysis.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", warn)
	}
	if len(analysis.Invalid) > 0 {
		fmt.Fprintf(out, "\n--- Invalid bundle references ---\n")
		fmt.Fprintln(out, "The following bundle references are malformed:")
		for _, inv := range analysis.Invalid {
			fmt.Fprintf(out, "  - %s\n", inv)
		}
	}
	if len(analysis.Missing) > 0 {
		fmt.Fprintf(out, "\n--- Missing bundles ---\n")
		fmt.Fprintln(out, "The following bundles are referenced but not installed:")
		for _, missing := range analysis.Missing {
			fmt.Fprintf(out, "  - %s\n", missing)
		}
		fmt.Fprintln(out, "\nPull missing bundles with: ctxloom remote bundles pull <name>")
	}
	if len(analysis.Orphans) > 0 {
		fmt.Fprintf(out, "\n--- Orphaned bundles ---\n")
		fmt.Fprintln(out, "The following bundles are no longer referenced by any profile:")
		for _, orphan := range analysis.Orphans {
			fmt.Fprintf(out, "  - %s\n", orphan)
		}
		fmt.Fprintln(out, "\nTo remove orphaned bundles, delete them manually from .ctxloom/bundles/")
		fmt.Fprintln(out, "Then run 'ctxloom remote lock' to update the lockfile.")
	}
}

// reportMissingDefaults warns about configured default profiles that don't
// exist. Silent when there are none.
func reportMissingDefaults(out io.Writer, missing []string) {
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(out, "\n--- Nonexistent default profiles ---\n")
	fmt.Fprintln(out, "The following default profiles do not exist:")
	for _, name := range missing {
		fmt.Fprintf(out, "  - %s\n", name)
	}
	fmt.Fprintln(out, "\nUpdate your ctxloom.yaml to fix the defaults.profiles list.")
}

// bundleAnalysis contains the results of analyzing bundle references.
type bundleAnalysis struct {
	Orphans  []string // Bundles in lockfile but not referenced by any profile
	Missing  []string // Bundles referenced by profiles but not in lockfile
	Invalid  []string // Invalid bundle references that couldn't be parsed
	Warnings []string // Non-fatal warnings encountered during analysis
}

// analyzeBundleReferences checks for orphaned, missing, and invalid bundle
// references against the active lockfile. Production callers use this form
// (real OS filesystem rooted at .ctxloom); tests should call
// analyzeBundleReferencesWithFS directly so they can inject an afero
// MemMapFs and an arbitrary base dir.
func analyzeBundleReferences(lockfile *remote.Lockfile) *bundleAnalysis {
	return analyzeBundleReferencesWithFS(lockfile, afero.NewOsFs(), ".ctxloom")
}

// analyzeBundleReferencesWithFS is the seam'd implementation. fs is the
// filesystem to walk; appDir is the base under which "profiles/" and
// "bundles/" subdirs live. Always returns a non-nil result; partial
// failures land in Warnings so the caller can surface them.
func analyzeBundleReferencesWithFS(lockfile *remote.Lockfile, fs afero.Fs, appDir string) *bundleAnalysis {
	result := &bundleAnalysis{}

	// Collect all bundle references from all profiles
	referencedBundles := make(map[string]bool)

	// Scan local profile files
	profileDir := filepath.Join(appDir, "profiles")
	err := afero.Walk(fs, profileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Warn but continue walking
			result.Warnings = append(result.Warnings, fmt.Sprintf("error accessing %s: %v", path, err))
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		content, err := afero.ReadFile(fs, path)
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

		for _, bundle := range profile.Bundles {
			if bundle == "" {
				continue
			}

			// Strip any item path suffix (e.g., #fragments/name)
			bundleRef, _, _ := strings.Cut(bundle, "#")

			// Validate the reference format (should contain "/")
			if !strings.Contains(bundleRef, "/") {
				result.Invalid = append(result.Invalid, fmt.Sprintf("%s (in %s)", bundle, filepath.Base(path)))
				continue
			}

			// Normalize canonical URLs to local names for comparison
			// e.g., https://github.com/owner/ctxloom-github@v1/bundles/name -> ctxloom-github/name
			ref, err := remote.ParseReference(bundleRef)
			if err == nil && ref.IsCanonical {
				bundleRef = ref.ToLocalName()
			}

			referencedBundles[bundleRef] = true
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
	for ref := range referencedBundles {
		if _, exists := lockfile.Bundles[ref]; !exists {
			// Check if the bundle file exists locally at the local name path
			bundlePath := filepath.Join(appDir, "bundles", strings.Replace(ref, "/", string(filepath.Separator), 1)+".yaml")
			if _, err := fs.Stat(bundlePath); os.IsNotExist(err) {
				result.Missing = append(result.Missing, ref)
			}
		}
	}

	return result
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// pullOutcome describes how a per-item Pull error should be reported.
// Extracted so the (user-visible) classification rules can be unit-tested
// without spinning up a Puller. The matching is intentionally substring-
// based — the upstream errors flow through several layers of wrapping
// (forge client → fetcher → puller) and don't carry a stable typed value
// we can errors.Is on. If forge error shapes change, this is the one
// place to update.
type pullOutcome int

const (
	pullOutcomeFailed  pullOutcome = iota // generic error, count as failure
	pullOutcomeSkipped                    // user-cancelled prompt or context cancel
	pullOutcomeRemoved                    // remote no longer has this ref/file
)

func classifyPullError(err error) pullOutcome {
	if err == nil {
		return pullOutcomeFailed // caller shouldn't ask in this case
	}
	if errors.Is(err, errs.ErrCancelled) {
		return pullOutcomeSkipped
	}
	if errors.Is(err, errs.ErrRemoteContentNotFound) {
		return pullOutcomeRemoved
	}
	return pullOutcomeFailed
}

// checkDefaultProfiles returns names of default profiles that don't exist.
func checkDefaultProfiles() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil // Can't check if config won't load
	}

	defaultProfiles := cfg.GetDefaultProfiles()
	if len(defaultProfiles) == 0 {
		return nil
	}

	var missing []string
	profileLoader := cfg.GetProfileLoader()

	for _, name := range defaultProfiles {
		// Check if profile exists in config
		if _, exists := cfg.Profiles[name]; exists {
			continue
		}

		// Check if profile exists as a file
		_, err := profileLoader.Load(name)
		if err != nil {
			missing = append(missing, name)
		}
	}

	return missing
}

func init() {
	remoteCmd.AddCommand(remoteUpdateCmd)

	remoteUpdateCmd.Flags().BoolVar(&updateApply, "apply", false,
		"Apply available updates")
	remoteUpdateCmd.Flags().BoolVar(&updateForce, "force", false,
		"Skip confirmation prompts when applying updates")
	remoteUpdateCmd.Flags().BoolVar(&updateBlind, "blind", false,
		"Skip security review display (implies --force)")
	remoteUpdateCmd.Flags().BoolVar(&updateCleanup, "cleanup", false,
		"Remove local files for items deleted from remote")
}
