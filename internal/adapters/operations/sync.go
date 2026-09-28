package operations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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

	// AllowDowngrade names the refs whose signed version floor the operator
	// waives for this run (`deps pull --allow-downgrade <ref>`). Never blanket.
	AllowDowngrade []string `json:"allow_downgrade,omitempty"`

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
	// cause is the failure Error was rendered from, kept typed so Remedy can
	// read the fix its raise site named rather than matching the text.
	cause error
}

// The fix lines a failed sync item carries when its cause names none of its
// own (remedySyncFailed), and the one its invalid-reference cause names.
const (
	remedySyncFailed       = "check network/auth and retry (ctxloom deps pull), or drop the reference from its profile"
	remedyInvalidReference = "fix the reference's spelling in its profile, or drop it"
)

// Remedy is the item's fix line: the fix its cause names (clifmt.RemedyOf),
// else remedySyncFailed; "" unless the item failed. It is a method, not a
// field, so the item's JSON shape is unchanged — a structured consumer
// that wants the fix serialises Remedy() itself.
func (i SyncItem) Remedy() string {
	if i.Status != "failed" {
		return ""
	}
	if fix, ok := clifmt.RemedyOf(i.cause); ok {
		return fix
	}
	return remedySyncFailed
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
	// internal/adapters/remote/retract.go's RetractionVerdict and
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
	// Removed names the lockfile entries the post-pull lock rebuild dropped
	// because nothing the project composes reaches them any more.
	Removed   []string `json:"removed,omitempty"`
	Total     int      `json:"total"`
	Installed int      `json:"installed"`
	Updated   int      `json:"updated"`
	Errors    int      `json:"errors"`
	Message   string   `json:"message,omitempty"`
}

// SyncDependencies syncs remote bundles and profiles referenced in config.
// This is the main entry point for auto-fetch on startup.
func SyncDependencies(ctx context.Context, app *App, req SyncDependenciesRequest) (*SyncDependenciesResult, error) {
	if app == nil {
		return nil, fmt.Errorf("sync: app is required")
	}
	reg := app.Engines()
	cfg, err := app.Config(ctx)
	if err != nil {
		return nil, err
	}
	fs := getFS(req.FS)
	baseDir := ProjectAppDir(cfg)

	// Collect all remote bundle references from profiles. Bundle profiles used
	// as parents contribute their underlying bundle; top-level remote profiles
	// were retired, so there is no separate profile-ref set.
	bundleRefs := collectRemoteReferences(cfg, req.Profiles)

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
	downgrades, err := newDowngradeSet(req.AllowDowngrade)
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

	// The loop's two dependencies, named as arguments rather than reached for
	// through cfg. Building them here — and nowhere else — is what keeps
	// syncToFixedPoint free of any knowledge that a Config exists.
	collect := func() []string { return collectRemoteReferences(cfg, req.Profiles) }

	pullBatch := func(ctx context.Context, refs []string) error {
		// Refresh each referenced clone to its live tip before pulling. A first
		// install resolves an unpinned ref to the default-branch HEAD, and the
		// cache serves an existing clone as-is (ensureClone never fetches — only
		// an explicit UpdateRepo does). A stale clone — e.g. one predating an
		// upstream layout migration — would otherwise resolve to old content or
		// 404 a moved path. CheckOutdated/Relock refresh for the same reason.
		// Skipped when a Puller is injected (tests drive a mock fetcher with no
		// real clone to advance); per-URL failures warn and continue.
		if req.Puller == nil {
			refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(refs))
		}
		if err := syncRefs(ctx, puller, refs, remote.ItemTypeBundle, baseDir, req.Force, bundleReader, downgrades, result); err != nil {
			return err
		}
		// A pull lands new pinned content: the next generation is the one that
		// holds it AND the lockfile's retraction records. Post-sync steps read
		// bundles immediately (lockfile, hook materialization), and a later
		// pass may resolve a profile that only became loadable because of this
		// pull, so every later read in this sync is against the reloaded
		// generation.
		snap, rerr := app.Reload(ctx)
		if rerr != nil {
			return fmt.Errorf("reload configuration after pull: %w", rerr)
		}
		cfg = snap.Config
		return nil
	}

	converged, err := syncToFixedPoint(ctx, bundleRefs, collect, pullBatch)
	if err != nil {
		return result, err
	}
	if !converged {
		clidiag.Warn("ctxloom",
			"dependency graph still revealing new references after %d sync passes; "+
				"run 'ctxloom deps pull' again to continue converging", maxSyncPasses)
	}

	runSyncPostSteps(ctx, reg, cfg, req, result, fs)

	summarizeSync(result)
	return result, nil
}

// summarizeSync settles the result's status and one-line tally.
func summarizeSync(result *SyncDependenciesResult) {
	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}
	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d updated, %d skipped, %d retracted, %d failed",
		result.Total, result.Installed, result.Updated, len(result.Skipped), len(result.Retracted), result.Errors)
}

// RefCollector reports every remote ref currently visible.
//
// The fixed-point loop calls it once per PASS, and naming it as a dependency is
// the point of this type existing. The loop used to hold a *config.Config and
// re-interrogate it, which made its real requirement — a fresh read of the
// filesystem after every pull — invisible in the signature and easy to break:
// memoizing any resolved state behind that Config would have frozen the ref set,
// and sync would exit 0 having silently left part of the graph unpinned.
type RefCollector func() []string

// PullBatch pulls one batch of refs, recording outcomes wherever the caller
// chose to record them.
type PullBatch func(ctx context.Context, refs []string) error

// syncToFixedPoint pulls until the visible ref set stops growing, reporting
// whether the graph converged.
//
// A single pass is not enough. collect can only see the refs of profiles that
// currently RESOLVE: a profile whose remote parent is not yet installed fails to
// load and contributes nothing — not even the bundles it references directly
// (collectProfileReferences swallows the loader error). Installing that parent
// makes the profile loadable, revealing refs the first pass could not have known
// about. Collecting once leaves part of the graph unpinned while still exiting
// 0, forcing the user to re-run `deps pull` until it happens to converge.
//
// It holds no Config and touches no filesystem: everything it needs is an
// argument, so what it depends on is exactly what its signature says.
func syncToFixedPoint(ctx context.Context, initial []string, collect RefCollector, pull PullBatch) (converged bool, err error) {
	refs := initial
	synced := collections.NewSet[string]()

	for pass := 0; pass < maxSyncPasses; pass++ {
		var pending []string
		for _, ref := range refs {
			if !synced.Has(ref) {
				pending = append(pending, ref)
			}
		}
		if len(pending) == 0 {
			break
		}

		if err := pull(ctx, pending); err != nil {
			return false, err
		}
		for _, ref := range pending {
			synced.Add(ref)
		}

		// Re-collect: the pulls above may have made previously-unresolvable
		// profiles loadable.
		refs = collect()
	}

	// Converged means the graph is settled, NOT that the loop finished early.
	// Reporting on the pass counter warned whenever the loop merely REACHED the
	// last pass, including when that pass converged the graph.
	for _, ref := range refs {
		if !synced.Has(ref) {
			return false, nil
		}
	}
	return true, nil
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
			// The directory-form half of the INSTALL, composed here for the
			// same reason: git owns the checkout (remote.RepoCache) and the
			// content layer owns the tree format that decides its modes.
			remote.WithTreeInstaller(remotetree.WorktreeInstaller(NewRepoCache(cfg))),
			// Verify before pin: the tree is held to its publisher's signature
			// and the entry's version floor before anything is checked out or
			// recorded, with the same verifier every reader uses.
			remote.WithTreeVerifier(bundles.TreeVerifier(cfg.Trust().Root())),
			// Retractions are read only from the default branch's signed tip
			// manifest, verified by the same trust root.
			remote.WithManifestVerifier(bundles.ManifestVerifier(cfg.Trust().Root())),
		)
	}
	return puller, nil
}

// syncRefs syncs each ref of one item type into result, checking for context
// cancellation between items (returns ctx.Err() to abort the whole sync).
func syncRefs(ctx context.Context, puller Puller, refs []string, itemType remote.ItemType, baseDir string, force bool, bundles remote.BundleByteSource, downgrades downgradeSet, result *SyncDependenciesResult) error {
	for _, ref := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item := syncItem(ctx, puller, ref, itemType, baseDir, force, bundles, downgrades)
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
	// syncHooksStep applies from the generation runSyncPostSteps holds — the
	// one reloaded after the last pull.
	syncHooksStep func(context.Context, engine.Registry, ApplyHooksRequest) (*ApplyHooksResult, error)
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
func runSyncPostSteps(ctx context.Context, reg engine.Registry, cfg *config.Config, req SyncDependenciesRequest, result *SyncDependenciesResult, fs afero.Fs) {
	if req.Lock && result.Installed+result.Updated > 0 {
		// The puller already wrote the lockfile inline during this sync, so the
		// lock step only needs to surface it — SkipSync avoids a redundant
		// second sync pass.
		lockRes, err := syncLockStep(ctx, cfg, LockDependenciesRequest{FS: fs})
		if err != nil {
			clidiag.Warn("ctxloom", "failed to generate lockfile after sync: %v", err)
			zap.L().Warn("failed to generate lockfile", zap.Error(err))
		} else if lockRes != nil {
			result.Removed = lockRes.Removed
		}
	}

	// Apply hooks whenever there were remote references, so MCP servers from
	// bundles get registered even if every dependency was already installed.
	// A wholesale apply failure is fatal-class in strict mode (hook/MCP/
	// settings apply — per-backend partial failures are already instrumented
	// inside ApplyHooks); degraded mode warns and continues.
	if req.ApplyHooks && result.Total > 0 {
		if _, err := syncHooksStep(ctx, reg, ApplyHooksRequest{
			Cfg:               cfg,
			RegenerateContext: true,
		}); err != nil {
			strictness.Fail(report.KindApply, "fix the failure, then re-apply (ctxloom manage hooks install)",
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

// collectRemoteReferences returns the remote bundle refs the project closure
// reaches (see closureBundleRefs), selector stripped, first-seen order — the
// set `deps pull` installs.
func collectRemoteReferences(cfg *config.Config, profileNames []string) []string {
	reached := closureBundleRefs(cfg, profileNames)
	refs := make([]string, 0, len(reached))
	for _, r := range reached {
		refs = append(refs, r.ref)
	}
	return refs
}

// closureRef is one remote bundle the closure reaches, and the profile whose
// edge reached it.
type closureRef struct {
	ref   string // bundle base: the ref with any item selector stripped
	owner string // name of the profile that references it
}

// closureBundleRefs walks the project closure from the root set every deps
// command shares (closureRoots; or exactly profileNames when given) and returns
// each remote bundle it reaches, once per bundle identity.
//
// This is the install-side walk: it yields refs as the profiles spell them,
// because a pull must carry each ref's version constraint. The lock-side walk
// (flattenRootsWith) resolves those same edges to commits. The two start from
// the same roots and follow the same edges — a bundle-profile parent pins its
// bundle AND is walked into — so pull installs what lock and upgrade pin.
//
// A bundle-profile parent is readable only once its bundle is installed, so
// before that its own dependencies are not yet visible. syncToFixedPoint
// re-collects after every pull for exactly that reason.
func closureBundleRefs(cfg *config.Config, profileNames []string) []closureRef {
	loader := profileLoader(cfg)
	var roots []*profiles.Profile
	if len(profileNames) == 0 {
		roots, _ = closureRoots(cfg, loader)
	} else {
		roots, _ = namedRoots(cfg, loader, profileNames)
	}
	c := &refCollector{loader: loader, seen: collections.NewSet[string](), visited: collections.NewSet[string]()}
	for _, root := range roots {
		c.walk(root)
	}
	return c.refs
}

// refCollector is one closureBundleRefs walk.
type refCollector struct {
	loader  *profiles.Loader
	seen    collections.Set[string] // bundle identities already collected
	visited collections.Set[string] // profile names already walked
	refs    []closureRef
}

func (c *refCollector) walk(p *profiles.Profile) {
	if c.visited.Has(p.Name) {
		return
	}
	c.visited.Add(p.Name)
	for _, b := range p.Bundles {
		if isRemoteReference(b) {
			c.add(b, p.Name)
		}
	}
	for _, parent := range p.Parents {
		if !isRemoteReference(parent) {
			// "profile:" marks a profile ref (vs a bundle ref) and is not part of
			// the name.
			if child, err := c.loader.Load(strings.TrimPrefix(parent, "profile:")); err == nil {
				c.walk(child)
			}
			continue
		}
		c.add(parent, p.Name)
		if _, _, ok := remote.SplitBundleProfileRef(parent); !ok {
			continue
		}
		// Not loadable until its bundle is installed; see closureBundleRefs.
		if child, err := c.loader.Load(parent); err == nil {
			c.walk(child)
		}
	}
}

// add records ref's bundle base, once per bundle identity. A ref in the
// retired top-level "@profiles/" grammar — or otherwise unparseable — must
// never enter the install plan: it cannot pull, so planning it walks the user
// into a confirmed install that then fails with "unknown item type". Warn once
// and keep collecting (report the failure, continue with what works).
func (c *refCollector) add(ref, owner string) {
	base, _, _ := strings.Cut(ref, "#")
	if _, _, retired := remote.SplitRetiredProfileRef(base); retired {
		clidiag.WarnOnce("ctxloom",
			"profile %q references %s in the retired top-level @profiles/ grammar; profiles ship inside bundles now — point the parent at \"<url>@bundles/<bundle>#profiles/<name>\" (or install a bundle that ships it, which auto-rewrites the parent on load); skipping from sync",
			owner, ref)
		return
	}
	parsed, err := remote.ParseReference(base)
	if err != nil {
		clidiag.WarnOnce("ctxloom", "profile %q references invalid ref %s (%v); skipping from sync", owner, ref, err)
		return
	}
	// Two spellings of one bundle (a URL form in a project profile, the
	// canonical form in a shipped one) are one install.
	identity := base
	if key, kerr := parsed.LockKey(); kerr == nil {
		identity = string(key)
	}
	if c.seen.Has(identity) {
		return
	}
	c.seen.Add(identity)
	c.refs = append(c.refs, closureRef{ref: base, owner: owner})
}

// isRemoteReference reports whether a reference addresses something FETCHED
// from outside the project, and therefore must not be looked up as a local
// profile or bundle name.
//
// It carries no prefix list of its own. remote.IsSelfContainedRef is the one
// place that knows which spellings are scheme-qualified -- its own doc says
// "THIS IS THE ONLY LIST", recorded after a second copy drifted and downgraded
// a malformed companion ref into an auto-trusted first-party name. A third copy
// here drifted the same way and in the same direction: it listed only the four
// retired prefixes, so every canonical ctxloom+<class>: ref answered "not
// remote" and was reported as a missing local profile.
//
// Self-contained is necessary but not sufficient: the local and companion
// classes are self-contained and are NOT fetched. refuri.Parts
// .IsExternal is the vocabulary-driven answer to which classes address a
// repository, so a class added to refuri.Classes without being handled there
// fails that package's exhaustiveness test rather than being misread here.
func isRemoteReference(ref string) bool {
	if !remote.IsSelfContainedRef(ref) {
		return false // a bare name is first-party local
	}
	if refuri.HasScheme(ref) {
		p, err := refuri.Parse(ref)
		return err == nil && p.IsExternal()
	}
	// A scheme-qualified spelling outside the canonical family: remote unless
	// it names one of the two ctxloom: source tokens, neither of which fetches.
	return !strings.HasPrefix(ref, remote.LocalSource) &&
		!strings.HasPrefix(ref, remote.CompanionSource)
}

// syncItem syncs a single item and returns the result.
func syncItem(ctx context.Context, puller Puller, ref string, itemType remote.ItemType, baseDir string, force bool, bundles remote.BundleByteSource, downgrades downgradeSet) SyncItem {
	item := SyncItem{
		Reference: ref,
		Type:      string(itemType),
	}

	// Validate the reference before pulling.
	if _, err := remote.ParseReference(ref); err != nil {
		item.Status = "failed"
		item.cause = report.Errorf(remedyInvalidReference, "invalid reference: %w", err)
		item.Error = item.cause.Error()
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
	if !force && isInstalled(ctx, ref, baseDir, bundles) {
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
		Force:          true,
		Reresolve:      force,
		ItemType:       itemType,
		Stdout:         os.Stderr,
		AllowDowngrade: downgrades.allowsRef(ref),
	}

	result, err := puller.Pull(ctx, ref, opts)
	if err != nil {
		item.Status = "failed"
		item.Error = err.Error()
		item.cause = err
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

// CheckMissingDependencies checks which remote dependencies are not installed:
// the bundles of the same closure `deps pull` installs (closureBundleRefs), so
// the startup probe never asks for a sync that would install nothing.
func CheckMissingDependencies(ctx context.Context, cfg *config.Config, req CheckMissingDependenciesRequest) (*CheckMissingDependenciesResult, error) {
	// Installed-ness is probed through the read path, plus — once the layout
	// MATERIALIZES a bundle — the presence of what it materialized. The base
	// dir is what lets isInstalled ask that second question; empty means "clone
	// readability is the whole answer", which is exactly right for a document
	// layout that writes nothing to disk.
	missingBaseDir := ""
	if appPaths := cfg.GetAppPaths(); len(appPaths) > 0 {
		missingBaseDir = appPaths[0]
	}
	bundleReader := req.BundleReader
	if bundleReader == nil {
		bundleReader = NewBundleReaderForConfig(cfg)
	}
	var missing []MissingDependency
	for _, r := range closureBundleRefs(cfg, req.Profiles) {
		if !isInstalled(ctx, r.ref, missingBaseDir, bundleReader) {
			missing = append(missing, MissingDependency{Reference: r.ref, Type: "bundle", Profile: r.owner})
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
func isInstalled(ctx context.Context, ref, baseDir string, bundles remote.BundleByteSource) bool {
	parsedRef, err := remote.ParseReference(ref)
	if err != nil {
		return false
	}
	if bundles == nil {
		return false
	}
	key, kerr := parsedRef.LockKey()
	if kerr != nil {
		return false
	}
	if _, rerr := bundles.ReadBundleBytes(ctx, key); rerr != nil {
		return false
	}
	// READABLE IS NOT INSTALLED once a layout MATERIALIZES.
	//
	// The byte read above proves the bytes are reachable in the CLONE. That was
	// the whole of installed-ness in the reference-only model, where nothing
	// lived on disk. A tree layout breaks that equivalence: consumers read a
	// tree from the git worktree Reference.LocalTreePath names, and a skill
	// needs a real directory there, so a bundle can be perfectly readable from
	// the clone and still be unusable.
	//
	// Answering the clone question here is what made a format change
	// un-installable: sync skipped every bundle whose pin had not moved, the
	// tree was never written, and the resulting error told the user to run the
	// very pull that was refusing. Measured at the flip: 16 materialized trees
	// loaded, 25 unmaterialized ones failed, and pull called all 42 "skipped".
	if baseDir != "" {
		tree, terr := parsedRef.LocalTreePath(baseDir)
		if terr != nil {
			return false
		}
		if _, serr := os.Stat(tree); serr != nil {
			return false
		}
	}
	return true
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
	refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(collectRemoteReferences(cfg, nil)))
}

// SyncOnStartup is a convenience function that runs sync with sensible defaults.
// This is meant to be called during MCP server initialization or CLI startup.
func SyncOnStartup(ctx context.Context, app *App) (*SyncDependenciesResult, error) {
	cfg, err := app.Config(ctx)
	if err != nil {
		return nil, err
	}
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
	return SyncDependencies(ctx, app, SyncDependenciesRequest{
		Force:      false, // Don't overwrite existing
		Lock:       true,  // Update lockfile
		ApplyHooks: true,  // Apply hooks
	})
}
