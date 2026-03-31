package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// Puller interface for pulling remote items (allows mocking in tests).
type Puller interface {
	Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error)
}

// LockDependenciesRequest contains parameters for generating a lockfile.
type LockDependenciesRequest struct {
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
func LockDependencies(ctx context.Context, cfg *config.Config, req LockDependenciesRequest) (*LockDependenciesResult, error) {
	// Use provided filesystem or default to OS
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	baseDir := getBaseDir(cfg)

	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Version:  1,
		Bundles:  make(map[string]remote.LockEntry),
		Profiles: make(map[string]remote.LockEntry),
	}

	itemCount := 0

	// Scan for installed items (bundles and profiles only)
	for _, itemType := range []remote.ItemType{
		remote.ItemTypeBundle,
		remote.ItemTypeProfile,
	} {
		var dirName string
		switch itemType {
		case remote.ItemTypeBundle:
			dirName = "bundles"
		case remote.ItemTypeProfile:
			dirName = "profiles"
		}

		itemDir := filepath.Join(baseDir, dirName)
		entries, err := afero.ReadDir(fs, itemDir)
		if err != nil {
			continue
		}

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

				relPath, _ := filepath.Rel(filepath.Join(itemDir, remoteName), file)
				name := strings.TrimSuffix(relPath, ".yaml")
				ref := fmt.Sprintf("%s/%s", remoteName, name)

				lockEntry := remote.LockEntry{
					SHA:        meta.Source.SHA,
					URL:        meta.Source.URL,
					CtxloomVersion: meta.Source.Version,
					FetchedAt:  meta.Source.FetchedAt,
				}

				lockfile.AddEntry(itemType, ref, lockEntry)
				itemCount++
			}
		}
	}

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

// InstallDependenciesRequest contains parameters for installing from lockfile.
type InstallDependenciesRequest struct {
	Force bool `json:"force"`

	// Testing injection points
	FS          afero.Fs                 `json:"-"` // Optional filesystem for testing
	LockManager *remote.LockfileManager  `json:"-"` // Optional lock manager for testing
	Registry    *remote.Registry         `json:"-"` // Optional registry for testing
	Puller      Puller                   `json:"-"` // Optional puller for testing
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

	// Use injected filesystem or default
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

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
		registry, err = remote.NewRegistry(filepath.Join(baseDir, "remotes.yaml"), remote.WithRegistryFS(fs))
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
		ref := fmt.Sprintf("%s@%s", e.Ref, e.Entry.SHA[:7])

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

// CheckOutdatedRequest contains parameters for checking outdated items.
type CheckOutdatedRequest struct {
	// Testing injection points
	FS             afero.Fs                `json:"-"` // Optional filesystem for testing
	LockManager    *remote.LockfileManager `json:"-"` // Optional lock manager for testing
	Registry       *remote.Registry        `json:"-"` // Optional registry for testing
	FetcherFactory FetcherFactory          `json:"-"` // Optional fetcher factory for testing
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
	baseDir := getBaseDir(cfg)

	// Use injected filesystem or default
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

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
		return &CheckOutdatedResult{
			Status:  "empty",
			Message: "No entries in lockfile",
		}, nil
	}

	// Use injected registry or create new one
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = remote.NewRegistry(filepath.Join(baseDir, "remotes.yaml"), remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	auth := remote.LoadAuth("")
	entries := lockfile.AllEntries()

	// Use injected fetcher factory or default
	fetcherFactory := req.FetcherFactory
	if fetcherFactory == nil {
		fetcherFactory = func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
			return remote.NewFetcher(url, auth)
		}
	}

	var outdated []OutdatedItem

	for _, e := range entries {
		ref, err := remote.ParseReference(e.Ref)
		if err != nil {
			continue
		}

		rem, err := registry.Get(ref.Remote)
		if err != nil {
			continue
		}

		fetcher, err := fetcherFactory(rem.URL, auth)
		if err != nil {
			continue
		}

		owner, repo, err := remote.ParseRepoURL(rem.URL)
		if err != nil {
			continue
		}

		branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			continue
		}

		latestSHA, err := fetcher.ResolveRef(ctx, owner, repo, branch)
		if err != nil {
			continue
		}

		if latestSHA != e.Entry.SHA {
			lockedShort := e.Entry.SHA
			if len(lockedShort) > 7 {
				lockedShort = lockedShort[:7]
			}
			latestShort := latestSHA
			if len(latestShort) > 7 {
				latestShort = latestShort[:7]
			}

			outdated = append(outdated, OutdatedItem{
				Type:      string(e.Type),
				Reference: e.Ref,
				LockedSHA: lockedShort,
				LatestSHA: latestShort,
			})
		}
	}

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
