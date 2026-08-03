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
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

var updateApply bool
var updateForce bool
var updateCleanup bool

var remoteUpdateCmd = &cobra.Command{
	Use:   "update [reference]",
	Short: "Check for and apply updates to remote items",
	Long: `Check for updates to installed remote items.

Without arguments, checks all items in the lockfile for updates. With a
reference, checks only that item. The reference is a canonical bundle
reference — a full repository URL plus its bundle path, e.g.
https://github.com/alice/ctxloom@bundles/security — not a remote name (see
"ctxloom remote add --help" for the repository URL formats a remote itself
may take).

By default this only reports what is out of date. Pass --apply to actually
update the lockfile and installed files.

Examples:
  ctxloom remote update                                                             # Check all for updates
  ctxloom remote update https://github.com/alice/ctxloom@bundles/security           # Check one item
  ctxloom remote update --apply                                                     # Apply all available updates
  ctxloom remote update https://github.com/alice/ctxloom@bundles/security --apply   # Update one item
  ctxloom remote update --apply --force                                             # Apply all updates without prompts
  ctxloom remote update --apply --cleanup                                           # Also remove items deleted from remote`,
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
	ref, err := parseUpdateRef(refStr)
	if err != nil {
		return err
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

	u, upToDate, err := detectSingleUpdate(cmd.Context(), os.Stdout, fetcher, lockfile, ref, refStr)
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
	puller := newUpdatePuller(cfg, registry, auth, lockManager)
	_, failed, removed := applyUpdateBatch(cmd.Context(), os.Stdout, puller, "\n--- Updating ---", updateForce, []updateInfo{u})
	// Reload after the apply so cleanup prunes from the freshly-pulled lockfile
	// rather than reverting it (see updateAll).
	reloadOK := true
	if reloaded, rerr := lockManager.Load(); rerr == nil {
		lockfile = reloaded
	} else {
		clidiag.Warn("ctxloom", "reload lockfile after apply: %v", rerr)
		reloadOK = false
	}
	reportRemovedFromRemote(os.Stdout, afero.NewOsFs(), projectAppDir(cfg), removed, lockfile, lockManager, updateCleanup, reloadOK)
	if failed > 0 {
		return fmt.Errorf("update failed for %s", refStr)
	}
	return nil
}

// newUpdatePuller builds the Puller `remote update --apply` pulls through. It is
// ONE constructor for both apply paths (single-ref and batch): they were two
// identical literals, and a literal that has to be kept in sync with another is
// the shape a divergence hides in.
//
// lockManager is a parameter rather than a default because the default is WRONG
// here: remote.NewPuller falls back to NewLockfileManager(".ctxloom"), a
// RELATIVE path resolved against the process cwd, while this command resolves
// the lockfile it DETECTS against through projectAppDir(cfg) — the project root
// config.Load walked up to. Run from a subdirectory those are two different
// files, so apply wrote its pin into a brand-new <cwd>/.ctxloom while the
// project lockfile kept the old SHA, and the post-apply reload read that stale
// file back. Exit 0, "Updated to <sha>", nothing moved.
//
// The directory-form tree install inherits the same base dir
// (Puller.installTree → lockfileManager.BaseDir()), so the CONTENT landed in
// the wrong tree too — which is exactly what BaseDir's own doc warns about:
// "two configuration axes for one directory is how a pin and the content it
// pins end up in different trees."
//
// extra is applied LAST, so a test can substitute the fetcher factory (which
// otherwise reaches the network) without standing up a second construction that
// could drift from this one — the drift that produced the bug in the first
// place.
func newUpdatePuller(cfg *config.Config, registry *remote.Registry, auth remote.AuthConfig, lockManager *remote.LockfileManager, extra ...remote.PullerOption) *remote.Puller {
	opts := []remote.PullerOption{
		remote.WithFetcherFactory(operations.NewCachedFetcherFactory(cfg)),
		// The lockfile detect reads, apply writes, reload re-reads and cleanup
		// prunes is ONE file. Mirrors operations.resolveSyncDeps, which has
		// always passed its own manager for exactly this reason.
		remote.WithLockfileManager(lockManager),
		// Same seam `remote pull` is wired through (operations.resolveSyncDeps):
		// an update that could not re-fetch a directory-form bundle would leave
		// its pin advanced and its content behind.
		remote.WithTreeFetcher(remotetree.PullTreeFetcher),
	}
	return remote.NewPuller(registry, auth, append(opts, extra...)...)
}

// parseUpdateRef parses a single-ref `remote update` argument and rejects one
// that carries no repository URL. It is the ONE rejection point on this path:
// the same input reaching two guards with two different opinions of it tells
// the user two different things about the same string, and "invalid reference"
// for a reference that parsed perfectly well sends the reader hunting a syntax
// error that isn't there.
func parseUpdateRef(refStr string) (*remote.Reference, error) {
	ref, err := remote.ParseReference(refStr)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}
	// Canonical refs carry the repo URL directly.
	if ref.URL == "" {
		return nil, fmt.Errorf("reference has no repository URL: %s", refStr)
	}
	return ref, nil
}

// detectSingleUpdate resolves one ref's update status against the lockfile,
// constraint-aware: the latest SHA is the newest commit the entry's
// RequestedVersion allows (latestWithinConstraint), never bare default-branch
// HEAD, which can exceed the manifest constraint. The lock entry is found by
// the ref's canonical identity, so a version-suffixed input still matches.
// ref is already validated by parseUpdateRef; refStr is carried only for the
// status lines, which quote the reference the user actually typed. Prints the
// status to out; returns the pending update when one exists.
func detectSingleUpdate(ctx context.Context, out io.Writer, fetcher remote.Fetcher, lockfile *remote.Lockfile, ref *remote.Reference, refStr string) (updateInfo, bool, error) {
	canonical := ref.CanonicalString()
	entry, itemType := lookupLockedEntry(lockfile, canonical)
	if itemType == "" {
		// An unlocked ref is a bundle — top-level profile distribution was retired,
		// so bundles are the only distributed item type.
		itemType = remote.ItemTypeBundle
	}

	latestSHA, ok, lerr := latestWithinConstraint(ctx, fetcher, ref.URL, entry.RequestedVersion)
	if lerr != nil {
		return updateInfo{}, false, fmt.Errorf("failed to resolve latest version for %s: %w", refStr, lerr)
	}
	if !ok {
		return updateInfo{}, false, fmt.Errorf("failed to resolve latest version for %s", refStr)
	}

	upToDate := reportUpdateStatus(out, refStr, entry.SHA, latestSHA)
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
	if cfg != nil && len(cfg.GetAppPaths()) > 0 && cfg.GetAppPaths()[0] != "" {
		return cfg.GetAppPaths()[0]
	}
	return ".ctxloom"
}

// refreshRemoteClone fetches the latest into one repo's local clone so updates
// can be detected — the single-ref counterpart of refreshRemoteRepos.
func refreshRemoteClone(ctx context.Context, cfg *config.Config, repoURL string) {
	fetchIntoClone(ctx, operations.NewRepoCache(cfg), repoURL)
}

// fetchIntoClone refreshes one repository's local clone. Fault-tolerant, and
// deliberately so on both arms: a fetch failure warns and leaves the stale
// clone in place (a stale clone risks missing an update, never a crash), and a
// URL whose forge cannot be detected has nothing to fetch from at all.
func fetchIntoClone(ctx context.Context, cache *remote.RepoCache, repoURL string) {
	forgeType, _, ferr := remote.DetectForge(repoURL)
	if ferr != nil {
		return
	}
	if _, uerr := cache.UpdateRepo(ctx, repoURL, forgeType); uerr != nil {
		clidiag.Warn("ctxloom", "fetch %s: %v", repoURL, uerr)
	}
}

// lookupLockedEntry finds refStr's bundle lock entry and item type. Returns a
// zero entry and empty type when not present.
func lookupLockedEntry(lockfile *remote.Lockfile, refStr string) (remote.LockEntry, remote.ItemType) {
	if entry, ok := lockfile.GetEntry(remote.ItemTypeBundle, refStr); ok {
		return entry, remote.ItemTypeBundle
	}
	return remote.LockEntry{}, ""
}

// reportUpdateStatus prints the update status and reports whether the ref is
// already up to date.
func reportUpdateStatus(out io.Writer, refStr, currentSHA, latestSHA string) bool {
	switch currentSHA {
	case "":
		fmt.Fprintf(out, "%s not found in lockfile, checking latest version...\n", refStr)
		return false
	case latestSHA:
		fmt.Fprintf(out, "%s is up to date (SHA: %s)\n", refStr, gitutil.ShortSHA(latestSHA))
		return true
	default:
		fmt.Fprintf(out, "%s has update available:\n", refStr)
		fmt.Fprintf(out, "  Current: %s\n", gitutil.ShortSHA(currentSHA))
		fmt.Fprintf(out, "  Latest:  %s\n", gitutil.ShortSHA(latestSHA))
		return false
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
	bundleUpdates, skippedEmpty, failedChecks := detectUpdates(cmd.Context(), os.Stdout, cfg, auth, lockfile)

	if skippedEmpty > 0 {
		fmt.Printf("Skipped %d entries with empty SHA (run 'ctxloom remote pull' to clean up)\n\n", skippedEmpty)
	}

	if len(bundleUpdates) == 0 {
		// "up to date" is a claim about entries that were actually
		// CHECKED. When some entries' checks failed (see the warnings above),
		// saying so unconditionally reads as "everything was verified current"
		// when in fact part of the closure was never resolved at all.
		if failedChecks > 0 {
			fmt.Printf("No updates found among the entries that could be checked — %d entry(ies) could not be checked (see warnings above).\n", failedChecks)
		} else {
			fmt.Println("All items are up to date!")
		}
		return nil
	}

	fmt.Printf("Found %d items with updates available:\n\n", len(bundleUpdates))

	printAvailableUpdates(os.Stdout, bundleUpdates)

	if !updateApply {
		fmt.Println("\nRun with --apply to update all items.")
		return nil
	}

	fmt.Println("\nApplying updates...")
	puller := newUpdatePuller(cfg, registry, auth, lockManager)
	updated, failed, removedFromRemote := applyUpdates(cmd.Context(), os.Stdout, puller, updateForce, bundleUpdates)

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

	reportRemovedFromRemote(os.Stdout, afero.NewOsFs(), projectAppDir(cfg), removedFromRemote, lockfile, lockManager, updateCleanup, reloadOK)

	fmt.Printf("\nUpdated: %d, Failed: %d\n", updated, failed)

	missingDefaults, defaultsErr := checkDefaultProfiles(config.Load)
	reportMissingDefaults(os.Stdout, missingDefaults, defaultsErr)

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
	// Kind and Version describe the selector for display ("tracking branch main",
	// "range ^1.2 → v1.3.0"). Version is the concrete tag currently locked, if any.
	Kind    remote.SelectorKind
	Version string
}

// selectorLabel renders a human description of an entry's selector for the update
// listing: what it tracks and, for a version range, the concrete tag it sits on.
func (u updateInfo) selectorLabel() string {
	switch u.Kind {
	case remote.SelectorBranch:
		name := u.RequestedVersion
		if name == "" {
			name = "default branch"
		}
		return "tracking branch " + name
	case remote.SelectorVersion:
		if u.Version != "" {
			return fmt.Sprintf("range %s → %s", u.RequestedVersion, u.Version)
		}
		return "range " + u.RequestedVersion
	default:
		return string(u.Kind)
	}
}

// pullRunner is the slice of *remote.Puller that the apply phase needs, declared
// here (consumer side) so applyUpdates can be unit-tested with a fake.
type pullRunner interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// refreshRemoteRepos fetches each unique remote git repo once so subsequent ref
// resolution reads from a fresh clone (one git fetch per repo, not one per
// entry). It adds only the batch concerns on top of fetchIntoClone: an entry
// with no locked SHA was never pulled and so has nothing to compare against,
// and an unparseable or URL-less reference is skipped.
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
		if _, ok := fetched[ref.URL]; ok {
			continue
		}
		fetched[ref.URL] = struct{}{}
		fetchIntoClone(ctx, cache, ref.URL)
	}
}

// detectUpdates resolves the latest SHA for every lockfile entry and returns the
// changed ones. Entries with an empty SHA are counted in skipped and not checked;
// per-entry resolution failures are skipped (the refresh pass already surfaced
// fetch errors). The only per-entry message printed here is for a reference that
// carries no repository URL.
func detectUpdates(ctx context.Context, out io.Writer, cfg *config.Config, auth remote.AuthConfig, lockfile *remote.Lockfile) (bundleUpdates []updateInfo, skipped, failed int) {
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
		// A sha/tag pin never goes outdated — re-resolving yields the same commit,
		// so skip the network round-trip entirely (declarative from the kind).
		if e.Entry.SelectorKind().IsPin() {
			continue
		}
		ref, err := remote.ParseReference(e.Ref)
		if err != nil {
			// This used to `continue` with ZERO diagnostic — a
			// lockfile entry with a reference this build can no longer parse
			// silently dropped out of the update check entirely, and if every
			// entry hit this, "All items are up to date!" printed with nothing
			// having actually been checked.
			clidiag.Warn("ctxloom", "%s: could not parse reference (%v); skipping the update check for it", e.Ref, err)
			failed++
			continue
		}
		if ref.URL == "" {
			fmt.Fprintf(out, "  %s: reference has no repository URL\n", e.Ref)
			failed++
			continue
		}
		fetcher, err := fetcherFor(ref.URL)
		if err != nil {
			// Same silent shape — a fetcher construction failure
			// (auth, network, a malformed URL the factory rejects) looked
			// identical to "this entry is already at the latest commit."
			clidiag.Warn("ctxloom", "%s: could not reach %s (%v); skipping the update check for it", e.Ref, ref.URL, err)
			failed++
			continue
		}
		latest, ok, lerr := latestWithinConstraint(ctx, fetcher, ref.URL, e.Entry.RequestedVersion)
		if lerr != nil {
			clidiag.Warn("ctxloom", "%s: could not resolve %q (%v); skipping the update check for it", e.Ref, e.Entry.RequestedVersion, lerr)
			failed++
			continue
		}
		if !ok || latest == e.Entry.SHA {
			continue
		}
		bundleUpdates = append(bundleUpdates, updateInfo{Type: e.Type, Ref: e.Ref, CurrentSHA: e.Entry.SHA, LatestSHA: latest, RequestedVersion: e.Entry.RequestedVersion, Kind: e.Entry.SelectorKind(), Version: e.Entry.Version})
	}
	return bundleUpdates, skipped, failed
}

// latestWithinConstraint returns the newest commit the entry's version
// constraint allows — the highest tag in a semver range, the tip of a branch, or
// (for a constraint-less entry) the default branch's HEAD. An exact tag/SHA
// constraint resolves to itself, so it is never reported outdated. ok=false on
// any failure. This is what makes `update` constraint-aware: it reports an update
// only when a newer commit actually satisfies what the manifest asked for.
// latestWithinConstraint resolves constraint against url's available
// versions. err is non-nil only for a genuine resolution FAILURE (a
// malformed URL, a network/auth error reaching the forge); ok=false with a
// nil err means resolution ran cleanly and found nothing satisfying
// constraint — callers must be able to tell these two cases apart instead of
// both collapsing into "no update, all good."
func latestWithinConstraint(ctx context.Context, fetcher remote.Fetcher, url, constraint string) (sha string, ok bool, err error) {
	owner, repo, err := remote.ParseOwnerRepo(url)
	if err != nil {
		return "", false, err
	}
	res, rerr := remote.ResolveConstraint(ctx, constraint, remote.NewFetcherRepoVersions(fetcher, owner, repo))
	if rerr != nil {
		return "", false, rerr
	}
	if res.SHA == "" {
		return "", false, nil
	}
	return res.SHA, true, nil
}

// printAvailableUpdates lists pending bundle updates; the section is omitted when
// empty.
func printAvailableUpdates(out io.Writer, bundleUpdates []updateInfo) {
	if len(bundleUpdates) > 0 {
		fmt.Fprintln(out, "Bundles:")
		for _, u := range bundleUpdates {
			fmt.Fprintf(out, "  %s  (%s)\n", u.Ref, u.selectorLabel())
			fmt.Fprintf(out, "    Current: %s → Latest: %s\n", gitutil.ShortSHA(u.CurrentSHA), gitutil.ShortSHA(u.LatestSHA))
		}
	}
}

// applyUpdates pulls the bundle updates, returning counts plus the items the
// remote no longer has (for cleanup). Per-item errors are classified and
// reported, never fatal.
func applyUpdates(ctx context.Context, out io.Writer, p pullRunner, force bool, bundleUpdates []updateInfo) (updated, failed int, removed []updateInfo) {
	return applyUpdateBatch(ctx, out, p, "\n--- Updating bundles ---", force, bundleUpdates)
}

// applyUpdateBatch pulls one batch under a header. force is the caller's
// --force decision, passed in rather than read from the flag global: this
// function already takes its writer and its pull runner as parameters, and the
// one input that changes what it DOES belongs on the same footing.
func applyUpdateBatch(ctx context.Context, out io.Writer, p pullRunner, header string, force bool, updates []updateInfo) (updated, failed int, removed []updateInfo) {
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
			Force:            force,
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
		fmt.Fprintf(out, "  Updated to %s\n", gitutil.ShortSHA(result.SHA))
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
func reportRemovedFromRemote(out io.Writer, fs afero.Fs, appDir string, removed []updateInfo, lockfile *remote.Lockfile, lockManager *remote.LockfileManager, cleanup, cleanupAllowed bool) {
	if len(removed) == 0 {
		return
	}
	fmt.Fprintf(out, "\n--- Items removed from remote ---\n")
	fmt.Fprintln(out, "The following items no longer exist on the remote:")
	for _, item := range removed {
		fmt.Fprintf(out, "  - %s %s\n", item.Type, item.Ref)
	}

	if !cleanup {
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
	// Per-item and per-save failures come back in res.Warnings and are printed
	// below; the error return is reserved for a whole-call refusal, on which
	// there is no result to report at all.
	res, err := operations.RemoveLocalItems(operations.RemoveLocalItemsRequest{
		AppDir:      appDir,
		Items:       items,
		Lockfile:    lockfile,
		LockManager: lockManager,
		FS:          fs,
	})
	if err != nil {
		fmt.Fprintf(out, "  Error: cleanup failed: %v\n", err)
		return
	}
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

// reportMissingDefaults warns about configured default profiles that don't
// exist. Silent when there are none — but never silent when err says the check
// could not be made: an unperformed check has no clean result to report, and
// printing nothing is indistinguishable from "they all exist".
func reportMissingDefaults(out io.Writer, missing []string, err error) {
	if err != nil {
		fmt.Fprintf(out, "\nWarning: could not check the default profiles: %v\n", err)
		return
	}
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

// pullOutcome describes how a per-item Pull error should be reported.
// Extracted so the (user-visible) classification rules can be unit-tested
// without spinning up a Puller. classifyPullError matches on the typed
// sentinels in internal/errs with errors.Is, so the several layers of
// wrapping between the forge client and the puller are transparent to it.
// If a new outcome is needed, this is the one place to add it.
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

// checkDefaultProfiles returns names of the default agent's composed profiles
// that don't exist (profiles.defaults was retired — see DefaultAgentProfiles).
// A config that will not load yields an error, never an empty slice: "nothing
// is missing" and "nothing was checked" are different answers and the caller
// renders them differently. loadConfig is seam'd for tests; production passes
// config.Load.
func checkDefaultProfiles(loadConfig func(...config.LoadOption) (*config.Config, error)) ([]string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}

	defaultProfiles := cfg.DefaultAgentProfiles()
	if len(defaultProfiles) == 0 {
		return nil, nil
	}

	var missing []string
	profileLoader := cfg.GetProfileLoader()
	profileDefs := cfg.GetProfileDefinitions()

	for _, name := range defaultProfiles {
		// Check if profile exists in config
		if _, exists := profileDefs[name]; exists {
			continue
		}

		// Check if profile exists as a file
		_, err := profileLoader.Load(name)
		if err != nil {
			missing = append(missing, name)
		}
	}

	return missing, nil
}

func init() {
	remoteCmd.AddCommand(remoteUpdateCmd)

	remoteUpdateCmd.Flags().BoolVar(&updateApply, "apply", false,
		"Apply available updates")
	remoteUpdateCmd.Flags().BoolVar(&updateForce, "force", false,
		"Skip confirmation prompts when applying updates")
	remoteUpdateCmd.Flags().BoolVar(&updateCleanup, "cleanup", false,
		"Remove local files for items deleted from remote")
}
