package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/collections"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
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

	// Changes lists the bundle-level delta between active and pending
	// lockfiles after this sync. Non-empty only when a bundle was added
	// or its SHA changed AND its source remote is not TrustBundles=true.
	// Consumed by the MCP review state in ctxServer (see
	// docs/bundle-review-plan.md Phase 3). nil for non-bundle syncs.
	Changes *BundleChangeSet `json:"-"`
}

// SyncDependencies syncs remote bundles and profiles referenced in config.
// This is the main entry point for auto-fetch on startup.
func SyncDependencies(ctx context.Context, cfg *config.Config, req SyncDependenciesRequest) (*SyncDependenciesResult, error) {
	fs := getFS(req.FS)
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

	registry, puller, err := resolveSyncDeps(cfg, req, baseDir, fs)
	if err != nil {
		return nil, err
	}

	result := &SyncDependenciesResult{
		Status: "completed",
	}

	// Sync profiles first (they may reference bundles), then bundles.
	if err := syncRefs(ctx, cfg, puller, registry, profileRefs, remote.ItemTypeProfile, baseDir, req.Force, fs, result); err != nil {
		return result, err
	}
	if err := syncRefs(ctx, cfg, puller, registry, bundleRefs, remote.ItemTypeBundle, baseDir, req.Force, fs, result); err != nil {
		return result, err
	}

	runSyncPostSteps(ctx, cfg, req, result, fs)

	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}

	applyTrustedPromotions(cfg, registry, fs, result)

	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d updated, %d skipped, %d failed",
		result.Total, result.Installed, result.Updated, len(result.Skipped), result.Errors)

	return result, nil
}

// resolveSyncDeps returns the registry and puller for a sync, preferring
// injected (test) instances. The puller redirects bundle writes to the
// *pending* lockfile so the active lock.yaml stays at the old SHA until the
// user approves the review (docs/bundle-review-plan.md Phase 2.3).
func resolveSyncDeps(cfg *config.Config, req SyncDependenciesRequest, baseDir string, fs afero.Fs) (*remote.Registry, Puller, error) {
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	puller := req.Puller
	if puller == nil {
		auth := remote.LoadAuth(baseDir)
		pendingMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs), remote.WithPendingLockfile())
		puller = remote.NewPuller(registry, auth,
			remote.WithFetcherFactory(newCachedFetcherFactory(cfg)),
			remote.WithLockfileManager(remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))),
			remote.WithBundleLockfileTarget(pendingMgr),
		)
	}
	return registry, puller, nil
}

// syncRefs syncs each ref of one item type into result, checking for context
// cancellation between items (returns ctx.Err() to abort the whole sync).
func syncRefs(ctx context.Context, cfg *config.Config, puller Puller, registry *remote.Registry, refs []string, itemType remote.ItemType, baseDir string, force bool, fs afero.Fs, result *SyncDependenciesResult) error {
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item := syncItem(ctx, cfg, puller, registry, ref, itemType, baseDir, force, fs)
		result.Total++
		addSyncItem(result, item)
	}
	return nil
}

// runSyncPostSteps runs the optional lockfile + hooks regeneration after a sync.
// Each step warns and continues on failure — partial success is success and a
// post-step failure must not fail the sync the user just completed (CLAUDE.md).
func runSyncPostSteps(ctx context.Context, cfg *config.Config, req SyncDependenciesRequest, result *SyncDependenciesResult, fs afero.Fs) {
	if req.Lock && result.Installed+result.Updated > 0 {
		if _, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FS: fs}); err != nil {
			zap.L().Warn("failed to generate lockfile", zap.Error(err))
		}
	}

	// Apply hooks whenever there were remote references, so MCP servers from
	// bundles get registered even if every dependency was already installed.
	if req.ApplyHooks && result.Total > 0 {
		if _, err := ApplyHooks(ctx, cfg, ApplyHooksRequest{
			Backend:           "all",
			RegenerateContext: true,
		}); err != nil {
			zap.L().Warn("failed to apply hooks", zap.Error(err))
		}
	}
}

// applyTrustedPromotions lifts trusted remotes' freshly-pulled entries out of
// pending and into active (bypassing the review gate), then records the
// active↔pending bundle delta on result. Fault-tolerant — a promotion failure
// just leaves those entries pending. result.Changes is empty when nothing
// landed in pending (only profiles synced, or all bundles were promoted).
func applyTrustedPromotions(cfg *config.Config, registry *remote.Registry, fs afero.Fs, result *SyncDependenciesResult) {
	if promoted, err := PromoteTrustedPendingBundles(cfg, registry, fs); err != nil {
		zap.L().Warn("failed to promote trusted pending bundles", zap.Error(err))
	} else if len(promoted) > 0 {
		zap.L().Info("auto-applied trusted bundles", zap.Strings("bundles", promoted))
	}

	result.Changes = computeBundleChanges(cfg, registry, fs)
}

// computeBundleChanges loads the active + pending lockfiles and diffs them.
// nil result means "nothing to review" — either no pending file exists or
// every change was filtered out by TrustBundles.
func computeBundleChanges(cfg *config.Config, registry *remote.Registry, fs afero.Fs) *BundleChangeSet {
	baseDir := getBaseDir(cfg)
	activeMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs), remote.WithPendingLockfile())

	active, err := activeMgr.Load()
	if err != nil {
		return nil
	}
	pending, err := pendingMgr.Load()
	if err != nil || pending.IsEmpty() {
		return nil
	}
	cs := DiffLockfiles(active, pending, registry)
	if cs.IsEmpty() {
		return nil
	}
	return cs
}

// PendingBundleChanges returns the active↔pending diff for cfg using the
// real OS filesystem. Used by the MCP startup path so review state gets
// populated even when no fresh sync ran this session (e.g. the user
// restarted while pending was still on disk from a previous run). nil
// when there's nothing to review.
func PendingBundleChanges(cfg *config.Config) *BundleChangeSet {
	if cfg == nil {
		return nil
	}
	registry, err := getRegistry(cfg)
	if err != nil {
		registry = nil // diff degrades to "no trust filter" rather than aborting
	}
	fs := afero.NewOsFs()
	// A pending file carried over from a previous session may strand
	// trusted bundles that were never promoted. Lift them into active here
	// too, so the startup reconcile path matches a fresh sync.
	if _, err := PromoteTrustedPendingBundles(cfg, registry, fs); err != nil {
		zap.L().Warn("failed to promote trusted pending bundles", zap.Error(err))
	}
	return computeBundleChanges(cfg, registry, fs)
}

// collectRemoteReferences collects all remote bundle and profile references from config.
// This recursively follows local parent profiles to find remote dependencies anywhere
// in the inheritance chain.
func collectRemoteReferences(cfg *config.Config, profileNames []string, fs afero.Fs) (bundleRefs []string, profileRefs []string, err error) {
	bundleSet := collections.NewSet[string]()
	profileSet := collections.NewSet[string]()

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
	seen := collections.NewSet[string]()
	var uniqueProfiles []string
	for _, name := range profilesToProcess {
		if !seen.Has(name) {
			seen.Add(name)
			uniqueProfiles = append(uniqueProfiles, name)
		}
	}

	// Collect references from each profile, recursively following local parents
	visited := collections.NewSet[string]()
	for _, profileName := range uniqueProfiles {
		collectProfileReferencesRecursive(cfg, profileName, bundleSet, profileSet, visited)
	}

	// Convert sets to slices
	bundleRefs = bundleSet.Items()
	profileRefs = profileSet.Items()

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

// collectProfileReferencesRecursive recursively collects remote bundle and profile
// references from a profile and all its local parent profiles.
// This ensures remote dependencies in nested local profiles are discovered.
func collectProfileReferencesRecursive(cfg *config.Config, profileName string, bundleSet, profileSet, visited collections.Set[string]) {
	// Prevent infinite loops
	if visited.Has(profileName) {
		return
	}
	visited.Add(profileName)

	bundles, parents := collectProfileReferences(cfg, profileName)

	// Add remote bundles
	for _, b := range bundles {
		if isRemoteReference(b) {
			bundleSet.Add(b)
		}
	}

	// Process parents
	for _, parent := range parents {
		if isRemoteReference(parent) {
			// Remote parent - add to profile refs for syncing
			profileSet.Add(parent)
		} else {
			// Local parent - recursively collect its references
			// Strip "profile:" prefix if present (used to distinguish profile refs from bundle refs)
			localName := strings.TrimPrefix(parent, "profile:")
			collectProfileReferencesRecursive(cfg, localName, bundleSet, profileSet, visited)
		}
	}
}

// isRemoteReference checks if a reference points to a remote source.
func isRemoteReference(ref string) bool {
	// URL-based references are always remote
	if strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://") {
		return true
	}

	// "profile:" prefix indicates a local profile reference (not remote)
	// e.g., "profile:personal/typescript-developer" is local
	if strings.HasPrefix(ref, "profile:") {
		return false
	}

	// Simple format: remote/path (e.g., "github/bundle-name")
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

	// Pull the item. Sync stages bundle changes into the *pending* lockfile
	// for the bundle-review gate (it does not activate them), so the pull must
	// be Blind: there is no interactive confirmation to give — the review IS
	// the confirmation — and any content display would both be premature and
	// corrupt a non-interactive caller's stream (e.g. the MCP server's stdout).
	opts := remote.PullOptions{
		LocalDir: baseDir,
		Force:    force,
		Blind:    true,
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
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	var missing []MissingDependency
	seen := collections.NewSet[string]()

	for _, profileName := range resolveProfilesToCheck(cfg, req.Profiles) {
		bundles, parentProfiles := collectProfileReferences(cfg, profileName)
		missing = append(missing, collectMissingRefs(bundles, remote.ItemTypeBundle, "bundle", profileName, baseDir, fs, seen)...)
		missing = append(missing, collectMissingRefs(parentProfiles, remote.ItemTypeProfile, "profile", profileName, baseDir, fs, seen)...)
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

// resolveProfilesToCheck returns the requested profiles, or every configured
// and directory profile when none were requested.
func resolveProfilesToCheck(cfg *config.Config, requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	var names []string
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	loader := cfg.GetProfileLoader()
	dirProfiles, _ := loader.List()
	for _, p := range dirProfiles {
		names = append(names, p.Name)
	}
	return names
}

// collectMissingRefs returns the not-yet-installed remote refs among refs,
// skipping local refs and any ref already in seen (marking the rest seen).
func collectMissingRefs(refs []string, itemType remote.ItemType, typeName, profileName, baseDir string, fs afero.Fs, seen collections.Set[string]) []MissingDependency {
	var missing []MissingDependency
	for _, ref := range refs {
		if !isRemoteReference(ref) || seen.Has(ref) {
			continue
		}
		seen.Add(ref)
		if !isInstalled(ref, itemType, baseDir, fs) {
			missing = append(missing, MissingDependency{
				Reference: ref,
				Type:      typeName,
				Profile:   profileName,
			})
		}
	}
	return missing
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
