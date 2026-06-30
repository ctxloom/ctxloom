package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Puller interface for pulling remote items (allows mocking in tests).
type Puller interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// LockDependenciesRequest contains parameters for generating a lockfile.
type LockDependenciesRequest struct {
	// SkipSync skips running sync before generating the lockfile.
	// By default, sync runs first to ensure all dependencies are installed
	// before locking their versions. Set to true to skip this behavior.
	SkipSync bool `json:"skip_sync"`

	// FailOnConflict makes a dependency hash conflict a hard error, for callers
	// that want strict locking. When false (startup auto-lock) the conflict is
	// warned and the conflicted items are dropped, never blocking the session
	// (CLAUDE.md).
	FailOnConflict bool `json:"-"`

	// StageUntrustedNew routes a FIRST INSTALL (a closure pin with no existing
	// active lockfile entry) from an untrusted remote into the pending lockfile
	// for `ctxloom bundle review` instead of the active one. SECURITY: the
	// startup auto-lock (runSyncPostSteps) sets this so never-reviewed remote
	// content cannot reach the active lockfile — and so cannot activate its
	// hooks/MCP/context — without approval. Trust follows the registry's
	// TrustBundles flag, mirroring StageUpgrade. A deliberate, explicit lock
	// (the default) applies the full closure as before.
	StageUntrustedNew bool `json:"-"`

	FS afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)
}

// LockDependenciesResult contains the result of generating a lockfile.
type LockDependenciesResult struct {
	Status    string `json:"status"`
	Path      string `json:"path,omitempty"`
	ItemCount int    `json:"item_count,omitempty"`
	Message   string `json:"message,omitempty"`
}

// LockDependencies builds lock.yaml from the flattened transitive closure of
// the project's local profiles. Every reference is hash-pinned, so the closure
// is fully determined by what each item references — no resolution to "latest"
// (that is Relock/`update`). If the same item appears at two differing hashes
// anywhere in the closure that is a conflict: surfaced immediately as a hard
// error when FailOnConflict is set, else warned and the conflicted items
// dropped so startup is never blocked (CLAUDE.md).
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	// Run sync first so the clones the closure walk reads are present.
	if !req.SkipSync {
		if _, err := SyncDependencies(ctx, cfg, SyncDependenciesRequest{
			Force: false,
			Lock:  false, // don't recurse back into lock
			FS:    req.FS,
		}); err != nil {
			return nil, fmt.Errorf("failed to sync dependencies before locking: %w", err)
		}
	}

	pins, conflicts, unexpanded := FlattenDependencies(ctx, cfg, nil)
	if len(conflicts) > 0 {
		if req.FailOnConflict {
			return nil, ConflictError(conflicts)
		}
		clidiag.Warn("ctxloom", "%v", ConflictError(conflicts))
		pins = dropConflicted(pins, conflicts)
	}

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	// The previous lockfile anchors two safety nets across the closure rebuild:
	// the per-item Pinned flag (the user's "do not upgrade this" mark must
	// survive a relock), the first-install detection for StageUntrustedNew, and
	// the preserved entries when the closure is incomplete. An unreadable
	// previous lockfile degrades to an empty one.
	prev, prevErr := lockManager.Load()
	if prevErr != nil {
		prev = &remote.Lockfile{Bundles: map[string]remote.LockEntry{}, Profiles: map[string]remote.LockEntry{}}
	}
	prevPinned := map[string]bool{}
	for _, e := range prev.AllEntries() {
		if e.Entry.Pinned {
			prevPinned[string(e.Type)+"\x00"+e.Ref] = true
		}
	}

	// Trust is only consulted for the StageUntrustedNew gate; registry load is
	// best-effort (nil registry → nothing is trusted, the safe default).
	var registry *remote.Registry
	if req.StageUntrustedNew {
		registry, _ = remote.NewRegistry(paths.RemotesPath(baseDir), remote.WithRegistryFS(fs))
	}

	lockfile := &remote.Lockfile{
		Version:  1,
		Bundles:  make(map[string]remote.LockEntry),
		Profiles: make(map[string]remote.LockEntry),
	}
	var staged []PinnedRef
	for _, p := range pins {
		// SECURITY: a first install from an untrusted remote is staged for
		// review, never activated (see LockDependenciesRequest.StageUntrustedNew).
		if req.StageUntrustedNew {
			if _, had := prev.GetEntry(p.Type, p.Identity); !had &&
				!isTrustedRemote(registry, remoteNameForKey(registry, p.Identity)) {
				staged = append(staged, p)
				continue
			}
		}
		// RequestedVersion records the manifest constraint so a later relock can
		// carry this SHA forward while the constraint is unchanged; Version records
		// the tag a semver constraint chose, for display and satisfaction checks.
		entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version}
		if prevPinned[string(p.Type)+"\x00"+p.Identity] {
			entry.Pinned = true
		}
		lockfile.AddEntry(p.Type, p.Identity, entry)
	}

	// An INCOMPLETE closure (a remote parent profile could not be expanded)
	// must not erase healthy entries: merge in every previous entry the rebuilt
	// closure no longer reaches, so a transient fetch failure never loses lock
	// state. The next complete relock drops genuinely-removed entries.
	if len(unexpanded) > 0 {
		preserved := 0
		for _, e := range prev.AllEntries() {
			if _, ok := lockfile.GetEntry(e.Type, e.Ref); !ok {
				lockfile.AddEntry(e.Type, e.Ref, e.Entry)
				preserved++
			}
		}
		if preserved > 0 {
			clidiag.Warn("ctxloom", "dependency closure is incomplete (%d parent profile(s) unreachable); preserving %d existing lockfile entry(ies)", len(unexpanded), preserved)
		}
	}

	stageNewForReview(baseDir, fs, staged)

	if lockfile.IsEmpty() {
		return &LockDependenciesResult{
			Status:  "empty",
			Message: "No remote items found",
		}, nil
	}

	if err := lockManager.Save(lockfile); err != nil {
		return nil, err
	}

	return &LockDependenciesResult{
		Status:    "generated",
		Path:      lockManager.Path(),
		ItemCount: len(lockfile.AllEntries()),
	}, nil
}

// stageNewForReview merges untrusted first-install pins into the pending
// lockfile and warns that they await `ctxloom bundle review`. Best-effort:
// a pending load/save failure warns but never blocks the lock (CLAUDE.md) —
// the entries simply stay out of the active lockfile either way.
func stageNewForReview(baseDir string, fs afero.Fs, staged []PinnedRef) {
	if len(staged) == 0 {
		return
	}
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs), remote.WithPendingLockfile())
	pending, err := pendingMgr.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "failed to load pending lockfile: %v", err)
		return
	}
	for _, p := range staged {
		pending.AddEntry(p.Type, p.Identity, remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version})
	}
	if err := pendingMgr.Save(pending); err != nil {
		clidiag.Warn("ctxloom", "failed to stage new bundles for review: %v", err)
		return
	}
	clidiag.Warn("ctxloom", "%d new bundle(s) staged pending review — run 'ctxloom bundle review'", len(staged))
}

// dropConflicted removes every pin whose identity is in conflicts — used by the
// startup auto-lock to degrade past a conflict rather than block the session.
func dropConflicted(pins []PinnedRef, conflicts []DependencyConflict) []PinnedRef {
	bad := make(map[string]struct{}, len(conflicts))
	for _, c := range conflicts {
		bad[c.Item] = struct{}{}
	}
	kept := pins[:0]
	for _, p := range pins {
		if _, isBad := bad[p.Identity]; !isBad {
			kept = append(kept, p)
		}
	}
	return kept
}

// InstallDependenciesRequest contains parameters for installing from lockfile.
type InstallDependenciesRequest struct {
	Force bool `json:"force"`

	// Testing injection points
	FS          afero.Fs                `json:"-"` // Optional filesystem for testing
	LockManager *remote.LockfileManager `json:"-"` // Optional lock manager for testing
	Registry    *remote.Registry        `json:"-"` // Optional registry for testing
	Puller      Puller                  `json:"-"` // Optional puller for testing
}

// InstallDependenciesResult contains the result of installing from lockfile.
type InstallDependenciesResult struct {
	Status    string   `json:"status"`
	Installed int      `json:"installed"`
	Failed    int      `json:"failed"`
	Total     int      `json:"total"`
	Errors    []string `json:"errors,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// InstallDependencies installs all items from the lockfile.
func InstallDependencies(ctx context.Context, cfg *config.Config, req InstallDependenciesRequest) (*InstallDependenciesResult, error) {
	baseDir := getBaseDir(cfg)
	fs := getFS(req.FS)

	// Use injected lock manager or create new one
	lockManager := req.LockManager
	if lockManager == nil {
		lockManager = remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	}

	lockfile, err := lockManager.Load()
	if err != nil {
		return nil, err
	}

	if lockfile.IsEmpty() {
		return &InstallDependenciesResult{
			Status:  "empty",
			Message: "No entries in lockfile",
		}, nil
	}

	// Use injected registry or create new one
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = remote.NewRegistry(paths.RemotesPath(baseDir), remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	// Use injected puller or create new one
	puller := req.Puller
	if puller == nil {
		auth := remote.LoadAuth("")
		puller = remote.NewPuller(registry, auth)
	}

	entries := lockfile.AllEntries()
	installed := 0
	failed := 0
	var errors []string

	for _, e := range entries {
		// Pin to the full locked SHA, not the 7-char abbreviation: the puller
		// resolves an exact commit, and an abbreviated SHA is a weaker (and
		// ambiguity-prone) reference than what the lockfile actually recorded.
		ref := fmt.Sprintf("%s@%s", e.Ref, e.Entry.SHA)

		opts := remote.PullOptions{
			Force:    req.Force,
			ItemType: e.Type,
			LocalDir: baseDir,
		}

		_, err := puller.Pull(ctx, ref, opts)
		if err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("%s: %v", e.Ref, err))
			continue
		}
		installed++
	}

	result := &InstallDependenciesResult{
		Status:    "completed",
		Installed: installed,
		Failed:    failed,
		Total:     len(entries),
		Errors:    errors,
	}

	return result, nil
}

// FetcherFactory is a function that creates a Fetcher for a given URL.
type FetcherFactory func(url string, auth remote.AuthConfig) (remote.Fetcher, error)

// RepoUpdater is the subset of *remote.RepoCache that CheckOutdated needs for
// its per-URL refresh pre-pass. Production uses *remote.RepoCache directly;
// tests inject a counting mock so the dedup invariant is observable.
type RepoUpdater interface {
	UpdateRepo(ctx context.Context, repoURL string, forgeType remote.ForgeType) (string, error)
}

// CheckOutdatedRequest contains parameters for checking outdated items.
type CheckOutdatedRequest struct {
	// Testing injection points
	FS             afero.Fs                `json:"-"` // Optional filesystem for testing
	LockManager    *remote.LockfileManager `json:"-"` // Optional lock manager for testing
	Registry       *remote.Registry        `json:"-"` // Optional registry for testing
	FetcherFactory FetcherFactory          `json:"-"` // Optional fetcher factory for testing
	RepoCache      RepoUpdater             `json:"-"` // Optional repo updater for testing the per-URL refresh dedup
}

// OutdatedItem represents an item with a newer version available.
type OutdatedItem struct {
	Type      string `json:"type"`
	Reference string `json:"reference"`
	LockedSHA string `json:"locked_sha"`
	LatestSHA string `json:"latest_sha"`
}

// CheckOutdatedResult contains the result of checking for outdated items.
type CheckOutdatedResult struct {
	Status  string         `json:"status"`
	Count   int            `json:"count,omitempty"`
	Items   []OutdatedItem `json:"items,omitempty"`
	Total   int            `json:"total,omitempty"`
	Message string         `json:"message,omitempty"`
}

// CheckOutdated checks if any locked items have newer versions available.
func CheckOutdated(ctx context.Context, cfg *config.Config, req CheckOutdatedRequest) (*CheckOutdatedResult, error) {
	entries, registry, auth, early, err := loadOutdatedInputs(cfg, req)
	if err != nil {
		return nil, err
	}
	if early != nil {
		return early, nil
	}

	// Use injected fetcher factory or the cached one. The cached factory clones
	// once per repo URL and serves every subsequent resolve from the local
	// clone — but it does not refresh on its own. We explicitly UpdateRepo per
	// unique URL below so "outdated" can actually be detected.
	fetcherFactory := req.FetcherFactory
	if fetcherFactory == nil {
		cached := newCachedFetcherFactory(cfg)
		fetcherFactory = FetcherFactory(cached)
	}

	// Dedup repos so we run one git fetch per unique URL instead of 2×N API
	// calls per lockfile entry. Run when we have a real cache to refresh —
	// either an injected one (for tests asserting dedup) or the production
	// default (when no fetcher mock is in play).
	if req.RepoCache != nil || req.FetcherFactory == nil {
		cache := req.RepoCache
		if cache == nil {
			cache = newRepoCache(cfg)
		}
		refreshRepoCaches(ctx, cache, uniqueRemoteURLs(entries, registry))
	}

	outdated := findOutdatedEntries(ctx, entries, registry, auth, fetcherFactory)

	if len(outdated) == 0 {
		return &CheckOutdatedResult{
			Status:  "up_to_date",
			Message: "All items are up to date",
		}, nil
	}

	return &CheckOutdatedResult{
		Status: "outdated",
		Count:  len(outdated),
		Items:  outdated,
		Total:  len(entries),
	}, nil
}

// loadOutdatedInputs prepares CheckOutdated's inputs: it loads the lockfile
// (via the injected or a new manager), opens the registry (injected or new),
// and returns the entries, registry, and auth. When the lockfile is empty it
// returns a non-nil early result the caller should return directly.
func loadOutdatedInputs(cfg *config.Config, req CheckOutdatedRequest) (entries []struct {
	Type  remote.ItemType
	Ref   string
	Entry remote.LockEntry
}, registry *remote.Registry, auth remote.AuthConfig, early *CheckOutdatedResult, err error) {
	baseDir := getBaseDir(cfg)
	fs := getFS(req.FS)

	lockManager := req.LockManager
	if lockManager == nil {
		lockManager = remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	}
	lockfile, err := lockManager.Load()
	if err != nil {
		return nil, nil, auth, nil, err
	}
	if lockfile.IsEmpty() {
		return nil, nil, auth, &CheckOutdatedResult{Status: "empty", Message: "No entries in lockfile"}, nil
	}

	registry = req.Registry
	if registry == nil {
		registry, err = remote.NewRegistry(paths.RemotesPath(baseDir), remote.WithRegistryFS(fs))
		if err != nil {
			return nil, nil, auth, nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	return lockfile.AllEntries(), registry, remote.LoadAuth(baseDir), nil, nil
}

// findOutdatedEntries resolves each lockfile entry's latest remote SHA and
// returns those whose locked SHA differs. Entries with unparseable refs,
// unknown remotes, or resolution failures are skipped (best-effort). One
// fetcher is reused per repo URL across entries.
func findOutdatedEntries(ctx context.Context, entries []struct {
	Type  remote.ItemType
	Ref   string
	Entry remote.LockEntry
}, registry *remote.Registry, auth remote.AuthConfig, factory FetcherFactory) []OutdatedItem {
	var outdated []OutdatedItem
	fetcherByURL := map[string]remote.Fetcher{}
	for _, e := range entries {
		repoURL := repoURLForEntry(e.Ref, e.Entry, registry)
		if repoURL == "" {
			continue
		}
		latestSHA, err := latestWithinConstraintSHA(ctx, repoURL, e.Entry.RequestedVersion, auth, factory, fetcherByURL)
		if err != nil {
			continue
		}
		if latestSHA != e.Entry.SHA {
			outdated = append(outdated, OutdatedItem{
				Type:      string(e.Type),
				Reference: e.Ref,
				LockedSHA: shortSHA(e.Entry.SHA),
				LatestSHA: shortSHA(latestSHA),
			})
		}
	}
	return outdated
}

// uniqueRemoteURLs returns the unique repo URLs referenced by entries, in the
// order they first appear. Unparseable refs or unknown remotes are skipped.
// Used to ensure per-URL operations (clone-cache refresh, fetcher
// construction) run once per repo even when many lockfile entries share a URL.
func uniqueRemoteURLs(entries []struct {
	Type  remote.ItemType
	Ref   string
	Entry remote.LockEntry
}, registry *remote.Registry) []string {
	seen := map[string]struct{}{}
	var urls []string
	for _, e := range entries {
		repoURL := repoURLForEntry(e.Ref, e.Entry, registry)
		if repoURL == "" {
			continue
		}
		if _, ok := seen[repoURL]; ok {
			continue
		}
		seen[repoURL] = struct{}{}
		urls = append(urls, repoURL)
	}
	return urls
}

// repoURLForEntry resolves a lockfile entry's repo URL. Canonical lockfile
// entries record it directly (entry.URL); a missing URL yields "".
func repoURLForEntry(_ string, entry remote.LockEntry, _ *remote.Registry) string {
	return entry.URL
}

// refreshRepoCaches advances each unique clone to live HEAD before SHA
// resolution, so a stale shallow clone doesn't pin/report the old SHA. Per-URL
// failures warn and continue — stale data beats aborting. Shared by Relock and
// CheckOutdated.
func refreshRepoCaches(ctx context.Context, cache RepoUpdater, urls []string) {
	for _, url := range urls {
		forgeType, _, err := remote.DetectForge(url)
		if err != nil {
			clidiag.Warn("ctxloom", "detect forge for %s: %v", url, err)
			continue
		}
		if _, err := cache.UpdateRepo(ctx, url, forgeType); err != nil {
			clidiag.Warn("ctxloom", "fetch %s: %v", url, err)
		}
	}
}

// latestWithinConstraintSHA returns the newest commit repoURL allows under the
// entry's version constraint — the highest tag in a semver range, the tip of a
// branch, or (for a constraint-less entry) default-branch HEAD. This is what
// makes `outdated` constraint-aware: a dependency pinned to a tag/range is only
// reported outdated when a newer commit actually satisfies what the manifest
// asked for, not merely when branch HEAD moves past a pinned tag. Reuses a cached
// fetcher per URL (fetcherByURL is read and populated).
func latestWithinConstraintSHA(ctx context.Context, repoURL, constraint string, auth remote.AuthConfig, factory FetcherFactory, fetcherByURL map[string]remote.Fetcher) (string, error) {
	fetcher, ok := fetcherByURL[repoURL]
	if !ok {
		f, err := factory(repoURL, auth)
		if err != nil {
			return "", err
		}
		fetcher = f
		fetcherByURL[repoURL] = fetcher
	}
	owner, repoName, err := remote.ParseRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	res, err := remote.ResolveConstraint(ctx, constraint, remote.NewFetcherRepoVersions(fetcher, owner, repoName))
	if err != nil {
		return "", err
	}
	if res.SHA == "" {
		return "", fmt.Errorf("no commit satisfies constraint %q for %s", constraint, repoURL)
	}
	return res.SHA, nil
}
