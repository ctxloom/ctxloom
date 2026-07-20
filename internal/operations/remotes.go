package operations

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// getBaseDir returns the ctxloom directory from config, defaulting to ".ctxloom".
func getBaseDir(cfg *config.Config) string {
	if cfg != nil && len(cfg.GetAppPaths()) > 0 {
		return cfg.GetAppPaths()[0]
	}
	return ".ctxloom"
}

// getRegistry creates a registry using the config's ctxloom path.
func getRegistry(cfg *config.Config, opts ...remote.RegistryOption) (*remote.Registry, error) {
	baseDir := getBaseDir(cfg)
	return remote.NewRegistry(paths.RemotesPath(baseDir), opts...)
}

// RemoteEntry represents a remote in operation results. A remote is now just an
// address to fetch from — it carries no trust flag (spec §11); trust is a
// property of the publisher KEY, surfaced by `ctxloom signer list`.
type RemoteEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ListRemotesRequest is empty but exists for consistency.
type ListRemotesRequest struct {
	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// ListRemotesResult contains the list of configured remotes.
type ListRemotesResult struct {
	Remotes []RemoteEntry `json:"remotes"`
	Count   int           `json:"count"`
	Default string        `json:"default,omitempty"`
}

// ListRemotes returns all configured remotes.
func ListRemotes(ctx context.Context, cfg *config.Config, req ListRemotesRequest) (*ListRemotesResult, error) {
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
		if err != nil {
			return nil, fmt.Errorf("failed to load registry: %w", err)
		}
	}

	remotes := registry.List()

	result := &ListRemotesResult{
		Remotes: make([]RemoteEntry, 0, len(remotes)),
		Count:   len(remotes),
		Default: registry.GetDefault(),
	}

	for _, r := range remotes {
		result.Remotes = append(result.Remotes, RemoteEntry{
			Name: r.Name,
			URL:  r.URL,
		})
	}

	return result, nil
}

// AddRemoteRequest contains parameters for adding a remote.
type AddRemoteRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`

	// Forge optionally binds the remote to a named forge instance (a forges:
	// label or a built-in: "github" / "git"). Empty lets the forge resolve from
	// the URL host (configured base_url match → built-in github for github.com →
	// generic git).
	Forge string `json:"forge,omitempty"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Fetcher is an optional pre-configured fetcher (for testing).
	Fetcher remote.Fetcher `json:"-"`
	// Cache is an optional pre-configured repo cache for the eager clone-on-add
	// (for testing — a fake avoids real git/network).
	Cache repoCloner `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// AddRemoteResult contains the result of adding a remote.
type AddRemoteResult struct {
	Status  string `json:"status"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Warning string `json:"warning,omitempty"`
}

// AddRemote registers a new remote source.
func AddRemote(ctx context.Context, cfg *config.Config, req AddRemoteRequest) (*AddRemoteResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	if err := registry.Add(req.Name, req.URL); err != nil {
		return nil, err
	}

	// Bind the forge before verifying/cloning so both resolve against the chosen
	// adapter. An unknown label rolls the registration back.
	if req.Forge != "" {
		if err := registry.SetForge(req.Name, req.Forge); err != nil {
			_ = registry.Remove(req.Name)
			return nil, err
		}
	}

	// Verify the remote
	fetcher := req.Fetcher
	if fetcher == nil {
		var err error
		fetcher, err = getCachedFetcher(cfg, req.URL)
		if err != nil {
			_ = registry.Remove(req.Name)
			return nil, fmt.Errorf("failed to create fetcher: %w", err)
		}
	}

	owner, repo, err := remote.ParseRepoURL(req.URL)
	if err != nil {
		_ = registry.Remove(req.Name)
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	valid, validErr := fetcher.ValidateRepo(ctx, owner, repo)

	// A remote carries no trust on add. Its content is born pending and takes
	// the review path until either a human reviews it or its publisher key is
	// added to allowed_signers — adding a REMOTE (an address) and trusting a
	// PUBLISHER (a key) are now separate acts, deliberately (spec §11).

	rem, err := registry.Get(req.Name)
	if err != nil || rem == nil {
		_ = registry.Remove(req.Name)
		return nil, fmt.Errorf("remote %q vanished after add: %w", req.Name, err)
	}

	result := &AddRemoteResult{
		Status: "added",
		Name:   req.Name,
		URL:    rem.URL,
	}
	if !valid {
		// A false result can mean either a genuinely-missing ctxloom/ directory
		// (validErr nil) or that validation itself failed (network/auth). Don't
		// report the "no directory structure" text when the real cause was an
		// error — that would mislead the user into thinking the repo is wrong.
		if validErr != nil {
			result.Warning = fmt.Sprintf("could not validate repository: %v", validErr)
		} else {
			result.Warning = "repository does not have a ctxloom/ directory structure"
		}
	}

	// Eagerly clone the remote into the local cache so a bad URL, missing auth,
	// or network problem surfaces now (at add time) rather than on first use —
	// friction up front. Best-effort (CLAUDE.md): a failed clone leaves the
	// remote registered with a warning so a transient issue doesn't block
	// registration; `remote update` retries the clone later.
	cache := req.Cache
	if cache == nil {
		cache = newRepoCache(cfg)
	}
	if msg := ensureClone(ctx, cache, rem); msg != "" {
		result.Warning = appendWarning(result.Warning, msg)
		clidiag.Warn("ctxloom", "%s", msg)
	}

	return result, nil
}

// RemoveRemoteRequest contains parameters for removing a remote.
type RemoveRemoteRequest struct {
	Name string `json:"name"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// RemoveRemoteResult contains the result of removing a remote.
type RemoveRemoteResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// RemoveRemote unregisters a remote source.
func RemoveRemote(ctx context.Context, cfg *config.Config, req RemoveRemoteRequest) (*RemoveRemoteResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	if err := registry.Remove(req.Name); err != nil {
		return nil, err
	}

	return &RemoveRemoteResult{
		Status: "removed",
		Name:   req.Name,
	}, nil
}

// DefaultRemoteRequest is the input for Get/SetDefaultRemote.
type DefaultRemoteRequest struct {
	Name string `json:"name,omitempty"` // SetDefaultRemote: "" clears the default

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// FS is an optional filesystem (for testing).
	FS afero.Fs `json:"-"`
}

// DefaultRemoteResult reports the default-remote state. Status is "ok" (read),
// "set", or "cleared".
type DefaultRemoteResult struct {
	Status string `json:"status"`
	Name   string `json:"name,omitempty"`
}

// GetDefaultRemote returns the configured default remote (empty when none).
func GetDefaultRemote(_ context.Context, cfg *config.Config, req DefaultRemoteRequest) (*DefaultRemoteResult, error) {
	registry, err := defaultRemoteRegistry(cfg, req)
	if err != nil {
		return nil, err
	}
	return &DefaultRemoteResult{Status: "ok", Name: registry.GetDefault()}, nil
}

// SetDefaultRemote sets the default remote, or clears it when Name is empty.
func SetDefaultRemote(_ context.Context, cfg *config.Config, req DefaultRemoteRequest) (*DefaultRemoteResult, error) {
	registry, err := defaultRemoteRegistry(cfg, req)
	if err != nil {
		return nil, err
	}
	if err := registry.SetDefault(req.Name); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return &DefaultRemoteResult{Status: "cleared"}, nil
	}
	return &DefaultRemoteResult{Status: "set", Name: req.Name}, nil
}

func defaultRemoteRegistry(cfg *config.Config, req DefaultRemoteRequest) (*remote.Registry, error) {
	if req.Registry != nil {
		return req.Registry, nil
	}
	registry, err := getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize registry: %w", err)
	}
	return registry, nil
}

// `ctxloom remote trust|untrust` and its SetRemoteTrust operation are DELETED,
// not deprecated (signature-envelope spec §11). Source trust — "everything this
// URL publishes reaches the agent unreviewed" — was hash-blind and is gone.
// Trusting a publisher is now `ctxloom signer add <principal> --key …`, which
// trusts a KEY and verifies the bytes. There is no parallel dormant path.

// UpdateRemoteRequest contains parameters for updating cached remote repos.
type UpdateRemoteRequest struct {
	// Name is an optional remote name. If empty, all remotes are updated.
	Name string `json:"name,omitempty"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
}

// UpdateRemoteResult contains the result of updating remote repos.
type UpdateRemoteResult struct {
	Status  string              `json:"status"`
	Updated []UpdateRemoteEntry `json:"updated,omitempty"`
	Errors  []string            `json:"errors,omitempty"`
}

// UpdateRemoteEntry represents a single updated remote.
type UpdateRemoteEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// UpdateRemote fetches the latest changes for cached remote repos.
// If name is specified, only that remote is updated. Otherwise all remotes are updated.
func UpdateRemote(ctx context.Context, cfg *config.Config, req UpdateRemoteRequest) (*UpdateRemoteResult, error) {
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to load registry: %w", err)
		}
	}

	cache := newRepoCache(cfg)

	var remotes []*remote.Remote
	if req.Name != "" {
		rem, err := registry.Get(req.Name)
		if err != nil {
			return nil, err
		}
		remotes = []*remote.Remote{rem}
	} else {
		remotes = registry.List()
	}

	if len(remotes) == 0 {
		return &UpdateRemoteResult{
			Status: "empty",
		}, nil
	}

	result := &UpdateRemoteResult{
		Status: "completed",
	}

	for _, rem := range remotes {
		forgeType, _, err := remote.DetectForge(rem.URL)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rem.Name, err))
			continue
		}

		_, err = cache.UpdateRepo(ctx, rem.URL, forgeType)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rem.Name, err))
			continue
		}

		result.Updated = append(result.Updated, UpdateRemoteEntry{
			Name: rem.Name,
			URL:  rem.URL,
		})
	}

	if len(result.Errors) > 0 {
		result.Status = "completed_with_errors"
	}

	return result, nil
}

// DiscoverRemotesRequest contains parameters for discovering remote repositories.
type DiscoverRemotesRequest struct {
	Query    string `json:"query"`
	Source   string `json:"source"` // "github" or "all" (only GitHub offers repo search)
	MinStars int    `json:"min_stars"`
	Limit    int    `json:"limit"`

	// GitHubFetcher is an optional pre-configured GitHub fetcher (for testing).
	GitHubFetcher remote.Fetcher `json:"-"`
}

// RepoEntry represents a discovered repository.
type RepoEntry struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Stars       int    `json:"stars"`
	URL         string `json:"url"`
	Forge       string `json:"forge"`
	AddCommand  string `json:"add_command"`
}

// DiscoverRemotesResult contains discovered repositories.
type DiscoverRemotesResult struct {
	Repositories []RepoEntry `json:"repositories"`
	Count        int         `json:"count"`
	Errors       []string    `json:"errors,omitempty"`
}

// DiscoverRemotes searches forges for ctxloom repositories.
func DiscoverRemotes(ctx context.Context, cfg *config.Config, req DiscoverRemotesRequest) (*DiscoverRemotesResult, error) {
	if req.Source == "" {
		req.Source = "all"
	}
	if req.Limit <= 0 {
		req.Limit = 30
	}

	// Repository search is a GitHub-only capability — the generic git adapter
	// clones a known URL and cannot enumerate a host. "all" therefore means
	// GitHub; any other explicit source is rejected.
	switch req.Source {
	case "all", "github":
	default:
		return nil, fmt.Errorf("unsupported discovery source %q: only \"github\" is searchable", req.Source)
	}

	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)

	fetcher := req.GitHubFetcher
	if fetcher == nil {
		fetcher = remote.NewGitHubFetcher(auth.GitHub)
	}

	var allRepos []remote.RepoInfo
	var errs []string
	repos, err := fetcher.SearchRepos(ctx, req.Query, req.Limit)
	if err != nil {
		errs = append(errs, fmt.Sprintf("%s: %v", remote.ForgeGitHub, err))
	} else {
		for _, r := range repos {
			if r.Stars >= req.MinStars {
				allRepos = append(allRepos, r)
			}
		}
	}

	result := &DiscoverRemotesResult{
		Repositories: make([]RepoEntry, 0, len(allRepos)),
		Count:        len(allRepos),
		Errors:       errs,
	}

	for _, r := range allRepos {
		result.Repositories = append(result.Repositories, RepoEntry{
			Owner:       r.Owner,
			Name:        r.Name,
			Description: r.Description,
			Stars:       r.Stars,
			URL:         r.URL,
			Forge:       string(r.Forge),
			AddCommand:  fmt.Sprintf("ctxloom remote add %s %s/%s", r.Owner, r.Owner, r.Name),
		})
	}

	return result, nil
}

// BrowseRemoteRequest contains parameters for browsing a remote.
type BrowseRemoteRequest struct {
	Remote    string `json:"remote"`
	ItemType  string `json:"item_type"` // "bundle", "profile", or empty for both
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Fetcher is an optional pre-configured fetcher (for testing).
	Fetcher remote.Fetcher `json:"-"`
}

// BrowseItemEntry represents an item in a remote repository.
type BrowseItemEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir,omitempty"`
	PullRef string `json:"pull_ref"`
}

// BrowseRemoteResult contains the contents of a remote repository.
type BrowseRemoteResult struct {
	Remote   string            `json:"remote"`
	URL      string            `json:"url"`
	Items    []BrowseItemEntry `json:"items"`
	Count    int               `json:"count"`
	Warnings []string          `json:"warnings,omitempty"`
}

// BrowseRemote lists items available in a remote repository.
func BrowseRemote(ctx context.Context, cfg *config.Config, req BrowseRemoteRequest) (*BrowseRemoteResult, error) {
	if req.Remote == "" {
		return nil, fmt.Errorf("remote is required")
	}

	registry, err := resolveRegistry(cfg, req.Registry)
	if err != nil {
		return nil, err
	}

	rem, err := registry.Get(req.Remote)
	if err != nil {
		return nil, err
	}

	fetcher, owner, repo, err := resolveBrowseFetcher(cfg, rem, req.Fetcher)
	if err != nil {
		return nil, err
	}

	var items []BrowseItemEntry
	var warnings []string
	for _, itemType := range browseTypeList(req.ItemType) {
		typeItems, warning := browseTypeItems(ctx, fetcher, owner, repo, rem.URL, itemType, req)
		items = append(items, typeItems...)
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	return &BrowseRemoteResult{
		Remote:   req.Remote,
		URL:      rem.URL,
		Items:    items,
		Count:    len(items),
		Warnings: warnings,
	}, nil
}

// resolveBrowseFetcher returns the fetcher (preferring an injected one) and the
// parsed owner/repo for a remote.
func resolveBrowseFetcher(cfg *config.Config, rem *remote.Remote, injected remote.Fetcher) (remote.Fetcher, string, string, error) {
	fetcher := injected
	if fetcher == nil {
		var err error
		fetcher, err = getCachedFetcher(cfg, rem.URL)
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to create fetcher: %w", err)
		}
	}
	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid remote URL: %w", err)
	}
	return fetcher, owner, repo, nil
}

// browseTypeList maps the request item_type filter to the item types to browse.
// Only bundles are distributed at the top level now (top-level profile
// distribution was retired; profiles ship inside bundles).
func browseTypeList(itemType string) []remote.ItemType {
	return []remote.ItemType{remote.ItemTypeBundle}
}

// browseTypeItems lists one item type's entries. A genuine "not found" (the
// type's directory doesn't exist) yields no items and no warning; any other
// error yields a warning string (also echoed to stderr).
func browseTypeItems(ctx context.Context, fetcher remote.Fetcher, owner, repo, repoURL string, itemType remote.ItemType, req BrowseRemoteRequest) ([]BrowseItemEntry, string) {
	basePath := path.Join(paths.RepoContentPrefix, itemType.DirName())
	if req.Path != "" {
		basePath = path.Join(basePath, req.Path)
	}

	entries, err := browseDir(ctx, fetcher, owner, repo, basePath, "", req.Recursive)
	if err != nil {
		// Sentinel, not error text: a missing directory is not a warning.
		if errors.Is(err, errs.ErrRemoteContentNotFound) {
			return nil, ""
		}
		warning := fmt.Sprintf("failed to browse %s: %v", itemType.DirName(), err)
		clidiag.Warn("ctxloom", "%s", warning)
		return nil, warning
	}

	items := make([]BrowseItemEntry, 0, len(entries))
	for _, e := range entries {
		items = append(items, browseEntry(e, itemType, repoURL, req))
	}
	return items, ""
}

// browseEntry builds a BrowseItemEntry from a directory entry, stripping the
// ".yaml" suffix from files and prefixing req.Path onto the pull path. The
// PullRef is the canonical "<url>@<kind>s/<path>" ref — canonical is the sole
// stored identity (short "<alias>/<path>" forms are still accepted as install
// input and resolved to canonical at the boundary).
func browseEntry(e remote.DirEntry, itemType remote.ItemType, repoURL string, req BrowseRemoteRequest) BrowseItemEntry {
	name := e.Name
	if !e.IsDir && strings.HasSuffix(name, ".yaml") {
		name = strings.TrimSuffix(name, ".yaml")
	}

	pullPath := name
	if req.Path != "" {
		pullPath = req.Path + "/" + name
	}

	return BrowseItemEntry{
		Name:    name,
		Type:    string(itemType),
		Path:    pullPath,
		IsDir:   e.IsDir,
		PullRef: (&remote.Reference{URL: repoURL, ItemType: itemType, Path: pullPath}).CanonicalString(),
	}
}

// browseDir lists directory contents, optionally recursively.
func browseDir(ctx context.Context, fetcher remote.Fetcher, owner, repo, dir, ref string, recursive bool) ([]remote.DirEntry, error) {
	entries, err := fetcher.ListDir(ctx, owner, repo, dir, ref)
	if err != nil {
		return nil, err
	}

	if !recursive {
		return entries, nil
	}

	var results []remote.DirEntry
	for _, entry := range entries {
		if entry.IsDir {
			// dir is a logical, forward-slash repo path consumed by go-git
			// ListDir — build with path.Join, not filepath.Join (which would
			// emit backslashes on Windows and break tree navigation).
			fullPath := path.Join(dir, entry.Name)
			subEntries, err := browseDir(ctx, fetcher, owner, repo, fullPath, ref, true)
			if err != nil {
				continue // Continue on error for subdirectories
			}
			// Prefix subentries with directory name
			for _, sub := range subEntries {
				sub.Name = entry.Name + "/" + sub.Name
				results = append(results, sub)
			}
		} else if strings.HasSuffix(entry.Name, ".yaml") {
			results = append(results, entry)
		}
	}

	return results, nil
}

// EnsureRemoteClonesResult reports the outcome of cloning every configured
// remote.
type EnsureRemoteClonesResult struct {
	Cloned   []string `json:"cloned"`             // remote names whose clone is now present
	Failed   []string `json:"failed,omitempty"`   // remote names that could not be cloned
	Warnings []string `json:"warnings,omitempty"` // human-readable per-remote failures
}

// EnsureRemoteClones clones every configured remote into the local repo cache
// so discovery (search_remotes / browse) can read them offline. It is the
// eager-clone step run at init time.
//
// Fault tolerance (CLAUDE.md): a per-remote clone failure is warned and
// recorded, never fatal — whatever clones cleanly is still usable. Returns a
// non-nil result even when some (or all) remotes fail.
func EnsureRemoteClones(ctx context.Context, cfg *config.Config) (*EnsureRemoteClonesResult, error) {
	registry, err := getRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load registry: %w", err)
	}

	cache := newRepoCache(cfg)
	result := &EnsureRemoteClonesResult{}

	for _, rem := range registry.List() {
		if msg := ensureClone(ctx, cache, rem); msg != "" {
			result.Failed = append(result.Failed, rem.Name)
			result.Warnings = append(result.Warnings, msg)
			clidiag.Warn("ctxloom", "%s", msg)
			continue
		}
		result.Cloned = append(result.Cloned, rem.Name)
	}

	return result, nil
}

// repoCloner clones a remote repository into the local cache with full history.
// *remote.RepoCache satisfies it; tests inject a fake to avoid real git/network.
type repoCloner interface {
	EnsureFullRepo(ctx context.Context, repoURL string, forgeType remote.ForgeType) (string, error)
}

// ensureClone clones rem into the local repo cache with full history (so any
// pinned SHA is readable), returning a human-readable warning string on failure
// (empty on success). Shared by the eager clone at `remote add` (AddRemote) and
// the all-remotes clone at init (EnsureRemoteClones).
func ensureClone(ctx context.Context, cache repoCloner, rem *remote.Remote) string {
	forgeType, _, derr := remote.DetectForge(rem.URL)
	if derr != nil {
		return fmt.Sprintf("%s: detect forge: %v", rem.Name, derr)
	}
	if _, cerr := cache.EnsureFullRepo(ctx, rem.URL, forgeType); cerr != nil {
		return fmt.Sprintf("%s: clone failed: %v", rem.Name, cerr)
	}
	return ""
}

// appendWarning joins two warning strings with "; ", treating an empty existing
// warning as "no warning yet".
func appendWarning(existing, msg string) string {
	if existing == "" {
		return msg
	}
	return existing + "; " + msg
}

// SearchRemotesRequest contains parameters for searching across all remotes.
type SearchRemotesRequest struct {
	Query    string `json:"query"`
	ItemType string `json:"item_type"` // "bundle", "profile", or empty for both

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
}

// SearchRemoteEntry represents a search result from a remote.
type SearchRemoteEntry struct {
	Remote      string   `json:"remote"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	PullRef     string   `json:"pull_ref"`
}

// SearchRemotesResult contains search results from all remotes.
type SearchRemotesResult struct {
	Results  []SearchRemoteEntry `json:"results"`
	Count    int                 `json:"count"`
	Query    string              `json:"query"`
	Warnings []string            `json:"warnings,omitempty"`
}

// resolveRegistry returns the injected registry, or loads one from cfg. Shared
// by the remote-listing operations (browse/search) that take no FS override.
func resolveRegistry(cfg *config.Config, injected *remote.Registry) (*remote.Registry, error) {
	if injected != nil {
		return injected, nil
	}
	registry, err := getRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load registry: %w", err)
	}
	return registry, nil
}

// SearchRemotes searches for bundles and profiles across all configured remotes.
func SearchRemotes(ctx context.Context, cfg *config.Config, req SearchRemotesRequest) (*SearchRemotesResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	registry, err := resolveRegistry(cfg, req.Registry)
	if err != nil {
		return nil, err
	}

	remotes := registry.List()
	if len(remotes) == 0 {
		return &SearchRemotesResult{
			Results:  []SearchRemoteEntry{},
			Count:    0,
			Query:    req.Query,
			Warnings: []string{"no remotes configured"},
		}, nil
	}

	types := searchTypeList(req.ItemType)
	query := remote.ParseSearchQuery(req.Query)

	prewarmRemoteClones(ctx, cfg, remotes)
	results, warnings := fanOutRemoteSearch(ctx, cfg, remotes, types, query)
	entries := toSearchEntries(results)

	return &SearchRemotesResult{
		Results:  entries,
		Count:    len(entries),
		Query:    req.Query,
		Warnings: warnings,
	}, nil
}

// searchTypeList maps the request item_type filter to the item types to search.
// Only bundles are distributed at the top level now (top-level profile
// distribution was retired); "fragment" also searches bundles (fragments live
// inside bundles).
func searchTypeList(itemType string) []remote.ItemType {
	return []remote.ItemType{remote.ItemTypeBundle}
}

// prewarmRemoteClones ensures each remote's clone exists BEFORE the parallel
// search fans out. The per-(remote × type) goroutines all read the same clone;
// on a cold cache two would race to clone the same dir ("directory not empty").
// Cloning once up front (serially) makes the parallel reads pure. Fault-
// tolerant: a clone failure is left to surface as a per-search warning.
func prewarmRemoteClones(ctx context.Context, cfg *config.Config, remotes []*remote.Remote) {
	cache := newRepoCache(cfg)
	for _, rem := range remotes {
		if forgeType, _, derr := remote.DetectForge(rem.URL); derr == nil {
			_, _ = cache.EnsureRepo(ctx, rem.URL, forgeType)
		}
	}
}

// fanOutRemoteSearch searches every (remote × type) pair concurrently,
// returning the merged results and per-search warnings.
func fanOutRemoteSearch(ctx context.Context, cfg *config.Config, remotes []*remote.Remote, types []remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, []string) {
	var wg sync.WaitGroup
	resultsCh := make(chan []remote.SearchResult, len(remotes)*len(types))
	warningsCh := make(chan string, len(remotes)*len(types))

	for _, rem := range remotes {
		for _, itemType := range types {
			wg.Add(1)
			go func(r *remote.Remote, t remote.ItemType) {
				defer wg.Done()

				results, err := searchSingleRemote(ctx, cfg, r, t, query)
				if err != nil {
					warningsCh <- fmt.Sprintf("%s (%s): %v", r.Name, t, err)
					return
				}
				resultsCh <- results
			}(rem, itemType)
		}
	}

	wg.Wait()
	close(resultsCh)
	close(warningsCh)

	var allResults []remote.SearchResult
	for results := range resultsCh {
		allResults = append(allResults, results...)
	}
	var warnings []string
	for w := range warningsCh {
		warnings = append(warnings, w)
	}
	return allResults, warnings
}

// toSearchEntries converts internal search results to the response format.
func toSearchEntries(results []remote.SearchResult) []SearchRemoteEntry {
	entries := make([]SearchRemoteEntry, 0, len(results))
	for _, r := range results {
		entries = append(entries, SearchRemoteEntry{
			Remote:      r.Remote,
			Name:        r.Entry.Name,
			Type:        "bundle",
			Tags:        r.Entry.Tags,
			Description: r.Entry.Description,
			Author:      r.Entry.Author,
			// Canonicalize through Reference so all sites share one format
			// (consistent with browseEntry) rather than the ad-hoc "%ss" pluralize.
			PullRef: (&remote.Reference{URL: r.RemoteURL, ItemType: r.ItemType, Path: r.Entry.Name}).CanonicalString(),
		})
	}
	return entries
}

// searchSingleRemote searches a single remote for matching items.
func searchSingleRemote(ctx context.Context, cfg *config.Config, rem *remote.Remote, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	fetcher, err := getCachedFetcher(cfg, rem.URL)
	if err != nil {
		return nil, err
	}

	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return nil, err
	}

	// Get default branch
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	// Try to fetch manifest first (faster)
	manifestPath := paths.RepoContentPrefix + "/manifest.yaml"
	manifestContent, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err == nil {
		return searchManifestContent(rem, manifestContent, itemType, query)
	}

	// Fall back to directory listing
	return searchDirectoryContent(ctx, fetcher, rem, owner, repo, branch, itemType, query)
}

// searchManifestContent searches the manifest for matching items.
func searchManifestContent(rem *remote.Remote, content []byte, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	var manifest remote.Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return nil, err
	}

	var entries []remote.ManifestEntry
	switch itemType {
	case remote.ItemTypeBundle:
		entries = manifest.Bundles
	}

	var results []remote.SearchResult
	for _, entry := range entries {
		if remote.MatchesQuery(entry, query) {
			results = append(results, remote.SearchResult{
				Remote:    rem.Name,
				Entry:     entry,
				RemoteURL: rem.URL,
				ItemType:  itemType,
			})
		}
	}

	return results, nil
}

// searchDirectoryContent searches by listing directory contents.
func searchDirectoryContent(ctx context.Context, fetcher remote.Fetcher, rem *remote.Remote, owner, repo, branch string, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	dirPath := path.Join(paths.RepoContentPrefix, itemType.DirName())

	entries, err := fetcher.ListDir(ctx, owner, repo, dirPath, branch)
	if err != nil {
		return nil, err
	}

	var results []remote.SearchResult
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}

		name := strings.TrimSuffix(entry.Name, ".yaml")

		// Build the manifest entry from the file's own metadata so tag: and
		// description searches work without a manifest.yaml. Reads come from
		// the local clone, so this is cheap. If the file can't be read or
		// parsed, fall back to a name-only entry (still text-matchable).
		manifestEntry := remote.ManifestEntry{Name: name}
		filePath := path.Join(dirPath, entry.Name)
		if content, ferr := fetcher.FetchFile(ctx, owner, repo, filePath, branch); ferr == nil {
			var meta struct {
				Tags        []string `yaml:"tags"`
				Author      string   `yaml:"author"`
				Description string   `yaml:"description"`
				Version     string   `yaml:"version"`
			}
			if yaml.Unmarshal(content, &meta) == nil {
				manifestEntry.Tags = meta.Tags
				manifestEntry.Author = meta.Author
				manifestEntry.Description = meta.Description
				manifestEntry.Version = meta.Version
			}
		}

		if remote.MatchesQuery(manifestEntry, query) {
			results = append(results, remote.SearchResult{
				Remote:    rem.Name,
				Entry:     manifestEntry,
				RemoteURL: rem.URL,
				ItemType:  itemType,
			})
		}
	}

	return results, nil
}
