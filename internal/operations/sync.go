package operations

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	// BundleReader probes whether a bundle's content is retrievable at its
	// locked address (the reference-only "installed" check). Nil in production
	// (built from cfg); injected in tests.
	BundleReader remote.BundleByteSource `json:"-"`
}

// SyncItem represents an item that was synced.
type SyncItem struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
	Status    string `json:"status"` // "installed", "updated", "skipped", "staged", "failed"
	Error     string `json:"error,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

// SyncDependenciesResult contains the result of syncing dependencies.
type SyncDependenciesResult struct {
	Status    string     `json:"status"`
	Synced    []SyncItem `json:"synced,omitempty"`
	Skipped   []SyncItem `json:"skipped,omitempty"`
	Failed    []SyncItem `json:"failed,omitempty"`
	Staged    []SyncItem `json:"staged,omitempty"`
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

	// Collect all remote bundle references from profiles. Bundle profiles used
	// as parents contribute their underlying bundle; top-level remote profiles
	// were retired, so there is no separate profile-ref set.
	bundleRefs, err := collectRemoteReferences(cfg, req.Profiles, fs)
	if err != nil {
		return nil, fmt.Errorf("failed to collect references: %w", err)
	}

	if len(bundleRefs) == 0 {
		return &SyncDependenciesResult{
			Status:  "empty",
			Message: "No remote references found in profiles",
		}, nil
	}

	registry, puller, err := resolveSyncDeps(cfg, req, baseDir, fs)
	if err != nil {
		return nil, err
	}

	// Refresh each referenced clone to its live tip before pulling. A first
	// install resolves an unpinned ref to the default-branch HEAD, and the
	// cache serves an existing clone as-is (ensureClone never fetches — only an
	// explicit UpdateRepo does). A stale clone — e.g. one predating an upstream
	// layout migration (ctxloom/v1/<kind>/ → ctxloom/<kind>/) — would otherwise
	// resolve to old content or 404 a moved path. CheckOutdated/Relock refresh
	// for the same reason. Skipped when a Puller is injected (tests drive a mock
	// fetcher with no real clone to advance); per-URL failures warn and continue.
	if req.Puller == nil {
		refreshRepoCaches(ctx, newRepoCache(cfg), syncRefURLs(bundleRefs))
	}

	// Installed-probe source (reference-only model: lockfile entry + content
	// retrievable from the clone cache, never a disk check).
	bundleReader := req.BundleReader
	if bundleReader == nil {
		bundleReader = NewBundleReaderForConfig(cfg)
	}

	result := &SyncDependenciesResult{
		Status: "completed",
	}

	if err := syncRefs(ctx, puller, bundleRefs, remote.ItemTypeBundle, baseDir, req.Force, bundleReader, result); err != nil {
		return result, err
	}

	// SECURITY: surface staged first-installs loudly — they are inert (no
	// hooks/MCP/context) until the user reviews and approves them.
	if n := len(result.Staged); n > 0 {
		clidiag.Warn("ctxloom", "%d new bundle(s)/profile(s) staged pending review — run 'ctxloom bundle review'", n)
	}

	runSyncPostSteps(ctx, cfg, req, result, fs)

	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}

	recordReviewState(cfg, registry, fs, result)

	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d updated, %d skipped, %d staged for review, %d failed",
		result.Total, result.Installed, result.Updated, len(result.Skipped), len(result.Staged), result.Errors)

	return result, nil
}

// resolveSyncDeps returns the registry and puller for a sync, preferring
// injected (test) instances. Sync installs exactly the pinned set; surfacing
// an upstream change to an already-locked item is `remote upgrade`'s job
// (operations.StageUpgrade), and the post-sync lock rebuilds the active
// lockfile from the pinned closure. The one exception is a FIRST install from
// an untrusted remote: sync pulls are Blind (no security review), so syncItem
// sets StageUntrustedNew and the puller stages such items into the pending
// lockfile for `bundle review` instead of activating them.
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
		puller = remote.NewPuller(registry, auth,
			remote.WithFetcherFactory(newCachedFetcherFactory(cfg)),
			remote.WithLockfileManager(remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))),
		)
	}
	return registry, puller, nil
}

// syncRefs syncs each ref of one item type into result, checking for context
// cancellation between items (returns ctx.Err() to abort the whole sync).
func syncRefs(ctx context.Context, puller Puller, refs []string, itemType remote.ItemType, baseDir string, force bool, bundles remote.BundleByteSource, result *SyncDependenciesResult) error {
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item := syncItem(ctx, puller, ref, itemType, baseDir, force, bundles)
		result.Total++
		addSyncItem(result, item)
	}
	return nil
}

// syncLockStep and syncHooksStep are seams over the two post-sync side effects
// so tests can pin the guard conditions in runSyncPostSteps without driving the
// full lockfile/hook machinery. Production wires them to the real operations.
var (
	syncLockStep  func(context.Context, *config.Config, LockDependenciesRequest) (*LockDependenciesResult, error)
	syncHooksStep func(context.Context, *config.Config, ApplyHooksRequest) (*ApplyHooksResult, error)
)

// Bound in init (not at declaration) to avoid an initialization cycle: the real
// steps transitively reference runSyncPostSteps, which reads these vars.
func init() {
	syncLockStep = LockDependencies
	syncHooksStep = ApplyHooks
}

// runSyncPostSteps runs the optional lockfile + hooks regeneration after a sync.
// Each step warns and continues on failure — partial success is success and a
// post-step failure must not fail the sync the user just completed (CLAUDE.md).
func runSyncPostSteps(ctx context.Context, cfg *config.Config, req SyncDependenciesRequest, result *SyncDependenciesResult, fs afero.Fs) {
	if req.Lock && result.Installed+result.Updated > 0 {
		// The puller already wrote the lockfile inline during this sync, so the
		// lock step only needs to surface it — SkipSync avoids a redundant
		// second sync pass. StageUntrustedNew keeps the closure rebuild from
		// activating untrusted first-installs the puller just staged for review.
		if _, err := syncLockStep(ctx, cfg, LockDependenciesRequest{FS: fs, SkipSync: true, StageUntrustedNew: true}); err != nil {
			clidiag.Warn("ctxloom", "failed to generate lockfile after sync: %v", err)
			zap.L().Warn("failed to generate lockfile", zap.Error(err))
		}
	}

	// Apply hooks whenever there were remote references, so MCP servers from
	// bundles get registered even if every dependency was already installed.
	// A wholesale apply failure is fatal-class in strict mode (hook/MCP/
	// settings apply — per-backend partial failures are already instrumented
	// inside ApplyHooks); degraded mode warns and continues.
	if req.ApplyHooks && result.Total > 0 {
		if _, err := syncHooksStep(ctx, cfg, ApplyHooksRequest{
			Backend:           "all",
			RegenerateContext: true,
		}); err != nil {
			strictness.Fail(strictness.ClassApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
				"failed to apply hooks after sync: %v", err)
			zap.L().Warn("failed to apply hooks", zap.Error(err))
		}
	}
}

// recordReviewState records the active↔pending bundle delta on result so the MCP
// review surface can show any changes a `remote upgrade` staged for review.
// Sync itself never stages (it installs exactly the pinned set) and never
// auto-applies trust — trusted changes are applied coherently by StageUpgrade,
// which rewrites the refs. result.Changes is empty when pending is empty.
func recordReviewState(cfg *config.Config, registry *remote.Registry, fs afero.Fs, result *SyncDependenciesResult) {
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
	// Trusted changes are applied coherently by StageUpgrade (it rewrites the
	// refs), and DiffLockfiles filters trusted entries from review regardless, so
	// there is nothing to promote here — just report the reviewable delta.
	return computeBundleChanges(cfg, registry, fs)
}

// syncRefURLs returns the unique repo URLs behind the given canonical refs, in
// first-seen order. Unparseable refs and local (ctxloom:local) refs carry no
// URL and are skipped — only network remotes have a clone to refresh. Mirrors
// uniqueRemoteURLs but works from raw refs (sync's input) rather than lockfile
// entries.
func syncRefURLs(refSets ...[]string) []string {
	seen := collections.NewSet[string]()
	var urls []string
	for _, refs := range refSets {
		for _, ref := range refs {
			parsed, err := remote.ParseReference(ref)
			if err != nil || parsed.URL == "" || seen.Has(parsed.URL) {
				continue
			}
			seen.Add(parsed.URL)
			urls = append(urls, parsed.URL)
		}
	}
	return urls
}

// collectRemoteReferences collects all remote bundle and profile references from config.
// This recursively follows local parent profiles to find remote dependencies anywhere
// in the inheritance chain.
func collectRemoteReferences(cfg *config.Config, profileNames []string, fs afero.Fs) (bundleRefs []string, err error) {
	bundleSet := collections.NewSet[string]()

	// Get profiles to process
	profilesToProcess := profileNames
	if len(profilesToProcess) == 0 {
		// Process all profiles from config
		for name := range cfg.Profiles.Definitions {
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
		collectProfileReferencesRecursive(cfg, profileName, bundleSet, visited)
	}

	// The default agent's composed profiles are dependency roots too: the
	// init-seeded default agent may name a remote bundle profile that no local
	// profile references. A bundle-profile default/parent
	// (<url>@bundles/x#profiles/y) resolves through its bundle's lockfile entry,
	// so sync (and lock) the underlying bundle by stripping the selector — else
	// the first `ctxloom run` after init fails to assemble the default.
	if len(profileNames) == 0 {
		for _, name := range cfg.DefaultAgentProfiles() {
			if isRemoteReference(name) {
				addRemoteBundleBase(bundleSet, name, "default profile")
			}
		}
	}

	return bundleSet.Items(), nil
}

// collectProfileReferences collects bundle and parent profile references from a profile.
func collectProfileReferences(cfg *config.Config, profileName string) (bundles []string, profiles []string) {
	// Try config-based profile first
	if profile, ok := cfg.Profiles.Definitions[profileName]; ok {
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
func collectProfileReferencesRecursive(cfg *config.Config, profileName string, bundleSet, visited collections.Set[string]) {
	// Prevent infinite loops
	if visited.Has(profileName) {
		return
	}
	visited.Add(profileName)

	bundles, parents := collectProfileReferences(cfg, profileName)

	// Add remote bundles. Strip any `#fragments/<name>` selector so distinct
	// fragment refs to the same bundle dedupe to a single fetch/lock entry; the
	// selector is re-applied at assembly time by the bundle loader.
	for _, b := range bundles {
		if isRemoteReference(b) {
			addRemoteBundleBase(bundleSet, b, fmt.Sprintf("profile %q", profileName))
		}
	}

	// Process parents.
	for _, parent := range parents {
		if isRemoteReference(parent) {
			// The only remote profile parents are bundle profiles
			// (<url>@bundles/x#profiles/y) — top-level @profiles/ was retired.
			// Sync (and lock) the underlying bundle by stripping the selector,
			// mirroring the #fragments/ handling above; the bundle profile's own
			// composed bundles are closed by FlattenDependencies.
			addRemoteBundleBase(bundleSet, parent, fmt.Sprintf("profile %q", profileName))
		} else {
			// Local parent - recursively collect its references. Strip "profile:"
			// prefix if present (distinguishes a profile ref from a bundle ref).
			localName := strings.TrimPrefix(parent, "profile:")
			collectProfileReferencesRecursive(cfg, localName, bundleSet, visited)
		}
	}
}

// addRemoteBundleBase adds ref's bundle base (item selector stripped) to
// bundleSet after checking the base still parses as a distributable reference.
// A ref in the retired top-level "@profiles/" grammar — or otherwise
// unparseable — must never enter the install plan: it cannot pull, so planning
// it walks the user into a confirmed install that then fails with "unknown
// item type". Warn once and keep collecting (CLAUDE.md fault tolerance:
// report the failure, continue with what works). owner names the referencing
// profile for the diagnostic.
func addRemoteBundleBase(bundleSet collections.Set[string], ref, owner string) {
	base, _, _ := strings.Cut(ref, "#")
	if _, _, retired := remote.SplitRetiredProfileRef(base); retired {
		clidiag.WarnOnce("ctxloom",
			"%s references %s in the retired top-level @profiles/ grammar; profiles ship inside bundles now (\"<url>@bundles/<bundle>#profiles/<name>\") — run `ctxloom remote upgrade` to refresh pins; skipping from sync",
			owner, ref)
		return
	}
	if _, err := remote.ParseReference(base); err != nil {
		clidiag.WarnOnce("ctxloom", "%s references invalid ref %s (%v); skipping from sync", owner, ref, err)
		return
	}
	bundleSet.Add(base)
}

// isRemoteReference checks if a reference points to a remote source. Remote
// refs are scheme-qualified canonical URLs; ctxloom:local and the "profile:"
// local-profile alias are not remote.
func isRemoteReference(ref string) bool {
	if strings.HasPrefix(ref, remote.LocalSource) { // ctxloom:local
		return false
	}
	return strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "git@") ||
		strings.HasPrefix(ref, "file://")
}

// syncItem syncs a single item and returns the result.
func syncItem(ctx context.Context, puller Puller, ref string, itemType remote.ItemType, baseDir string, force bool, bundles remote.BundleByteSource) SyncItem {
	item := SyncItem{
		Reference: ref,
		Type:      string(itemType),
	}

	// Validate the reference before pulling.
	if _, err := remote.ParseReference(ref); err != nil {
		item.Status = "failed"
		item.Error = fmt.Sprintf("invalid reference: %v", err)
		return item
	}

	// Skip already-installed items (unless force): lockfile entry + content
	// retrievable from the clone cache, same probe CheckMissingDependencies
	// uses. Nothing lives on disk in the reference-only model.
	if !force && isInstalled(ctx, ref, bundles) {
		item.Status = "skipped"
		return item
	}

	// Pull the item. The pull must be Blind: there is no interactive
	// confirmation to give — the bundle-review gate is the confirmation — and
	// any content display would be premature. Because Blind skips the security
	// review entirely, StageUntrustedNew is the compensating gate: a FIRST
	// install from an untrusted remote lands in the pending lockfile for
	// `ctxloom bundle review` instead of the active one, so its hooks/MCP/
	// context cannot activate until approved. Stdout is pinned to stderr
	// because sync runs inside the MCP server, whose process stdout carries
	// the JSON-RPC stream; pull's informational output (Blind-mode notice,
	// lockfile warnings) must never land there.
	opts := remote.PullOptions{
		LocalDir:          baseDir,
		Force:             force,
		Blind:             true,
		StageUntrustedNew: true,
		ItemType:          itemType,
		Stdout:            os.Stderr,
	}

	result, err := puller.Pull(ctx, ref, opts)
	if err != nil {
		item.Status = "failed"
		item.Error = err.Error()
		return item
	}

	item.LocalPath = result.LocalPath
	switch {
	case result.Staged:
		item.Status = "staged"
	case result.Overwritten:
		item.Status = "updated"
	default:
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
	case "staged":
		result.Staged = append(result.Staged, item)
	case "failed":
		result.Failed = append(result.Failed, item)
		result.Errors++
	}
}

// CheckMissingDependenciesRequest contains parameters for checking missing deps.
type CheckMissingDependenciesRequest struct {
	Profiles []string `json:"profiles,omitempty"`
	// BundleReader probes whether a bundle's content is retrievable at its
	// locked address. Nil in production (built from cfg); injected in tests.
	BundleReader remote.BundleByteSource `json:"-"`
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
	// Remote items live in the git clone cache, not on disk, so installed
	// state is probed through the read path rather than a file check.
	bundleReader := req.BundleReader
	if bundleReader == nil {
		bundleReader = NewBundleReaderForConfig(cfg)
	}
	var missing []MissingDependency
	seen := collections.NewSet[string]()

	for _, profileName := range resolveProfilesToCheck(cfg, req.Profiles) {
		bundles, parents := collectProfileReferences(cfg, profileName)
		// A bundle-profile parent (<url>@bundles/x#profiles/y) contributes its
		// underlying bundle to the installed-check; top-level @profiles/ parents
		// were retired. Local parents are separate profiles, probed in their own
		// iteration of resolveProfilesToCheck.
		refs := append(append([]string(nil), bundles...), parentBundleRefs(parents)...)
		missing = append(missing, collectMissingRefs(ctx, refs, "bundle", profileName, bundleReader, seen)...)
	}

	// The default agent's composed profiles are dependency roots too — mirror
	// collectRemoteReferences. An init-seeded default agent may name a bundle
	// profile that no local profile references; resolveProfilesToCheck only
	// enumerates Definitions keys and directory profiles, so such a default is
	// never probed. Without this, the SyncOnStartup gate reports Count 0 and
	// short-circuits to "up_to_date", leaving the default's bundle never
	// auto-installed. Only probe defaults when no explicit profiles were
	// requested; collectMissingRefs filters to remote refs and dedupes via seen.
	if len(req.Profiles) == 0 {
		missing = append(missing, collectMissingRefs(ctx, parentBundleRefs(cfg.DefaultAgentProfiles()), "bundle", "", bundleReader, seen)...)
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
	for name := range cfg.Profiles.Definitions {
		names = append(names, name)
	}
	loader := cfg.GetProfileLoader()
	dirProfiles, _ := loader.List()
	for _, p := range dirProfiles {
		names = append(names, p.Name)
	}
	return names
}

// parentBundleRefs maps remote profile-parent / config-default refs to the
// bundle whose installation they require: a bundle-profile ref
// (<url>@bundles/x#profiles/y) yields its bundle <url>@bundles/x. Non-remote
// (local) refs are dropped — they are probed as their own profiles. Top-level
// @profiles/ refs were retired; carrying no selector they map to themselves and
// resolve as "not installed".
func parentBundleRefs(refs []string) []string {
	var out []string
	for _, r := range refs {
		if !isRemoteReference(r) {
			continue
		}
		// Retired top-level @profiles/ refs carry no selector, so they would map
		// to themselves and read as "not installed" — the actual sync
		// (addRemoteBundleBase) skips them, so they must never be offered as a
		// missing dependency (the user would be prompted for a dep sync then
		// rejects).
		if _, _, ok := remote.SplitRetiredProfileRef(r); ok {
			continue
		}
		base, _, _ := strings.Cut(r, "#")
		out = append(out, base)
	}
	return out
}

// collectMissingRefs returns the not-yet-installed remote bundle refs among
// refs, skipping local refs and any ref already in seen (marking the rest seen).
func collectMissingRefs(ctx context.Context, refs []string, typeName, profileName string, bundles remote.BundleByteSource, seen collections.Set[string]) []MissingDependency {
	var missing []MissingDependency
	for _, ref := range refs {
		if !isRemoteReference(ref) || seen.Has(ref) {
			continue
		}
		seen.Add(ref)
		if !isInstalled(ctx, ref, bundles) {
			missing = append(missing, MissingDependency{
				Reference: ref,
				Type:      typeName,
				Profile:   profileName,
			})
		}
	}
	return missing
}

// isInstalled reports whether a bundle reference is installed.
//
// A bundle is installed only when the active lockfile holds an entry AND the
// content is retrievable at that entry's address (URL + SHA): remote bundles are
// pure references, never written to disk, so a file check can never see them.
// The byte-source read proves both — ErrBundleNotInLockfile with no entry, a
// fetch error when the locked SHA is absent from the clone cache.
//
// The probe key is the ref's canonical string (no version constraint, no
// selector). A bundle-profile ref must be stripped to its bundle before probing.
func isInstalled(ctx context.Context, ref string, bundles remote.BundleByteSource) bool {
	parsedRef, err := remote.ParseReference(ref)
	if err != nil {
		return false
	}
	if bundles == nil {
		return false
	}
	_, rerr := bundles.ReadBundleBytes(ctx, parsedRef.CanonicalString())
	return rerr == nil
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

// startupCloneRefresh is the seam over the pre-probe clone refresh (test
// injection point; production = refreshReferencedClones).
var startupCloneRefresh = refreshReferencedClones

// refreshReferencedClones advances every remote clone the config references
// (the bundle refs collectRemoteReferences gathers across profiles and config
// defaults) to its live tip. Best-effort throughout: a collect or fetch
// failure leaves the cache as-is; the probe and sync paths surface any real
// problem.
func refreshReferencedClones(ctx context.Context, cfg *config.Config) {
	bundleRefs, err := collectRemoteReferences(cfg, nil, getFS(nil))
	if err != nil {
		return
	}
	refreshRepoCaches(ctx, newRepoCache(cfg), syncRefURLs(bundleRefs))
}

// SyncOnStartup is a convenience function that runs sync with sensible defaults.
// This is meant to be called during MCP server initialization or CLI startup.
func SyncOnStartup(ctx context.Context, cfg *config.Config) (*SyncDependenciesResult, error) {
	// Refresh every referenced clone to its live tip BEFORE the
	// missing-dependency probe. In steady state (everything installed) the probe
	// reports Count 0 and short-circuits below — so this is the ONLY fetch a
	// healthy startup performs; without it a project's clone cache goes stale
	// indefinitely (SyncDependencies' own refresh is unreachable then, and the
	// probe itself reads the possibly-stale cache to decide "missing"). Failures
	// warn and continue inside refreshRepoCaches — an offline startup must never
	// block the LLM (CLAUDE.md).
	startupCloneRefresh(ctx, cfg)

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
