package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/afero"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/remote"
)

// getBaseDir returns the SCM directory from config, defaulting to ".scm".
func getBaseDir(cfg *config.Config) string {
	if cfg != nil && len(cfg.SCMPaths) > 0 {
		return cfg.SCMPaths[0]
	}
	return ".scm"
}

// getRegistry creates a registry using the config's SCM path.
func getRegistry(cfg *config.Config, opts ...remote.RegistryOption) (*remote.Registry, error) {
	baseDir := getBaseDir(cfg)
	return remote.NewRegistry(filepath.Join(baseDir, "remotes.yaml"), opts...)
}

// RemoteEntry represents a remote in operation results.
type RemoteEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version string `json:"version"`
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
}

// ListRemotes returns all configured remotes.
func ListRemotes(ctx context.Context, cfg *config.Config, req ListRemotesRequest) (*ListRemotesResult, error) {
	registry := req.Registry
	if registry == nil {
		fs := req.FS
		if fs == nil {
			fs = afero.NewOsFs()
		}
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to load registry: %w", err)
		}
	}

	remotes := registry.List()

	result := &ListRemotesResult{
		Remotes: make([]RemoteEntry, 0, len(remotes)),
		Count:   len(remotes),
	}

	for _, r := range remotes {
		result.Remotes = append(result.Remotes, RemoteEntry{
			Name:    r.Name,
			URL:     r.URL,
			Version: r.Version,
		})
	}

	return result, nil
}

// AddRemoteRequest contains parameters for adding a remote.
type AddRemoteRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`

	// Registry is an optional pre-configured registry (for testing).
	Registry *remote.Registry `json:"-"`
	// Fetcher is an optional pre-configured fetcher (for testing).
	Fetcher remote.Fetcher `json:"-"`
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
		fs := req.FS
		if fs == nil {
			fs = afero.NewOsFs()
		}
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	if err := registry.Add(req.Name, req.URL); err != nil {
		return nil, err
	}

	// Verify the remote
	fetcher := req.Fetcher
	if fetcher == nil {
		baseDir := getBaseDir(cfg)
		auth := remote.LoadAuth(baseDir)
		var err error
		fetcher, err = registry.GetFetcher(req.Name, auth)
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

	valid, _ := fetcher.ValidateRepo(ctx, owner, repo)

	rem, _ := registry.Get(req.Name)

	result := &AddRemoteResult{
		Status: "added",
		Name:   req.Name,
		URL:    rem.URL,
	}
	if !valid {
		result.Warning = "repository does not have scm/v1/ directory structure"
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
		fs := req.FS
		if fs == nil {
			fs = afero.NewOsFs()
		}
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
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

// DiscoverRemotesRequest contains parameters for discovering remote repositories.
type DiscoverRemotesRequest struct {
	Query    string `json:"query"`
	Source   string `json:"source"`   // "github", "gitlab", or "all"
	MinStars int    `json:"min_stars"`
	Limit    int    `json:"limit"`

	// GitHubFetcher is an optional pre-configured GitHub fetcher (for testing).
	GitHubFetcher remote.Fetcher `json:"-"`
	// GitLabFetcher is an optional pre-configured GitLab fetcher (for testing).
	GitLabFetcher remote.Fetcher `json:"-"`
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

// DiscoverRemotes searches forges for SCM repositories.
func DiscoverRemotes(ctx context.Context, cfg *config.Config, req DiscoverRemotesRequest) (*DiscoverRemotesResult, error) {
	if req.Source == "" {
		req.Source = "all"
	}
	if req.Limit <= 0 {
		req.Limit = 30
	}

	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)

	var forges []remote.ForgeType
	switch req.Source {
	case "github":
		forges = []remote.ForgeType{remote.ForgeGitHub}
	case "gitlab":
		forges = []remote.ForgeType{remote.ForgeGitLab}
	default:
		forges = []remote.ForgeType{remote.ForgeGitHub, remote.ForgeGitLab}
	}

	var wg sync.WaitGroup
	resultsCh := make(chan []remote.RepoInfo, len(forges))
	errorsCh := make(chan error, len(forges))

	for _, forge := range forges {
		wg.Add(1)
		go func(f remote.ForgeType) {
			defer wg.Done()

			var fetcher remote.Fetcher
			var err error

			switch f {
			case remote.ForgeGitHub:
				if req.GitHubFetcher != nil {
					fetcher = req.GitHubFetcher
				} else {
					fetcher = remote.NewGitHubFetcher(auth.GitHub)
				}
			case remote.ForgeGitLab:
				if req.GitLabFetcher != nil {
					fetcher = req.GitLabFetcher
				} else {
					fetcher, err = remote.NewGitLabFetcher("", auth.GitLab)
					if err != nil {
						errorsCh <- fmt.Errorf("GitLab: %w", err)
						return
					}
				}
			}

			repos, err := fetcher.SearchRepos(ctx, req.Query, req.Limit)
			if err != nil {
				errorsCh <- fmt.Errorf("%s: %w", f, err)
				return
			}

			filtered := repos[:0]
			for _, r := range repos {
				if r.Stars >= req.MinStars {
					filtered = append(filtered, r)
				}
			}

			resultsCh <- filtered
		}(forge)
	}

	wg.Wait()
	close(resultsCh)
	close(errorsCh)

	var allRepos []remote.RepoInfo
	for repos := range resultsCh {
		allRepos = append(allRepos, repos...)
	}

	var errors []string
	for err := range errorsCh {
		errors = append(errors, err.Error())
	}

	result := &DiscoverRemotesResult{
		Repositories: make([]RepoEntry, 0, len(allRepos)),
		Count:        len(allRepos),
		Errors:       errors,
	}

	for _, r := range allRepos {
		result.Repositories = append(result.Repositories, RepoEntry{
			Owner:       r.Owner,
			Name:        r.Name,
			Description: r.Description,
			Stars:       r.Stars,
			URL:         r.URL,
			Forge:       string(r.Forge),
			AddCommand:  fmt.Sprintf("scm remote add %s %s/%s", r.Owner, r.Owner, r.Name),
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
	Remote string            `json:"remote"`
	URL    string            `json:"url"`
	Items  []BrowseItemEntry `json:"items"`
	Count  int               `json:"count"`
}

// BrowseRemote lists items available in a remote repository.
func BrowseRemote(ctx context.Context, cfg *config.Config, req BrowseRemoteRequest) (*BrowseRemoteResult, error) {
	if req.Remote == "" {
		return nil, fmt.Errorf("remote is required")
	}

	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to load registry: %w", err)
		}
	}

	rem, err := registry.Get(req.Remote)
	if err != nil {
		return nil, err
	}

	fetcher := req.Fetcher
	if fetcher == nil {
		baseDir := getBaseDir(cfg)
		auth := remote.LoadAuth(baseDir)
		fetcher, err = remote.NewFetcher(rem.URL, auth)
		if err != nil {
			return nil, fmt.Errorf("failed to create fetcher: %w", err)
		}
	}

	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	// Determine which types to list
	var itemTypes []remote.ItemType
	switch req.ItemType {
	case "bundle":
		itemTypes = []remote.ItemType{remote.ItemTypeBundle}
	case "profile":
		itemTypes = []remote.ItemType{remote.ItemTypeProfile}
	default:
		itemTypes = []remote.ItemType{remote.ItemTypeBundle, remote.ItemTypeProfile}
	}

	var items []BrowseItemEntry

	for _, itemType := range itemTypes {
		basePath := fmt.Sprintf("scm/%s/%s", rem.Version, itemType.DirName())
		if req.Path != "" {
			basePath = filepath.Join(basePath, req.Path)
		}

		entries, err := browseDir(ctx, fetcher, owner, repo, basePath, "", req.Recursive)
		if err != nil {
			continue // Directory might not exist for this type
		}

		for _, e := range entries {
			name := e.Name
			if !e.IsDir && strings.HasSuffix(name, ".yaml") {
				name = strings.TrimSuffix(name, ".yaml")
			}

			pullPath := name
			if req.Path != "" {
				pullPath = req.Path + "/" + name
			}

			items = append(items, BrowseItemEntry{
				Name:    name,
				Type:    string(itemType),
				Path:    pullPath,
				IsDir:   e.IsDir,
				PullRef: fmt.Sprintf("%s/%s", req.Remote, pullPath),
			})
		}
	}

	return &BrowseRemoteResult{
		Remote: req.Remote,
		URL:    rem.URL,
		Items:  items,
		Count:  len(items),
	}, nil
}

// browseDir lists directory contents, optionally recursively.
func browseDir(ctx context.Context, fetcher remote.Fetcher, owner, repo, path, ref string, recursive bool) ([]remote.DirEntry, error) {
	entries, err := fetcher.ListDir(ctx, owner, repo, path, ref)
	if err != nil {
		return nil, err
	}

	if !recursive {
		return entries, nil
	}

	var results []remote.DirEntry
	for _, entry := range entries {
		if entry.IsDir {
			fullPath := filepath.Join(path, entry.Name)
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
