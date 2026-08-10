package operations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// maxSyncPasses bounds the collect→pull fixed-point iteration in
// SyncDependencies. Each pass can only reveal new references by installing a
// dependency that unblocks a profile, so the pass count is bounded by the depth
// of the profile→remote-parent chain — single digits in any real graph. The
// bound exists purely so a pathological or cyclic graph cannot spin forever.
const maxSyncPasses = 10

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
	Status    string `json:"status"` // "installed", "updated", "skipped", "retracted", "failed"
	Error     string `json:"error,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
}

// RetractionChecker is the OPTIONAL seam a Puller may satisfy to let sync
// re-evaluate retraction for a ref that is ALREADY installed. This is the
// fix for the gap where syncItem's install-skip meant the retraction check
// wired into Puller.Pull (confirmRetraction) never ran again once a bundle
// was pinned — retraction had no effect on anything already distributed. A
// Puller that doesn't implement this (e.g. a minimal test double) simply
// skips the re-check, never a regression: a fresh (unskipped) Pull still
// reports its own retraction verdict via PullResult.Retracted.
//
// *remote.Puller (the production implementation) satisfies this; sync type-
// asserts for it rather than widening the base Puller interface (lockfile.go),
// which other callers (InstallDependencies, tests) implement minimally.
type RetractionChecker interface {
	// CheckRetraction reports whether refStr is CURRENTLY retracted in its
	// remote's manifest — a live network probe, but far cheaper than a full
	// Pull (no content re-fetch, no lockfile SHA rewrite). When the remote
	// cannot be reached, this falls back to the last verdict this project
	// itself recorded for refStr (fail-stale — see
	// internal/remote/retract.go's RetractionVerdict and
	// Puller.resolveRetraction) rather than reporting "not retracted"; err is
	// non-nil only for a genuinely undeterminable manifest (e.g. unparseable),
	// which the caller must not paper over. checkedAt is when the returned
	// verdict was actually established (now for a fresh check, the persisted
	// entry's own timestamp for a fallback) — pass it straight through to
	// RecordRetraction so a fallback never fabricates a fresher timestamp than
	// it earned.
	CheckRetraction(ctx context.Context, refStr string, itemType remote.ItemType) (retracted bool, reason string, checkedAt time.Time, err error)
	// RecordRetraction persists retracted/reason/checkedAt onto refStr's
	// EXISTING lockfile entry (a no-op if there is none yet). A zero checkedAt
	// leaves the persisted timestamp untouched.
	RecordRetraction(itemType remote.ItemType, refStr string, retracted bool, reason string, checkedAt time.Time) error
}

// SyncDependenciesResult contains the result of syncing dependencies.
type SyncDependenciesResult struct {
	Status  string     `json:"status"`
	Synced  []SyncItem `json:"synced,omitempty"`
	Skipped []SyncItem `json:"skipped,omitempty"`
	// Retracted lists refs whose remote manifest currently retracts them —
	// surfaced separately from Skipped/Failed so a caller (the `deps pull`
	// CLI) can tell the user their content was retracted, whether that was
	// learned from a fresh pull or re-checked on an already-installed ref.
	Retracted []SyncItem `json:"retracted,omitempty"`
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
	fs := getFS(req.FS)
	baseDir := getBaseDir(cfg)

	// Collect all remote bundle references from profiles. Bundle profiles used
	// as parents contribute their underlying bundle; top-level remote profiles
	// were retired, so there is no separate profile-ref set.
	bundleRefs, err := collectRemoteReferences(cfg, req.Profiles)
	if err != nil {
		return nil, fmt.Errorf("failed to collect references: %w", err)
	}

	if len(bundleRefs) == 0 {
		return &SyncDependenciesResult{
			Status:  "empty",
			Message: "No remote references found in profiles",
		}, nil
	}

	puller, err := resolveSyncDeps(cfg, req, baseDir, fs)
	if err != nil {
		return nil, err
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

	// Sync to a FIXED POINT, not in a single pass. collectRemoteReferences can
	// only see the refs of profiles that currently resolve: a profile whose
	// remote parent is not yet installed fails to load and contributes NOTHING
	// — not even the bundles it references directly (collectProfileReferences
	// swallows the loader error). Installing that parent makes the profile
	// loadable, revealing refs the first pass could not have known about. So
	// re-collect after each pass and pull whatever is newly visible, until the
	// reference set stops growing. Collecting once leaves part of the graph
	// unpinned while still exiting 0, which forces the user to re-run
	// `deps pull` until it happens to converge.
	synced := collections.NewSet[string]()
	for pass := 0; pass < maxSyncPasses; pass++ {
		var pending []string
		for _, ref := range bundleRefs {
			if !synced.Has(ref) {
				pending = append(pending, ref)
			}
		}
		if len(pending) == 0 {
			break
		}

		// Refresh each referenced clone to its live tip before pulling. A first
		// install resolves an unpinned ref to the default-branch HEAD, and the
		// cache serves an existing clone as-is (ensureClone never fetches — only
		// an explicit UpdateRepo does). A stale clone — e.g. one predating an
		// upstream layout migration — would otherwise resolve to old content or
		// 404 a moved path. CheckOutdated/Relock refresh for the same reason.
		// Skipped when a Puller is injected (tests drive a mock fetcher with no
		// real clone to advance); per-URL failures warn and continue.
		if req.Puller == nil {
			refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(pending))
		}

		if err := syncRefs(ctx, puller, pending, remote.ItemTypeBundle, baseDir, req.Force, bundleReader, result); err != nil {
			return result, err
		}
		for _, ref := range pending {
			synced.Add(ref)
		}

		// Re-collect: the pulls above may have made previously-unresolvable
		// profiles loadable. A collect failure here is not fatal — keep the
		// items already synced (CLAUDE.md fault tolerance).
		next, err := collectRemoteReferences(cfg, req.Profiles)
		if err != nil {
			clidiag.Warn("ctxloom", "failed to re-collect references after sync pass: %v", err)
			break
		}
		bundleRefs = next
	}

	// Only warn when the graph is GENUINELY still unconverged — the final
	// re-collect surfaced refs nothing has synced. Checking this inside the
	// loop fired whenever the loop merely REACHED the last pass, including
	// when that pass converged the graph.
	for _, ref := range bundleRefs {
		if !synced.Has(ref) {
			clidiag.Warn("ctxloom",
				"dependency graph still revealing new references after %d sync passes; "+
					"run 'ctxloom deps pull' again to continue converging", maxSyncPasses)
			break
		}
	}

	runSyncPostSteps(ctx, cfg, req, result, fs)

	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}

	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d updated, %d skipped, %d retracted, %d failed",
		result.Total, result.Installed, result.Updated, len(result.Skipped), len(result.Retracted), result.Errors)

	return result, nil
}

// resolveSyncDeps returns the registry and puller for a sync, preferring
// injected (test) instances. Sync installs exactly the pinned set and writes
// each pin straight to the active lockfile; surfacing an upstream change to an
// already-locked item is `deps upgrade`'s job (operations.UpgradeDependencies),
// and the post-sync lock rebuilds the active lockfile from the pinned closure.
// Whether pulled content ever reaches the agent is decided per item at exposure
// by the content-hash trust gate, so sync itself needs no review ceremony.
func resolveSyncDeps(cfg *config.Config, req SyncDependenciesRequest, baseDir string, fs afero.Fs) (Puller, error) {
	registry := req.Registry
	if registry == nil {
		var err error
		registry, err = getRegistry(cfg, remote.WithRegistryFS(fs))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize registry: %w", err)
		}
	}

	puller := req.Puller
	if puller == nil {
		auth := remote.LoadAuth(baseDir)
		puller = remote.NewPuller(registry, auth,
			remote.WithFetcherFactory(NewCachedFetcherFactory(cfg)),
			remote.WithLockfileManager(remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))),
			// The directory-form half of the fetch. remote cannot reach the
			// content layer that owns the pinned-tree walker, so composition
			// happens here — the one place that already knows both.
			remote.WithTreeFetcher(remotetree.PullTreeFetcher),
		)
	}
	return puller, nil
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
	syncLockStep func(context.Context, *config.Config, LockDependenciesRequest) (*LockDependenciesResult, error)
	// syncHooksStep takes no *config.Config: ApplyHooks reloads config from
	// disk itself, so passing one here would be the same silent discard the
	// parameter removal fixed.
	syncHooksStep func(context.Context, ApplyHooksRequest) (*ApplyHooksResult, error)
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
		// second sync pass.
		if _, err := syncLockStep(ctx, cfg, LockDependenciesRequest{FS: fs, SkipSync: true}); err != nil {
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
		if _, err := syncHooksStep(ctx, ApplyHooksRequest{
			Backend:           "all",
			RegenerateContext: true,
		}); err != nil {
			strictness.Fail(strictness.ClassApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
				"failed to apply hooks after sync: %v", err)
			zap.L().Warn("failed to apply hooks", zap.Error(err))
		}
	}
}

// syncRefURLs returns the unique repo URLs behind the given canonical refs, in
// first-seen order. Unparseable refs and local (ctxloom:local) refs carry no
// URL and are skipped — only network remotes have a clone to refresh. Mirrors
// uniqueRemoteURLs but works from raw refs (sync's input) rather than lockfile
// entries.
func syncRefURLs(refs []string) []string {
	seen := collections.NewSet[string]()
	var urls []string
	for _, ref := range refs {
		parsed, err := remote.ParseReference(ref)
		if err != nil || parsed.URL == "" || seen.Has(parsed.URL) {
			continue
		}
		seen.Add(parsed.URL)
		urls = append(urls, parsed.URL)
	}
	return urls
}

// collectRemoteReferences collects all remote bundle and profile references from config.
// This recursively follows local parent profiles to find remote dependencies anywhere
// in the inheritance chain.
func collectRemoteReferences(cfg *config.Config, profileNames []string) (bundleRefs []string, err error) {
	bundleSet := collections.NewSet[string]()

	// Get profiles to process
	profilesToProcess := profileNames
	if len(profilesToProcess) == 0 {
		// Process all profiles from config
		for name := range cfg.GetProfileDefinitions() {
			profilesToProcess = append(profilesToProcess, name)
		}

		// Also get directory-based profiles. A List failure must actually
		// reach this function's own (previously always-nil) err return
		// rather than being discarded — every call site
		// already handles a non-nil err from collectRemoteReferences
		// correctly (abort, or warn-and-continue on a re-collect pass).
		loader := cfg.GetProfileLoader()
		dirProfiles, lerr := loader.List()
		if lerr != nil {
			return nil, fmt.Errorf("list directory profiles: %w", lerr)
		}
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
	if profile, ok := cfg.GetProfileDefinitions()[profileName]; ok {
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
			"%s references %s in the retired top-level @profiles/ grammar; profiles ship inside bundles now — point the parent at \"<url>@bundles/<bundle>#profiles/<name>\" (or install a bundle that ships it, which auto-rewrites the parent on load); skipping from sync",
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
	//
	// Retraction must still be RE-EVALUATED here even though the item is
	// skipped: this was the gap (task: retraction had no effect on anything
	// already distributed) — confirmRetraction only ever ran inside a fresh
	// Pull, and an already-installed ref never pulls again on an ordinary
	// sync. checkInstalledRetraction runs the lightweight (no content
	// re-fetch) check and persists its verdict onto the existing lockfile
	// entry, so EffectiveTrust sees it on the very next exposure without any
	// network call of its own.
	if !force && isInstalled(ctx, ref, bundles) {
		if retracted, reason := checkInstalledRetraction(ctx, puller, ref, itemType); retracted {
			item.Status = "retracted"
			item.Error = reason
			return item
		}
		item.Status = "skipped"
		return item
	}

	// Pull the item. Force so the non-interactive sync never blocks on a
	// retraction prompt (there is no other confirmation gate — exposure of the
	// pulled content is decided per item by the content trust gate). Stdout is
	// pinned to stderr because sync runs inside the MCP server, whose process
	// stdout carries the JSON-RPC stream; pull's informational output (lockfile
	// warnings) must never land there.
	opts := remote.PullOptions{
		Force:    true,
		ItemType: itemType,
		Stdout:   os.Stderr,
	}

	result, err := puller.Pull(ctx, ref, opts)
	if err != nil {
		item.Status = "failed"
		item.Error = err.Error()
		return item
	}

	item.LocalPath = result.LocalPath
	if result.Retracted {
		// The pull SUCCEEDED (Force always bypasses the cancel-on-decline
		// path here) but the publisher has retracted it — surface that to the
		// user distinctly from a plain install/update; Pull already persisted
		// Retracted onto the lockfile entry it just wrote (see
		// Puller.updateLockfile), so EffectiveTrust withholds it from here on.
		item.Status = "retracted"
		item.Error = result.RetractedReason
		return item
	}
	if result.Overwritten {
		item.Status = "updated"
	} else {
		item.Status = "installed"
	}

	return item
}

// checkInstalledRetraction re-evaluates retraction for a ref that syncItem is
// about to skip as already-installed. It is best-effort and fault-tolerant by
// construction, matching the rest of this file's CLAUDE.md discipline: a
// puller that doesn't implement RetractionChecker (a minimal test double)
// still silently reports "not retracted" rather than blocking or failing the
// sync — retraction is a security IMPROVEMENT layered on top of sync, never a
// new way for sync itself to fail.
//
// An UNREACHABLE remote is no longer in that "silently not retracted" bucket:
// CheckRetraction itself now falls back to the last verdict this project
// recorded for ref (fail-stale), so retracted here reflects that fallback,
// not a false "clean". Only a genuinely
// undeterminable manifest (parse failure) still resolves to "not retracted"
// here — CheckRetracted already turns that into a hard error, and this
// function's contract stays "never a new way for sync to fail", so it swallows
// that error rather than propagating it.
//
// When the manifest reports NOT retracted, it still calls RecordRetraction to
// clear any stale retracted flag from a previous sync (RecordRetraction itself
// no-ops when nothing would change) — so a publisher un-retracting content is
// honored too, not just the one-way trip to withheld.
func checkInstalledRetraction(ctx context.Context, puller Puller, ref string, itemType remote.ItemType) (retracted bool, reason string) {
	rc, ok := puller.(RetractionChecker)
	if !ok {
		return false, ""
	}
	// CheckRetraction itself now falls back to the last recorded verdict when
	// the remote is unreachable (fail-stale), so err here is reserved for a
	// genuinely undeterminable manifest (e.g. unparseable) — that case still
	// silently reports "not retracted" rather than blocking sync, matching
	// this function's fault-tolerant contract; it does NOT re-record (nothing
	// new was established, so nothing overwrites whatever was already there).
	retracted, reason, checkedAt, err := rc.CheckRetraction(ctx, ref, itemType)
	if err != nil {
		return false, ""
	}
	// A failure to PERSIST the verdict (distinct from a failure to check it)
	// is not the "tolerate an unreachable remote" case this function's
	// contract carves out — it silently drops a security improvement (or,
	// worse, a genuine retraction) on the floor with no diagnostic at all.
	// Still best-effort (never blocks or fails sync), just no longer silent.
	if rerr := rc.RecordRetraction(itemType, ref, retracted, reason, checkedAt); rerr != nil {
		clidiag.Warn("ctxloom", "record retraction verdict for %s: %v", ref, rerr)
	}
	return retracted, reason
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
	case "retracted":
		result.Retracted = append(result.Retracted, item)
	case "failed":
		result.Failed = append(result.Failed, item)
		result.Errors++
	default:
		// An unrecognized Status must never just vanish from every bucket and
		// counter — file it as failed and say why, rather than
		// silently disagreeing with result.Total.
		clidiag.Warn("ctxloom", "sync: item %q has unrecognized status %q; recording as failed", item.Reference, item.Status)
		item.Error = fmt.Sprintf("unrecognized sync status %q", item.Status)
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
	for name := range cfg.GetProfileDefinitions() {
		names = append(names, name)
	}
	loader := cfg.GetProfileLoader()
	dirProfiles, err := loader.List()
	if err != nil {
		// This used to discard the error outright, so an unreadable
		// profiles directory silently shrank the probed set with no
		// diagnostic at all.
		clidiag.Warn("ctxloom", "list directory profiles: %v", err)
	}
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

// startupCloneRefresh is the seam over the pre-probe clone refresh (test
// injection point; production = refreshReferencedClones).
var startupCloneRefresh = refreshReferencedClones

// refreshReferencedClones advances every remote clone the config references
// (the bundle refs collectRemoteReferences gathers across profiles and config
// defaults) to its live tip. Best-effort throughout: a collect or fetch
// failure leaves the cache as-is; the probe and sync paths surface any real
// problem.
func refreshReferencedClones(ctx context.Context, cfg *config.Config) {
	bundleRefs, err := collectRemoteReferences(cfg, nil)
	if err != nil {
		return
	}
	refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(bundleRefs))
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
