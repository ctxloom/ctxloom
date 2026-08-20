package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

var depsCheckCmd = &cobra.Command{
	Use:   "check [reference]",
	Short: "Report which dependencies have a newer commit available",
	Long: `Check the installed closure against its remotes and report what is out of date.

Without arguments, checks every lockfile entry. With a reference, checks only
that one. The reference is a canonical bundle reference — a full repository URL
plus its bundle path, e.g. https://github.com/alice/ctxloom@bundles/security —
not a remote name (see "ctxloom remote create --help" for the repository URL
formats a remote itself may take).

CHECK READS; UPGRADE WRITES. This reports and changes nothing, so it is safe to
run anywhere and on anything. 'ctxloom deps upgrade' is the verb that advances
the pins it names.

The check is constraint-aware: an entry is out of date only when a newer commit
actually satisfies what its manifest asked for. An entry pinned to an exact tag
or SHA is never out of date, and is not fetched for.

An entry that could NOT be checked — an unreachable remote, an unparseable
reference — is reported as unchecked rather than folded into "up to date".

Examples:
  ctxloom deps check
  ctxloom deps check https://github.com/alice/ctxloom@bundles/security`,
	RunE: runDepsCheck,
}

func runDepsCheck(cmd *cobra.Command, args []string) error {
	auth := remote.LoadAuth("")
	cfg := loadConfigOrFallback(GetConfig, os.Stderr)
	lockManager := remote.NewLockfileManager(projectAppDir(cfg))

	if len(args) > 0 {
		return checkSingle(cmd, cfg, args[0], lockManager)
	}
	return checkAll(cmd, cfg, auth, lockManager)
}

func checkSingle(cmd *cobra.Command, cfg *config.Config, refStr string, lockManager *remote.LockfileManager) error {
	ref, err := parseCheckRef(refStr)
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

	_, upToDate, err := detectSingleUpdate(cmd.Context(), os.Stdout, fetcher, lockfile, ref, refStr)
	if err != nil {
		return err
	}
	if !upToDate {
		fmt.Println("\nRun 'ctxloom deps upgrade' to advance it.")
	}
	return nil
}

// parseCheckRef parses a single-ref `deps check` argument and rejects one that
// carries no repository URL. It is the ONE rejection point on this path:
// the same input reaching two guards with two different opinions of it tells
// the user two different things about the same string, and "invalid reference"
// for a reference that parsed perfectly well sends the reader hunting a syntax
// error that isn't there.
func parseCheckRef(refStr string) (*remote.Reference, error) {
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
// ref is already validated by parseCheckRef; refStr is carried only for the
// status lines, which quote the reference the user actually typed. Prints the
// status to out; returns the pending update when one exists.
func detectSingleUpdate(ctx context.Context, out io.Writer, fetcher remote.Fetcher, lockfile *remote.Lockfile, ref *remote.Reference, refStr string) (updateInfo, bool, error) {
	canonical := ref.LockKey()
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
// project root — so `deps check` works from subdirectories. The bare
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

func checkAll(cmd *cobra.Command, cfg *config.Config, auth remote.AuthConfig, lockManager *remote.LockfileManager) error {
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("Nothing is installed, so there is nothing to check.")
		fmt.Println("Install this project's closure with: ctxloom deps pull")
		return nil
	}

	fmt.Printf("Checking %d items for updates...\n\n", len(lockfile.AllEntries()))

	// Refresh every unique remote once (one git fetch per repo, not two per
	// entry), then resolve the latest SHA for each entry.
	refreshRemoteRepos(cmd.Context(), cfg, lockfile)
	bundleUpdates, skippedEmpty, failedChecks := detectUpdates(cmd.Context(), os.Stdout, cfg, auth, lockfile)

	if skippedEmpty > 0 {
		fmt.Printf("Skipped %d entries with empty SHA (run 'ctxloom deps pull' to clean up)\n\n", skippedEmpty)
	}

	if len(bundleUpdates) == 0 {
		// "up to date" is a claim about entries that were actually CHECKED.
		// When some entries' checks failed (see the warnings above), saying so
		// unconditionally reads as "everything was verified current" when in
		// fact part of the closure was never resolved at all.
		if failedChecks > 0 {
			fmt.Printf("No updates found among the entries that could be checked — %d entry(ies) could not be checked (see warnings above).\n", failedChecks)
		} else {
			fmt.Println("All items are up to date!")
		}
		return nil
	}

	fmt.Printf("Found %d items with updates available:\n\n", len(bundleUpdates))

	printAvailableUpdates(os.Stdout, bundleUpdates)

	missingDefaults, defaultsErr := checkDefaultProfiles(config.Load)
	reportMissingDefaults(os.Stdout, missingDefaults, defaultsErr)

	fmt.Println("\nRun 'ctxloom deps upgrade' to advance these pins.")
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

	for _, name := range defaultProfiles {
		if _, err := profileLoader.Load(name); err != nil {
			missing = append(missing, name)
		}
	}

	return missing, nil
}

func init() {
	depsCmd.AddCommand(depsCheckCmd)
}
