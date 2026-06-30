package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	cfg := loadConfigOrFallback(GetConfig, os.Stderr)
	lockManager := remote.NewLockfileManager(projectAppDir(cfg))

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

	// Canonical refs carry the repo URL directly.
	if ref.URL == "" {
		return fmt.Errorf("reference has no repository URL: %s", refStr)
	}

	refreshRemoteClone(cmd.Context(), cfg, ref.URL)

	fetcher, err := operations.GetCachedFetcher(cfg, ref.URL)
	if err != nil {
		return fmt.Errorf("failed to create fetcher: %w", err)
	}

	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	u, upToDate, err := detectSingleUpdate(cmd.Context(), os.Stdout, fetcher, lockfile, refStr)
	if err != nil {
		return err
	}
	if upToDate {
		return nil
	}

	if !updateApply {
		fmt.Println("\nRun with --apply to update.")
		return nil
	}

	// Apply through the same batch machinery as `update --apply`, so the
	// single-ref path shares the constraint-pinned pull (ref@LatestSHA with
	// RequestedVersion carried into the lock) and the removed/skip handling.
	puller := remote.NewPuller(registry, auth, remote.WithFetcherFactory(operations.NewCachedFetcherFactory(cfg)))
	_, failed, removed := applyUpdateBatch(cmd.Context(), os.Stdout, puller, "\n--- Updating ---", []updateInfo{u})
	// Reload after the apply so cleanup prunes from the freshly-pulled lockfile
	// rather than reverting it (see updateAll).
	reloadOK := true
	if reloaded, rerr := lockManager.Load(); rerr == nil {
		lockfile = reloaded
	} else {
		clidiag.Warn("ctxloom", "reload lockfile after apply: %v", rerr)
		reloadOK = false
	}
	reportRemovedFromRemote(os.Stdout, afero.NewOsFs(), projectAppDir(cfg), removed, lockfile, lockManager, reloadOK)
	if failed > 0 {
		return fmt.Errorf("update failed for %s", refStr)
	}
	return nil
}

// detectSingleUpdate resolves one ref's update status against the lockfile,
// constraint-aware: the latest SHA is the newest commit the entry's
// RequestedVersion allows (latestWithinConstraint), never bare default-branch
// HEAD, which can exceed the manifest constraint. The lock entry is found by
// the ref's canonical identity, so a version-suffixed input still matches.
// Prints the status to out; returns the pending update when one exists.
func detectSingleUpdate(ctx context.Context, out io.Writer, fetcher remote.Fetcher, lockfile *remote.Lockfile, refStr string) (updateInfo, bool, error) {
	ref, err := remote.ParseReference(refStr)
	if err != nil || ref.URL == "" {
		return updateInfo{}, false, fmt.Errorf("invalid reference: %s", refStr)
	}
	canonical := ref.CanonicalString()
	entry, itemType := lookupLockedEntry(lockfile, canonical)
	if itemType == "" {
		// Unlocked ref: trust the ref's own type segment (@profiles/ vs
		// @bundles/) rather than assuming bundle — pulling a profile as a
		// bundle would install it under the wrong type. Canonical refs always
		// carry a type; the bundle fallback is defensive only.
		itemType = ref.ItemType
		if itemType == "" {
			clidiag.Warn("ctxloom", "%s does not carry an item type; assuming bundle", refStr)
			itemType = remote.ItemTypeBundle
		}
	}

	latestSHA, ok := latestWithinConstraint(ctx, fetcher, ref.URL, entry.RequestedVersion)
	if !ok {
		return updateInfo{}, false, fmt.Errorf("failed to resolve latest version for %s", refStr)
	}

	itemType, upToDate := reportUpdateStatus(out, refStr, entry.SHA, latestSHA, itemType)
	return updateInfo{
		Type:             itemType,
		Ref:              canonical,
		CurrentSHA:       entry.SHA,
		LatestSHA:        latestSHA,
		RequestedVersion: entry.RequestedVersion,
	}, upToDate, nil
}

// projectAppDir returns the project's .ctxloom dir for lockfile/cleanup paths.
// cfg.AppPaths[0] is resolved by config.Load, which walks up from cwd to the
// project root — so `remote update` works from subdirectories. The bare
// relative name is only the last resort when config resolution found nothing.
func projectAppDir(cfg *config.Config) string {
	if cfg != nil && len(cfg.AppPaths) > 0 && cfg.AppPaths[0] != "" {
		return cfg.AppPaths[0]
	}
	return ".ctxloom"
}

// refreshRemoteClone fetches the latest into the local clone so updates can be
// detected. Fault-tolerant: a fetch failure warns and the stale clone is used.
func refreshRemoteClone(ctx context.Context, cfg *config.Config, repoURL string) {
	cache := operations.NewRepoCache(cfg)
	if forgeType, _, ferr := remote.DetectForge(repoURL); ferr == nil {
		if _, uerr := cache.UpdateRepo(ctx, repoURL, forgeType); uerr != nil {
			clidiag.Warn("ctxloom", "fetch %s: %v", repoURL, uerr)
		}
	}
}

// lookupLockedEntry finds refStr's lock entry and item type, trying bundles
// then profiles. Returns a zero entry and empty type when not present.
func lookupLockedEntry(lockfile *remote.Lockfile, refStr string) (remote.LockEntry, remote.ItemType) {
	for _, it := range []remote.ItemType{remote.ItemTypeBundle, remote.ItemTypeProfile} {
		if entry, ok := lockfile.GetEntry(it, refStr); ok {
			return entry, it
		}
	}
	return remote.LockEntry{}, ""
}

// reportUpdateStatus prints the update status and returns the item type to pull
// plus whether the ref is already up to date. itemType is the caller's resolved
// type (lock entry, falling back to the ref's own type segment) and is passed
// through unchanged — an unlocked profile must still pull as a profile.
func reportUpdateStatus(out io.Writer, refStr, currentSHA, latestSHA string, itemType remote.ItemType) (remote.ItemType, bool) {
	switch currentSHA {
	case "":
		fmt.Fprintf(out, "%s not found in lockfile, checking latest version...\n", refStr)
		return itemType, false
	case latestSHA:
		fmt.Fprintf(out, "%s is up to date (SHA: %s)\n", refStr, shortSHA(latestSHA))
		return itemType, true
	default:
		fmt.Fprintf(out, "%s has update available:\n", refStr)
		fmt.Fprintf(out, "  Current: %s\n", shortSHA(currentSHA))
		fmt.Fprintf(out, "  Latest:  %s\n", shortSHA(latestSHA))
		return itemType, false
	}
}

func updateAll(cmd *cobra.Command, cfg *config.Config, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile.")
		fmt.Println("Generate one with: ctxloom remote pull")
		return nil
	}

	fmt.Printf("Checking %d items for updates...\n\n", len(lockfile.AllEntries()))

	// Refresh every unique remote once (one git fetch per repo, not 2×N API
	// calls), then resolve the latest SHA for each entry.
	refreshRemoteRepos(cmd.Context(), cfg, lockfile)
	profileUpdates, bundleUpdates, skippedEmpty := detectUpdates(cmd.Context(), os.Stdout, cfg, auth, lockfile)

	if skippedEmpty > 0 {
		fmt.Printf("Skipped %d entries with empty SHA (run 'ctxloom remote pull' to clean up)\n\n", skippedEmpty)
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

	// applyUpdates persisted the new SHAs to disk (each Pull load/AddEntry/Save).
	// Reload before cleanup so the wholesale lockfile rewrite in RemoveLocalItems
	// prunes removed entries from the FRESH lockfile — passing the stale pre-pull
	// snapshot would silently revert every update just applied. On reload failure
	// reloadOK gates off the destructive cleanup entirely.
	reloadOK := true
	if reloaded, rerr := lockManager.Load(); rerr == nil {
		lockfile = reloaded
	} else {
		clidiag.Warn("ctxloom", "reload lockfile after apply: %v", rerr)
		reloadOK = false
	}

	reportRemovedFromRemote(os.Stdout, afero.NewOsFs(), projectAppDir(cfg), removedFromRemote, lockfile, lockManager, reloadOK)

	fmt.Printf("\nUpdated: %d, Failed: %d\n", updated, failed)

	// Bundle-reference issues (orphans/missing/invalid) only matter when a
	// profile changed and may have altered its bundle list.
	if len(profileUpdates) > 0 {
		reportBundleIssues(os.Stdout, analyzeBundleReferences(lockfile, projectAppDir(cfg)))
	}

	reportMissingDefaults(os.Stdout, checkDefaultProfiles())

	fmt.Println("\nRun 'ctxloom remote pull' to update the lockfile.")

	return nil
}

// updateInfo is a single available update detected by detectUpdates.
type updateInfo struct {
	Type       remote.ItemType
	Ref        string
	CurrentSHA string
	LatestSHA  string
	// RequestedVersion is the entry's original manifest constraint, carried so
	// apply can pin the content to LatestSHA without overwriting the constraint.
	RequestedVersion string
}

// pullRunner is the slice of *remote.Puller that the apply phase needs, declared
// here (consumer side) so applyUpdates can be unit-tested with a fake.
type pullRunner interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// refreshRemoteRepos fetches each unique remote git repo once so subsequent ref
// resolution reads from a fresh clone. Best-effort: every failure is warned to
// stderr and skipped — a stale repo just risks missing an update, never a crash.
func refreshRemoteRepos(ctx context.Context, cfg *config.Config, lockfile *remote.Lockfile) {
	cache := operations.NewRepoCache(cfg)
	fetched := map[string]struct{}{}
	for _, e := range lockfile.AllEntries() {
		if e.Entry.SHA == "" {
			continue
		}
		ref, err := remote.ParseReference(e.Ref)
		if err != nil || ref.URL == "" {
			continue
		}
		repoURL := ref.URL
		if _, ok := fetched[repoURL]; ok {
			continue
		}
		fetched[repoURL] = struct{}{}
		forgeType, _, ferr := remote.DetectForge(repoURL)
		if ferr != nil {
			continue
		}
		if _, uerr := cache.UpdateRepo(ctx, repoURL, forgeType); uerr != nil {
			clidiag.Warn("ctxloom", "fetch %s: %v", repoURL, uerr)
		}
	}
}

// detectUpdates resolves the latest SHA for every lockfile entry and buckets the
// changed ones into profile vs bundle updates. Entries with an empty SHA are
// counted in skipped and not checked; per-entry resolution failures are skipped
// (the refresh pass already surfaced fetch errors). The only per-entry message
// printed here is for a reference that carries no repository URL.
func detectUpdates(ctx context.Context, out io.Writer, cfg *config.Config, auth remote.AuthConfig, lockfile *remote.Lockfile) (profileUpdates, bundleUpdates []updateInfo, skipped int) {
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
		if ref.URL == "" {
			fmt.Fprintf(out, "  %s: reference has no repository URL\n", e.Ref)
			continue
		}
		fetcher, err := fetcherFor(ref.URL)
		if err != nil {
			continue
		}
		latest, ok := latestWithinConstraint(ctx, fetcher, ref.URL, e.Entry.RequestedVersion)
		if !ok || latest == e.Entry.SHA {
			continue
		}
		info := updateInfo{Type: e.Type, Ref: e.Ref, CurrentSHA: e.Entry.SHA, LatestSHA: latest, RequestedVersion: e.Entry.RequestedVersion}
		if e.Type == remote.ItemTypeProfile {
			profileUpdates = append(profileUpdates, info)
		} else {
			bundleUpdates = append(bundleUpdates, info)
		}
	}
	return profileUpdates, bundleUpdates, skipped
}

// latestWithinConstraint returns the newest commit the entry's version
// constraint allows — the highest tag in a semver range, the tip of a branch, or
// (for a constraint-less entry) the default branch's HEAD. An exact tag/SHA
// constraint resolves to itself, so it is never reported outdated. ok=false on
// any failure. This is what makes `update` constraint-aware: it reports an update
// only when a newer commit actually satisfies what the manifest asked for.
func latestWithinConstraint(ctx context.Context, fetcher remote.Fetcher, url, constraint string) (sha string, ok bool) {
	owner, repo, err := remote.ParseRepoURL(url)
	if err != nil {
		return "", false
	}
	res, rerr := remote.ResolveConstraint(ctx, constraint, remote.NewFetcherRepoVersions(fetcher, owner, repo))
	if rerr != nil || res.SHA == "" {
		return "", false
	}
	return res.SHA, true
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

// applyUpdates pulls profile updates first (a profile may newly reference
// bundles, surfaced by the next lock) then bundle updates, returning counts plus
// the items the remote no longer has (for cleanup). Per-item errors are
// classified and reported, never fatal.
func applyUpdates(ctx context.Context, out io.Writer, p pullRunner, profileUpdates, bundleUpdates []updateInfo) (updated, failed int, removed []updateInfo) {
	pu, pf, pr := applyUpdateBatch(ctx, out, p, "\n--- Updating profiles first ---", profileUpdates)
	bu, bf, br := applyUpdateBatch(ctx, out, p, "\n--- Updating bundles ---", bundleUpdates)
	removed = append(pr, br...)
	return pu + bu, pf + bf, removed
}

// applyUpdateBatch pulls one batch under a header.
func applyUpdateBatch(ctx context.Context, out io.Writer, p pullRunner, header string, updates []updateInfo) (updated, failed int, removed []updateInfo) {
	if len(updates) == 0 {
		return 0, 0, nil
	}
	fmt.Fprintln(out, header)
	for _, u := range updates {
		fmt.Fprintf(out, "\nUpdating %s...\n", u.Ref)
		// Pull the exact constraint-bounded SHA detect computed and displayed as
		// "Latest", not the bare ref: a bare canonical ref re-resolves to
		// default-branch HEAD, which can exceed the manifest constraint. Preserve
		// the original constraint in the lock via RequestedVersion so pinning the
		// content does not freeze "^1.2" into a SHA.
		constraint := u.RequestedVersion
		result, err := p.Pull(ctx, u.Ref+"@"+u.LatestSHA, remote.PullOptions{
			ItemType:         u.Type,
			Force:            updateForce,
			Blind:            updateBlind,
			RequestedVersion: &constraint,
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
//
// cleanupAllowed must be false when the post-apply lockfile reload failed: the
// in-memory lockfile is then a stale pre-pull snapshot, and RemoveLocalItems'
// wholesale Save would overwrite the freshly-pulled on-disk SHAs — reverting
// every update just applied. In that case the destructive cleanup is skipped.
func reportRemovedFromRemote(out io.Writer, fs afero.Fs, appDir string, removed []updateInfo, lockfile *remote.Lockfile, lockManager *remote.LockfileManager, cleanupAllowed bool) {
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

	if !cleanupAllowed {
		// Reload failed: saving the stale lockfile would revert the SHAs just
		// written to disk. Skip cleanup and let the user reconcile first.
		fmt.Fprintln(out, "\nSkipping --cleanup: the lockfile could not be reloaded after applying updates.")
		fmt.Fprintln(out, "Run 'ctxloom remote pull' to refresh the lockfile, then re-run with --cleanup.")
		return
	}

	fmt.Fprintln(out, "\nCleaning up local files...")
	items := make([]operations.RemovedItem, len(removed))
	for i, item := range removed {
		items[i] = operations.RemovedItem{Type: item.Type, Ref: item.Ref}
	}
	res, _ := operations.RemoveLocalItems(operations.RemoveLocalItemsRequest{
		AppDir:      appDir,
		Items:       items,
		Lockfile:    lockfile,
		LockManager: lockManager,
		FS:          fs,
	})
	for _, p := range res.Removed {
		fmt.Fprintf(out, "  Removed: %s\n", p)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "  Warning: %s\n", w)
	}
	if res.Saved {
		fmt.Fprintf(out, "  Updated lockfile (removed %d entries)\n", len(res.Pruned))
	}
}

// reportBundleIssues prints orphan/missing/invalid bundle findings plus any
// non-fatal warnings. Silent when there is nothing to report.
func reportBundleIssues(out io.Writer, analysis *operations.BundleAnalysis) {
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
		fmt.Fprintln(out, "\nPull missing bundles with: ctxloom remote pull")
	}
	if len(analysis.Orphans) > 0 {
		fmt.Fprintf(out, "\n--- Orphaned bundles ---\n")
		fmt.Fprintln(out, "The following bundles are no longer referenced by any profile:")
		for _, orphan := range analysis.Orphans {
			fmt.Fprintf(out, "  - %s\n", orphan)
		}
		fmt.Fprintln(out, "\nTo remove orphaned bundles, delete them manually from .ctxloom/bundles/")
		fmt.Fprintln(out, "Then run 'ctxloom remote pull' to update the lockfile.")
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

// analyzeBundleReferences cross-checks the lockfile against the bundle
// references declared by local profiles, via the operations core.
func analyzeBundleReferences(lockfile *remote.Lockfile, appDir string) *operations.BundleAnalysis {
	return operations.AnalyzeBundleReferences(operations.AnalyzeBundleReferencesRequest{Lockfile: lockfile, AppDir: appDir})
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
		if _, exists := cfg.Profiles.Definitions[name]; exists {
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
