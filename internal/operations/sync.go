package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/remote"
)

// SyncDependenciesRequest contains parameters for syncing dependencies.
type SyncDependenciesRequest struct {
	// Profiles specifies which profiles to sync. Empty means all profiles.
	Profiles []string `json:"profiles,omitempty"`

	// Force pulls even if item exists locally.
	Force bool `json:"force"`

	// Lock generates/updates the lockfile after sync.
	Lock bool `json:"lock"`

	// ApplyHooks applies hooks after sync.
	ApplyHooks bool `json:"apply_hooks"`

	// Testing injection points
	FS       afero.Fs         `json:"-"`
	Registry *remote.Registry `json:"-"`
	Puller   Puller           `json:"-"`
}

// SyncItem represents an item that was synced.
type SyncItem struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
	Status    string `json:"status"` // "installed", "updated", "skipped", "failed"
	Error     string `json:"error,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

// SyncDependenciesResult contains the result of syncing dependencies.
type SyncDependenciesResult struct {
	Status    string     `json:"status"`
	Synced    []SyncItem `json:"synced,omitempty"`
	Skipped   []SyncItem `json:"skipped,omitempty"`
	Failed    []SyncItem `json:"failed,omitempty"`
	Total     int        `json:"total"`
	Installed int        `json:"installed"`
	Updated   int        `json:"updated"`
	Errors    int        `json:"errors"`
	Message   string     `json:"message,omitempty"`
}

// SyncDependencies syncs remote bundles and profiles referenced in config.
// This is the main entry point for auto-fetch on startup.
func SyncDependencies(ctx context.Context, cfg *config.Config, req SyncDependenciesRequest) (*SyncDependenciesResult, error) {
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	baseDir := getBaseDir(cfg)

	// Collect all remote bundle references from profiles
	bundleRefs, profileRefs, err := collectRemoteReferences(cfg, req.Profiles, fs)
	if err != nil {
		return nil, fmt.Errorf("failed to collect references: %w", err)
	}

	if len(bundleRefs) == 0 && len(profileRefs) == 0 {
		return &SyncDependenciesResult{
			Status:  "empty",
			Message: "No remote references found in profiles",
		}, nil
	}

	// Initialize registry
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	// Initialize puller
	puller := req.Puller
	if puller == nil {
		auth := remote.LoadAuth(baseDir)
		puller = remote.NewPuller(registry, auth)
	}

	result := &SyncDependenciesResult{
		Status: "completed",
	}

	// Sync profiles first (they may reference bundles)
	for _, ref := range profileRefs {
		item := syncItem(ctx, cfg, puller, registry, ref, remote.ItemTypeProfile, baseDir, req.Force, fs)
		result.Total++
		addSyncItem(result, item)
	}

	// Sync bundles
	for _, ref := range bundleRefs {
		item := syncItem(ctx, cfg, puller, registry, ref, remote.ItemTypeBundle, baseDir, req.Force, fs)
		result.Total++
		addSyncItem(result, item)
	}

	// Generate lockfile if requested
	if req.Lock && result.Installed+result.Updated > 0 {
		_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FS: fs})
		if err != nil {
			zap.L().Warn("failed to generate lockfile", zap.Error(err))
		}
	}

	// Always apply hooks if requested and there were any remote references
	// This ensures MCP servers from bundles are registered even if deps were already installed
	if req.ApplyHooks && result.Total > 0 {
		_, err := ApplyHooks(ctx, cfg, ApplyHooksRequest{
			Backend:           "all",
			RegenerateContext: true,
		})
		if err != nil {
			zap.L().Warn("failed to apply hooks", zap.Error(err))
		}
	}

	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}

	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d updated, %d skipped, %d failed",
		result.Total, result.Installed, result.Updated, len(result.Skipped), result.Errors)

	return result, nil
}

// collectRemoteReferences collects all remote bundle and profile references from config.
func collectRemoteReferences(cfg *config.Config, profileNames []string, fs afero.Fs) (bundleRefs []string, profileRefs []string, err error) {
	bundleSet := make(map[string]bool)
	profileSet := make(map[string]bool)

	// Get profiles to process
	profilesToProcess := profileNames
	if len(profilesToProcess) == 0 {
		// Process all profiles from config
		for name := range cfg.Profiles {
			profilesToProcess = append(profilesToProcess, name)
		}

		// Also get directory-based profiles
		loader := cfg.GetProfileLoader()
		dirProfiles, _ := loader.List()
		for _, p := range dirProfiles {
			profilesToProcess = append(profilesToProcess, p.Name)
		}
	}

	// Dedupe profile names
	seen := make(map[string]bool)
	var uniqueProfiles []string
	for _, name := range profilesToProcess {
		if !seen[name] {
			seen[name] = true
			uniqueProfiles = append(uniqueProfiles, name)
		}
	}

	// Collect references from each profile
	for _, profileName := range uniqueProfiles {
		bundles, profiles := collectProfileReferences(cfg, profileName)
		for _, b := range bundles {
			if isRemoteReference(b) && !bundleSet[b] {
				bundleSet[b] = true
				bundleRefs = append(bundleRefs, b)
			}
		}
		for _, p := range profiles {
			if isRemoteReference(p) && !profileSet[p] {
				profileSet[p] = true
				profileRefs = append(profileRefs, p)
			}
		}
	}

	return bundleRefs, profileRefs, nil
}

// collectProfileReferences collects bundle and parent profile references from a profile.
func collectProfileReferences(cfg *config.Config, profileName string) (bundles []string, profiles []string) {
	// Try config-based profile first
	if profile, ok := cfg.Profiles[profileName]; ok {
		bundles = append(bundles, profile.Bundles...)
		profiles = append(profiles, profile.Parents...)
		return
	}

	// Fall back to directory-based profile
	loader := cfg.GetProfileLoader()
	profile, err := loader.Load(profileName)
	if err != nil {
		return
	}

	bundles = append(bundles, profile.Bundles...)
	profiles = append(profiles, profile.Parents...)
	return
}

// isRemoteReference checks if a reference points to a remote source.
func isRemoteReference(ref string) bool {
	// Remote references contain "/" (e.g., "github/bundle-name")
	// or are URLs (https://, git@, file://)
	if strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://") {
		return true
	}

	// Simple format: remote/path
	return strings.Contains(ref, "/")
}

// syncItem syncs a single item and returns the result.
func syncItem(ctx context.Context, cfg *config.Config, puller Puller, registry *remote.Registry, ref string, itemType remote.ItemType, baseDir string, force bool, fs afero.Fs) SyncItem {
	item := SyncItem{
		Reference: ref,
		Type:      string(itemType),
	}

	// Parse reference
	parsedRef, err := remote.ParseReference(ref)
	if err != nil {
		item.Status = "failed"
		item.Error = fmt.Sprintf("invalid reference: %v", err)
		return item
	}

	// Check if already installed (unless force)
	localPath := parsedRef.LocalPath(baseDir, itemType)
	if !force {
		if _, err := fs.Stat(localPath); err == nil {
			item.Status = "skipped"
			item.LocalPath = localPath
			return item
		}
	}

	// Pull the item
	opts := remote.PullOptions{
		LocalDir: baseDir,
		Force:    force,
		ItemType: itemType,
		Cascade:  true, // Pull referenced bundles for profiles
	}

	result, err := puller.Pull(ctx, ref, opts)
	if err != nil {
		item.Status = "failed"
		item.Error = err.Error()
		return item
	}

	item.LocalPath = result.LocalPath
	if result.Overwritten {
		item.Status = "updated"
	} else {
		item.Status = "installed"
	}

	return item
}

// addSyncItem adds an item to the appropriate result list.
func addSyncItem(result *SyncDependenciesResult, item SyncItem) {
	switch item.Status {
	case "installed":
		result.Synced = append(result.Synced, item)
		result.Installed++
	case "updated":
		result.Synced = append(result.Synced, item)
		result.Updated++
	case "skipped":
		result.Skipped = append(result.Skipped, item)
	case "failed":
		result.Failed = append(result.Failed, item)
		result.Errors++
	}
}

// CheckMissingDependenciesRequest contains parameters for checking missing deps.
type CheckMissingDependenciesRequest struct {
	Profiles []string `json:"profiles,omitempty"`
	FS       afero.Fs `json:"-"`
}

// MissingDependency represents a dependency that is not installed locally.
type MissingDependency struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
	Profile   string `json:"profile"` // Which profile references this
}

// CheckMissingDependenciesResult contains the result of checking for missing deps.
type CheckMissingDependenciesResult struct {
	Status  string              `json:"status"`
	Missing []MissingDependency `json:"missing,omitempty"`
	Count   int                 `json:"count"`
	Message string              `json:"message,omitempty"`
}

// CheckMissingDependencies checks which remote dependencies are not installed.
func CheckMissingDependencies(ctx context.Context, cfg *config.Config, req CheckMissingDependenciesRequest) (*CheckMissingDependenciesResult, error) {
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	baseDir := getBaseDir(cfg)

	// Get profiles to check
	profilesToCheck := req.Profiles
	if len(profilesToCheck) == 0 {
		// Check all profiles
		for name := range cfg.Profiles {
			profilesToCheck = append(profilesToCheck, name)
		}
		loader := cfg.GetProfileLoader()
		dirProfiles, _ := loader.List()
		for _, p := range dirProfiles {
			profilesToCheck = append(profilesToCheck, p.Name)
		}
	}

	var missing []MissingDependency
	seen := make(map[string]bool)

	for _, profileName := range profilesToCheck {
		bundles, parentProfiles := collectProfileReferences(cfg, profileName)

		// Check bundles
		for _, ref := range bundles {
			if !isRemoteReference(ref) || seen[ref] {
				continue
			}
			seen[ref] = true

			if !isInstalled(ref, remote.ItemTypeBundle, baseDir, fs) {
				missing = append(missing, MissingDependency{
					Reference: ref,
					Type:      "bundle",
					Profile:   profileName,
				})
			}
		}

		// Check parent profiles
		for _, ref := range parentProfiles {
			if !isRemoteReference(ref) || seen[ref] {
				continue
			}
			seen[ref] = true

			if !isInstalled(ref, remote.ItemTypeProfile, baseDir, fs) {
				missing = append(missing, MissingDependency{
					Reference: ref,
					Type:      "profile",
					Profile:   profileName,
				})
			}
		}
	}

	if len(missing) == 0 {
		return &CheckMissingDependenciesResult{
			Status:  "complete",
			Count:   0,
			Message: "All dependencies are installed",
		}, nil
	}

	return &CheckMissingDependenciesResult{
		Status:  "missing",
		Missing: missing,
		Count:   len(missing),
		Message: fmt.Sprintf("%d dependencies need to be installed", len(missing)),
	}, nil
}

// isInstalled checks if a reference is installed locally.
func isInstalled(ref string, itemType remote.ItemType, baseDir string, fs afero.Fs) bool {
	parsedRef, err := remote.ParseReference(ref)
	if err != nil {
		return false
	}

	localPath := parsedRef.LocalPath(baseDir, itemType)
	_, err = fs.Stat(localPath)
	return err == nil
}

// AutoSyncConfig holds configuration for auto-sync behavior.
type AutoSyncConfig struct {
	// Enabled controls whether auto-sync runs on startup.
	Enabled bool `mapstructure:"enabled" yaml:"enabled,omitempty"`

	// Lock controls whether to update lockfile after sync.
	Lock bool `mapstructure:"lock" yaml:"lock,omitempty"`

	// ApplyHooks controls whether to apply hooks after sync.
	ApplyHooks bool `mapstructure:"apply_hooks" yaml:"apply_hooks,omitempty"`
}

// DefaultAutoSyncConfig returns the default auto-sync configuration.
func DefaultAutoSyncConfig() AutoSyncConfig {
	return AutoSyncConfig{
		Enabled:    true,
		Lock:       true,
		ApplyHooks: true,
	}
}

// SyncOnStartup is a convenience function that runs sync with sensible defaults.
// This is meant to be called during MCP server initialization or CLI startup.
func SyncOnStartup(ctx context.Context, cfg *config.Config) (*SyncDependenciesResult, error) {
	// Check for missing dependencies first
	checkResult, err := CheckMissingDependencies(ctx, cfg, CheckMissingDependenciesRequest{})
	if err != nil {
		return nil, err
	}

	// If nothing is missing, return early
	if checkResult.Count == 0 {
		return &SyncDependenciesResult{
			Status:  "up_to_date",
			Message: "All dependencies are already installed",
		}, nil
	}

	// Sync missing dependencies
	return SyncDependencies(ctx, cfg, SyncDependenciesRequest{
		Force:      false, // Don't overwrite existing
		Lock:       true,  // Update lockfile
		ApplyHooks: true,  // Apply hooks
	})
}

