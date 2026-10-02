package operations

import (
	"context"
	"errors"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// `ctxloom deps check` — the installed closure against its remotes. CHECK
// READS; UPGRADE WRITES: this reports and changes nothing. The check is
// constraint-aware: an entry is out of date only when a newer commit
// actually satisfies what its manifest asked for; an entry pinned to an exact
// tag or SHA is never out of date and is not fetched for. An entry that
// could NOT be checked is a typed row, never folded into "up to date".

// CheckDependenciesRequest scopes one check: the whole lockfile, or the one
// canonical reference named (a full repository URL plus its bundle path).
type CheckDependenciesRequest struct {
	Ref string
}

// DependencyUpdate is one entry a newer commit satisfies the manifest
// constraint for.
type DependencyUpdate struct {
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

// SelectorLabel renders a human description of an entry's selector for the
// update listing: what it tracks and, for a version range, the concrete tag
// it sits on.
func (u DependencyUpdate) SelectorLabel() string {
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

// UncheckedReason says why an entry's currency could not be established.
// Each used to be a `continue` with no diagnostic — a lockfile entry this
// build could not parse or reach silently dropped out of the check, and if
// every entry hit this, "All items are up to date!" printed with nothing
// having actually been checked.
type UncheckedReason int

const (
	// UncheckedUnparseable: the reference does not parse (Err is the parser's).
	UncheckedUnparseable UncheckedReason = iota
	// UncheckedNoRepositoryURL: the reference parsed but names no repository.
	UncheckedNoRepositoryURL
	// UncheckedUnreachable: no fetcher could be built for URL (auth, network,
	// a malformed URL the factory rejects); Err is the factory's.
	UncheckedUnreachable
	// UncheckedUnresolvable: the fetcher answered but Constraint could not be
	// resolved against the repository's versions; Err is the resolver's.
	UncheckedUnresolvable
	// UncheckedNotRefreshed: this run could not fetch URL, so the only clone to
	// read is the one a previous fetch left, and an answer from it is about
	// the past; Err is the fetch's.
	UncheckedNotRefreshed
)

// ErrCloneNotRefreshed refuses a single-reference check whose repository could
// not be fetched this run: there is no current state to report it against.
var ErrCloneNotRefreshed = errors.New("the repository could not be fetched, so whether this reference is current is unknown")

// UncheckedDependency is one entry that was neither verified current nor
// found outdated, in the order the lockfile lists it.
type UncheckedDependency struct {
	Ref        string
	URL        string
	Constraint string
	Reason     UncheckedReason
	Err        error
}

// RefreshFailure is one repository whose clone could not be refreshed before
// the check. Its entries are reported unchecked (UncheckedNotRefreshed), never
// answered from the stale clone: absence of an update is authority only from a
// remote this run proved it could read.
type RefreshFailure struct {
	URL string
	Err error
}

// DependencyStatus is a single-reference check's answer. Ref is the
// reference as the caller typed it; CurrentSHA is "" when the lockfile does
// not hold it.
type DependencyStatus struct {
	Ref        string
	CurrentSHA string
	LatestSHA  string
	Update     DependencyUpdate
}

// UpToDate reports whether the locked commit is the newest the constraint
// allows. An unlocked reference is never up to date: there is nothing to be
// current against.
func (s DependencyStatus) UpToDate() bool {
	return s.CurrentSHA != "" && s.CurrentSHA == s.LatestSHA
}

// CheckDependenciesResult is what the check found. Entries is how many the
// lockfile holds (0: nothing is installed, so there was nothing to check).
// Single is set when the request named one reference; the closure fields
// are then zero.
type CheckDependenciesResult struct {
	Entries      int
	Refresh      []RefreshFailure
	Updates      []DependencyUpdate
	Unchecked    []UncheckedDependency
	SkippedEmpty int
	// MissingDefaults names the default agent's composed profiles that do
	// not exist — asked only when an update was found, since that is when a
	// user is about to act on their defaults. MissingDefaultsErr says the
	// question could not be asked at all (the config did not load): "nothing
	// is missing" and "nothing was checked" are different answers.
	MissingDefaults    []string
	MissingDefaultsErr error
	Single             *DependencyStatus
}

// CheckDependencies reports which installed dependencies have a newer commit
// available within their constraint, or one reference's status. It changes
// nothing. A configuration that fails to load is not fatal — the check
// proceeds over a minimal default rooted at .ctxloom, exactly as the other
// fault-tolerant startup paths do — and is reported through
// MissingDefaultsErr.
func CheckDependencies(ctx context.Context, app *App, req CheckDependenciesRequest) (CheckDependenciesResult, error) {
	cfg, cfgErr := app.Config(ctx)
	if cfgErr != nil {
		cfg = config.NewFixture(config.Fixture{AppPaths: []string{".ctxloom"}})
	}
	lockManager := remote.NewLockfileManager(ProjectAppDir(cfg))
	if req.Ref != "" {
		return checkSingleDependency(ctx, cfg, req.Ref, lockManager)
	}
	return checkAllDependencies(ctx, cfg, cfgErr, remote.LoadAuth(""), lockManager)
}

func checkSingleDependency(ctx context.Context, cfg *config.Config, refStr string, lockManager *remote.LockfileManager) (CheckDependenciesResult, error) {
	var res CheckDependenciesResult
	ref, err := parseCheckRef(refStr)
	if err != nil {
		return res, err
	}

	res.Refresh = refreshRemoteClone(ctx, cfg, ref.URL)
	if len(res.Refresh) > 0 {
		return res, fmt.Errorf("%w: %s: %w", ErrCloneNotRefreshed, ref.URL, res.Refresh[0].Err)
	}

	fetcher, err := GetCachedFetcher(cfg, ref.URL)
	if err != nil {
		return res, fmt.Errorf("failed to create fetcher: %w", err)
	}

	lockfile, err := lockManager.Load()
	if err != nil {
		return res, err
	}

	status, err := detectSingleUpdate(ctx, fetcher, lockfile, ref, refStr)
	if err != nil {
		return res, err
	}
	res.Single = &status
	return res, nil
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

// detectSingleUpdate resolves one ref's status against the lockfile,
// constraint-aware: the latest SHA is the newest commit the entry's
// RequestedVersion allows (latestWithinConstraint), never bare default-branch
// HEAD, which can exceed the manifest constraint. The lock entry is found by
// the ref's canonical identity, so a version-suffixed input still matches.
// ref is already validated by parseCheckRef; refStr is carried only for the
// status, which quotes the reference the user actually typed.
func detectSingleUpdate(ctx context.Context, fetcher remote.Fetcher, lockfile *remote.Lockfile, ref *remote.Reference, refStr string) (DependencyStatus, error) {
	canonical, err := ref.LockKey()
	if err != nil {
		return DependencyStatus{}, fmt.Errorf("invalid reference %s: %w", refStr, err)
	}
	entry, itemType := lookupLockedEntry(lockfile, canonical)
	if itemType == "" {
		// An unlocked ref is a bundle — top-level profile distribution was retired,
		// so bundles are the only distributed item type.
		itemType = remote.ItemTypeBundle
	}

	latestSHA, ok, lerr := latestWithinConstraint(ctx, fetcher, ref.URL, entry.RequestedVersion)
	if lerr != nil {
		return DependencyStatus{}, fmt.Errorf("failed to resolve latest version for %s: %w", refStr, lerr)
	}
	if !ok {
		return DependencyStatus{}, fmt.Errorf("failed to resolve latest version for %s", refStr)
	}

	return DependencyStatus{
		Ref:        refStr,
		CurrentSHA: entry.SHA,
		LatestSHA:  latestSHA,
		Update: DependencyUpdate{
			Type:             itemType,
			Ref:              string(canonical),
			CurrentSHA:       entry.SHA,
			LatestSHA:        latestSHA,
			RequestedVersion: entry.RequestedVersion,
		},
	}, nil
}

// refreshRemoteClone fetches the latest into one repo's local clone so updates
// can be detected — the single-ref counterpart of refreshRemoteRepos.
func refreshRemoteClone(ctx context.Context, cfg *config.Config, repoURL string) []RefreshFailure {
	var failures []RefreshFailure
	fetchIntoClone(ctx, NewRepoCache(cfg), repoURL, &failures)
	return failures
}

// fetchIntoClone refreshes one repository's local clone. A fetch failure is
// recorded for the caller, which must not then answer from the stale clone; a
// URL whose forge cannot be detected has nothing to fetch from at all.
func fetchIntoClone(ctx context.Context, cache *remote.RepoCache, repoURL string, failures *[]RefreshFailure) {
	forgeType, _, ferr := remote.DetectForge(repoURL)
	if ferr != nil {
		return
	}
	if _, uerr := cache.UpdateRepo(ctx, repoURL, forgeType); uerr != nil {
		*failures = append(*failures, RefreshFailure{URL: repoURL, Err: uerr})
	}
}

// lookupLockedEntry finds refStr's bundle lock entry and item type. Returns a
// zero entry and empty type when not present.
func lookupLockedEntry(lockfile *remote.Lockfile, key trust.BundleKey) (remote.LockEntry, remote.ItemType) {
	if entry, ok := lockfile.GetEntry(remote.ItemTypeBundle, key); ok {
		return entry, remote.ItemTypeBundle
	}
	return remote.LockEntry{}, ""
}

func checkAllDependencies(ctx context.Context, cfg *config.Config, cfgErr error, auth remote.AuthConfig, lockManager *remote.LockfileManager) (CheckDependenciesResult, error) {
	var res CheckDependenciesResult
	lockfile, err := lockManager.Load()
	if err != nil {
		return res, err
	}
	if lockfile.IsEmpty() {
		return res, nil
	}
	res.Entries = len(lockfile.AllEntries())

	// Refresh every unique remote once (one git fetch per repo, not two per
	// entry), then resolve the latest SHA for each entry.
	res.Refresh = refreshRemoteRepos(ctx, cfg, lockfile)
	res.Updates, res.Unchecked, res.SkippedEmpty = detectUpdates(ctx, cfg, auth, lockfile, res.Refresh)

	if len(res.Updates) > 0 {
		if cfgErr != nil {
			res.MissingDefaultsErr = cfgErr
		} else {
			res.MissingDefaults = missingDefaultProfiles(cfg)
		}
	}
	return res, nil
}

// refreshRemoteRepos fetches each unique remote git repo once so subsequent ref
// resolution reads from a fresh clone (one git fetch per repo, not one per
// entry). It adds only the batch concerns on top of fetchIntoClone: an entry
// with no locked SHA was never pulled and so has nothing to compare against,
// and an unparseable or URL-less reference is skipped.
func refreshRemoteRepos(ctx context.Context, cfg *config.Config, lockfile *remote.Lockfile) []RefreshFailure {
	cache := NewRepoCache(cfg)
	fetched := map[string]struct{}{}
	var failures []RefreshFailure
	for _, e := range lockfile.AllEntries() {
		if e.Entry.SHA == "" {
			continue
		}
		ref, err := remote.ParseReference(string(e.Ref))
		if err != nil || ref.URL == "" {
			continue
		}
		if _, ok := fetched[ref.URL]; ok {
			continue
		}
		fetched[ref.URL] = struct{}{}
		fetchIntoClone(ctx, cache, ref.URL, &failures)
	}
	return failures
}

// detectUpdates resolves the latest SHA for every lockfile entry and returns the
// changed ones. Entries with an empty SHA are counted in skipped and not
// checked; every other entry that could not be resolved is an Unchecked row,
// including one whose repository is among the refresh failures — its clone is
// stale, so it is not read.
func detectUpdates(ctx context.Context, cfg *config.Config, auth remote.AuthConfig, lockfile *remote.Lockfile, refresh []RefreshFailure) (updates []DependencyUpdate, unchecked []UncheckedDependency, skipped int) {
	notRefreshed := make(map[string]error, len(refresh))
	for _, f := range refresh {
		notRefreshed[f.URL] = f.Err
	}
	cachedFactory := NewCachedFetcherFactory(cfg)
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
		ref, err := remote.ParseReference(string(e.Ref))
		if err != nil {
			unchecked = append(unchecked, UncheckedDependency{Ref: string(e.Ref), Reason: UncheckedUnparseable, Err: err})
			continue
		}
		if ref.URL == "" {
			unchecked = append(unchecked, UncheckedDependency{Ref: string(e.Ref), Reason: UncheckedNoRepositoryURL})
			continue
		}
		if ferr, failed := notRefreshed[ref.URL]; failed {
			unchecked = append(unchecked, UncheckedDependency{Ref: string(e.Ref), URL: ref.URL, Reason: UncheckedNotRefreshed, Err: ferr})
			continue
		}
		fetcher, err := fetcherFor(ref.URL)
		if err != nil {
			unchecked = append(unchecked, UncheckedDependency{Ref: string(e.Ref), URL: ref.URL, Reason: UncheckedUnreachable, Err: err})
			continue
		}
		latest, ok, lerr := latestWithinConstraint(ctx, fetcher, ref.URL, e.Entry.RequestedVersion)
		if lerr != nil {
			unchecked = append(unchecked, UncheckedDependency{Ref: string(e.Ref), URL: ref.URL, Constraint: e.Entry.RequestedVersion, Reason: UncheckedUnresolvable, Err: lerr})
			continue
		}
		if !ok || latest == e.Entry.SHA {
			continue
		}
		updates = append(updates, DependencyUpdate{Type: e.Type, Ref: string(e.Ref), CurrentSHA: e.Entry.SHA, LatestSHA: latest, RequestedVersion: e.Entry.RequestedVersion, Kind: e.Entry.SelectorKind(), Version: e.Entry.Version})
	}
	return updates, unchecked, skipped
}

// latestWithinConstraint returns the newest commit the entry's version
// constraint allows — the highest tag in a semver range, the tip of a branch, or
// (for a constraint-less entry) the default branch's HEAD. An exact tag/SHA
// constraint resolves to itself, so it is never reported outdated. This is what
// makes the check constraint-aware: it reports an update only when a newer
// commit actually satisfies what the manifest asked for.
//
// err is non-nil only for a genuine resolution FAILURE (a malformed URL, a
// network/auth error reaching the forge); ok=false with a nil err means
// resolution ran cleanly and found nothing satisfying constraint — callers
// must be able to tell these two cases apart instead of both collapsing into
// "no update, all good."
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

// missingDefaultProfiles returns names of the default agent's composed
// profiles that don't exist (profiles.defaults was retired — see
// DefaultAgentProfiles).
func missingDefaultProfiles(cfg *config.Config) []string {
	defaultProfiles := cfg.DefaultAgentProfiles()
	if len(defaultProfiles) == 0 {
		return nil
	}

	var missing []string
	profileLoader := cfg.GetProfileLoader()

	for _, name := range defaultProfiles {
		if _, err := profileLoader.Load(name); err != nil {
			missing = append(missing, name)
		}
	}

	return missing
}
