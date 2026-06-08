package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
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

	FS afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)
}

// LockDependenciesResult contains the result of generating a lockfile.
type LockDependenciesResult struct {
	Status    string `json:"status"`
	Path      string `json:"path,omitempty"`
	ItemCount int    `json:"item_count,omitempty"`
	Message   string `json:"message,omitempty"`
}

// LockDependencies generates a lockfile from currently installed remote items.
// By default, it runs sync first to ensure all dependencies are installed before
// locking their versions. Use SkipSync to disable this behavior.
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	// Run sync first to ensure all dependencies are installed
	// This prevents generating an incomplete lockfile if ephemeral was cleared
	if !req.SkipSync {
		_, err := SyncDependencies(ctx, cfg, SyncDependenciesRequest{
			Force: false,
			Lock:  false, // Don't recursively call lock
			FS:    req.FS,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sync dependencies before locking: %w", err)
		}
	}

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Version:  1,
		Bundles:  make(map[string]remote.LockEntry),
		Profiles: make(map[string]remote.LockEntry),
	}

	// Scan installed bundles and profiles for their install-time _source SHA.
	itemCount := 0
	itemCount += scanInstalledEntries(fs, paths.BundlesPath(baseDir), remote.ItemTypeBundle, lockfile)
	itemCount += scanInstalledEntries(fs, paths.ProfilesPath(baseDir), remote.ItemTypeProfile, lockfile)

	if itemCount == 0 {
		return &LockDependenciesResult{
			Status:  "empty",
			Message: "No remote items with source metadata found",
		}, nil
	}

	if err := lockManager.Save(lockfile); err != nil {
		return nil, err
	}

	return &LockDependenciesResult{
		Status:    "generated",
		Path:      lockManager.Path(),
		ItemCount: itemCount,
	}, nil
}

// RelockRequest contains parameters for regenerating the lockfile from the
// project's configured profiles.
type RelockRequest struct {
	FS afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)

	// Registry lets tests inject a pre-built registry. When nil one is opened
	// from the project's remotes dir.
	Registry *remote.Registry `json:"-"`

	// FetcherFactory lets tests inject a fetcher factory. When nil the cached
	// clone-backed factory is used.
	FetcherFactory FetcherFactory `json:"-"`

	// RepoCache lets tests inject a counting/fake repo updater for the per-URL
	// refresh pre-pass. When nil the production clone cache is used.
	RepoCache RepoUpdater `json:"-"`
}

// RelockResult contains the result of regenerating the lockfile.
type RelockResult struct {
	Status    string   `json:"status"` // "regenerated" or "empty"
	Path      string   `json:"path,omitempty"`
	ItemCount int      `json:"item_count,omitempty"`
	Failed    int      `json:"failed,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// Relock regenerates lock.yaml from the project's configured/installed profiles,
// re-pinning every bundle and parent-profile dependency at the current
// default-branch (origin/main) SHA.
//
// Unlike LockDependencies — which scans already-installed files for the
// install-time _source SHA they carry — Relock walks the live dependency graph
// from the configured profiles (following local parents) and resolves each
// remote's current HEAD. That means it works even when lock.yaml does NOT exist
// yet, which is the whole point: it regenerates a deleted/missing lockfile.
//
// Fault tolerance (CLAUDE.md): a per-URL fetch failure or an unresolvable entry
// is warned and skipped; whatever resolves cleanly is still written. Only a
// hard registry/collection failure aborts.
func Relock(ctx context.Context, cfg *config.Config, req RelockRequest) (*RelockResult, error) {
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = remote.NewRegistry(paths.RemotesPath(baseDir), remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	auth := remote.LoadAuth(baseDir)

	// Walk the configured profiles (and their local parents) to discover every
	// remote bundle + profile reference that should be pinned.
	bundleRefs, profileRefs, err := collectRemoteReferences(cfg, nil, fs)
	if err != nil {
		return nil, fmt.Errorf("failed to collect references: %w", err)
	}

	var items []relockItem
	for _, r := range profileRefs {
		items = append(items, relockItem{remote.ItemTypeProfile, r})
	}
	for _, r := range bundleRefs {
		items = append(items, relockItem{remote.ItemTypeBundle, r})
	}

	// repoURLForRef resolves a reference to its repository URL. Canonical URL
	// refs (e.g. a profile parent "https://…@profiles/name") carry the URL
	// directly and have no registry name; simple "remote/path" refs resolve
	// their URL via the registry.
	repoURLForRef := func(ref *remote.Reference) (string, bool) {
		// Canonical refs carry the URL directly; nothing else resolves now that
		// the short "repo/path" form is gone.
		return ref.URL, ref.URL != ""
	}

	urlForRef := func(refStr string) (string, bool) {
		ref, perr := remote.ParseReference(refStr)
		if perr != nil {
			return "", false
		}
		return repoURLForRef(ref)
	}

	// Refresh every unique remote clone first so origin/main is advanced to the
	// live HEAD before we resolve SHAs. Without this, a stale shallow clone
	// would re-pin at the old SHA.
	cache := req.RepoCache
	if cache == nil {
		cache = newRepoCache(cfg)
	}
	refreshRepoCaches(ctx, cache, uniqueRefURLs(items, urlForRef))

	// Fetcher factory backed by the (now-refreshed) local clones.
	fetcherFactory := req.FetcherFactory
	if fetcherFactory == nil {
		cached := newCachedFetcherFactory(cfg)
		fetcherFactory = FetcherFactory(cached)
	}

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Version:  1,
		Bundles:  make(map[string]remote.LockEntry),
		Profiles: make(map[string]remote.LockEntry),
	}

	itemCount, failed, relockErrs := pinRelockEntries(ctx, lockfile, items, repoURLForRef, auth, fetcherFactory)

	if itemCount == 0 {
		return &RelockResult{
			Status:  "empty",
			Failed:  failed,
			Errors:  relockErrs,
			Message: "No resolvable dependencies found in configured profiles",
		}, nil
	}

	if err := lockManager.Save(lockfile); err != nil {
		return nil, fmt.Errorf("failed to write lockfile: %w", err)
	}

	return &RelockResult{
		Status:    "regenerated",
		Path:      lockManager.Path(),
		ItemCount: itemCount,
		Failed:    failed,
		Errors:    relockErrs,
	}, nil
}

// scanInstalledEntries walks itemDir/<remote>/**/*.yaml, reads each file's
// install-time `_source` metadata, and adds an entry to lockfile for every file
// carrying a SHA. Returns the number of entries added. Unreadable dirs/files and
// files lacking _source.SHA are silently skipped. Used by LockDependencies to
// rebuild the lockfile from what's already installed on disk.
func scanInstalledEntries(fs afero.Fs, itemDir string, itemType remote.ItemType, lockfile *remote.Lockfile) int {
	entries, err := afero.ReadDir(fs, itemDir)
	if err != nil {
		return 0
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		remoteName := entry.Name()
		remoteDir := filepath.Join(itemDir, remoteName)

		files, _ := afero.Glob(fs, filepath.Join(remoteDir, "**", "*.yaml"))
		rootFiles, _ := afero.Glob(fs, filepath.Join(remoteDir, "*.yaml"))
		files = append(files, rootFiles...)

		for _, file := range files {
			content, err := afero.ReadFile(fs, file)
			if err != nil {
				continue
			}
			var meta struct {
				Source remote.SourceMeta `yaml:"_source"`
			}
			if err := yaml.Unmarshal(content, &meta); err != nil {
				continue
			}
			if meta.Source.SHA == "" {
				continue
			}

			if meta.Source.URL == "" {
				continue
			}
			relPath, _ := filepath.Rel(remoteDir, file)
			name := strings.TrimSuffix(filepath.ToSlash(relPath), ".yaml")
			// Lockfile key is the canonical ref, derived from the install-time
			// source URL recorded in _source.
			ref := (&remote.Reference{
				URL:      meta.Source.URL,
				ItemType: itemType,
				Path:     name,
			}).CanonicalString()
			lockfile.AddEntry(itemType, ref, remote.LockEntry{
				SHA:       meta.Source.SHA,
				URL:       meta.Source.URL,
				FetchedAt: meta.Source.FetchedAt,
			})
			count++
		}
	}
	return count
}

// relockItem is a typed (itemType, ref) pair queued for relock pinning.
type relockItem struct {
	Type remote.ItemType
	Ref  string
}

// uniqueRefURLs returns the unique repo URLs for items, first-seen order,
// resolving each ref via urlForRef. Refs that don't resolve are skipped. Used to
// fetch each repo's clone once before SHA resolution.
func uniqueRefURLs(items []relockItem, urlForRef func(string) (string, bool)) []string {
	seen := map[string]struct{}{}
	var urls []string
	for _, it := range items {
		url, ok := urlForRef(it.Ref)
		if !ok {
			continue
		}
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}
	return urls
}

// pinRelockEntries resolves each item's current default-branch SHA and adds it
// to lockfile, keyed by the canonical ref (ref.CanonicalString) — the sole
// content identity. Per CLAUDE.md fault tolerance, every per-item
// failure is counted and recorded but skipped — whatever resolves is still
// pinned. Returns the pinned count, failure count, and per-failure messages.
func pinRelockEntries(ctx context.Context, lockfile *remote.Lockfile, items []relockItem, repoURLForRef func(*remote.Reference) (string, bool), auth remote.AuthConfig, factory FetcherFactory) (itemCount, failed int, errs []string) {
	fetchedAt := time.Now().UTC()
	fetcherByURL := map[string]remote.Fetcher{}

	for _, it := range items {
		if it.Ref == "" {
			continue
		}
		ref, perr := remote.ParseReference(it.Ref)
		if perr != nil {
			failed++
			errs = append(errs, fmt.Sprintf("%s: invalid reference (skipped)", it.Ref))
			continue
		}
		repoURL, ok := repoURLForRef(ref)
		if !ok {
			failed++
			errs = append(errs, fmt.Sprintf("%s: remote not found (skipped)", it.Ref))
			continue
		}

		sha, serr := resolveLatestSHA(ctx, repoURL, auth, factory, fetcherByURL)
		if serr != nil || sha == "" {
			failed++
			errs = append(errs, fmt.Sprintf("%s: no SHA resolved (skipped)", it.Ref))
			fmt.Fprintf(os.Stderr, "ctxloom: warning: could not resolve SHA for %s, skipping\n", it.Ref)
			continue
		}

		lockfile.AddEntry(it.Type, ref.CanonicalString(), remote.LockEntry{
			SHA:       sha,
			URL:       repoURL,
			FetchedAt: fetchedAt,
		})
		itemCount++
	}
	return itemCount, failed, errs
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
		ref := fmt.Sprintf("%s@%s", e.Ref, shortSHA(e.Entry.SHA))

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
		latestSHA, err := resolveLatestSHA(ctx, repoURL, auth, factory, fetcherByURL)
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
			fmt.Fprintf(os.Stderr, "ctxloom: warning: detect forge for %s: %v\n", url, err)
			continue
		}
		if _, err := cache.UpdateRepo(ctx, url, forgeType); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: fetch %s: %v\n", url, err)
		}
	}
}

// resolveLatestSHA returns the default-branch HEAD SHA for repoURL, reusing a
// cached fetcher per URL (fetcherByURL is read and populated). It centralizes
// the fetcher-create → ParseRepoURL → GetDefaultBranch → ResolveRef chain shared
// by Relock and CheckOutdated. Shared by Relock and CheckOutdated.
func resolveLatestSHA(ctx context.Context, repoURL string, auth remote.AuthConfig, factory FetcherFactory, fetcherByURL map[string]remote.Fetcher) (string, error) {
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
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repoName)
	if err != nil {
		return "", err
	}
	sha, err := fetcher.ResolveRef(ctx, owner, repoName, branch)
	if err != nil {
		return "", err
	}
	return sha, nil
}
