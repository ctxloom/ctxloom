package operations

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
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
	Status    string `json:"status"` // "installed", "reinstalled", "skipped", "failed"
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

// SyncDependenciesResult contains the result of syncing dependencies.
type SyncDependenciesResult struct {
	Status    string     `json:"status"`
	Synced    []SyncItem `json:"synced,omitempty"`
	Skipped   []SyncItem `json:"skipped,omitempty"`
	Failed    []SyncItem `json:"failed,omitempty"`
	Total     int        `json:"total"`
	Installed int        `json:"installed"`
	// Reinstalled counts refs re-pulled at the pin they already had (a missing
	// tree, or --force): a sync never moves an existing pin.
	Reinstalled int    `json:"reinstalled"`
	Errors      int    `json:"errors"`
	Message     string `json:"message,omitempty"`
	// Removed names the lockfile entries the post-pull lock rebuild dropped
	// because nothing the project composes reaches them any more.
	Removed []string `json:"removed,omitempty"`
	// Incomplete and Unreachable are the post-sync lock rebuild's
	// (LockDependenciesResult): part of the closure could not be reached, and
	// these items' previous lock entries were kept rather than rebuilt.
	Incomplete  bool     `json:"incomplete,omitempty"`
	Unreachable []string `json:"unreachable,omitempty"`
	// ConstraintChanges names each pin whose manifest constraint no longer
	// matches the one it was resolved from. Only `deps upgrade` moves a pin, so
	// these stay where they are until the user upgrades.
	ConstraintChanges []ConstraintChange `json:"constraint_changes,omitempty"`
	// Changes discloses each pin this sync created: everything the bundle
	// brings in, executables included. A sync never moves an existing pin.
	Changes []PinChange `json:"changes,omitempty"`
}

// ConstraintChange is a pin kept at its commit although the manifest now asks
// for something else.
type ConstraintChange struct {
	Identity string `json:"identity"`
	// Pinned is the constraint the pin's SHA was resolved from.
	Pinned string `json:"pinned"`
	// Declared is what the manifest asks for now.
	Declared string `json:"declared"`
	SHA      string `json:"sha"`
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

	// Installed-probe source (reference-only model: lockfile entry + content
	// retrievable from the clone cache, never a disk check).
	bundleReader := req.BundleReader
	if bundleReader == nil {
		bundleReader = NewBundleReaderForConfig(cfg)
	}

	result := &SyncDependenciesResult{
		Status: "completed",
	}
	lockManager := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	before, err := lockBeforeSync(req, lockManager)
	if err != nil {
		return nil, err
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
			registered, err := registeredRepos(cfg)
			if err != nil {
				return err
			}
			refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(refs), registered)
		}
		if err := syncRefs(ctx, puller, refs, remote.ItemTypeBundle, baseDir, req.Force, bundleReader, result); err != nil {
			return err
		}
		// A pull lands new pinned content: the next generation is the one that
		// holds it. Post-sync steps read
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
	result.ConstraintChanges = constraintChangesIn(cfg, req.Profiles, baseDir, fs)
	result.Changes = newPinChanges(ctx, cfg, before, lockManager)

	summarizeSync(result)
	return result, nil
}

// lockBeforeSync is the lock a sync starts from, for disclosing the pins it
// creates; nil when a Puller is injected, since the disclosure reads each new
// pin from the clone cache and a test double populates none.
func lockBeforeSync(req SyncDependenciesRequest, m *remote.LockfileManager) (*remote.Lockfile, error) {
	if req.Puller != nil {
		return nil, nil
	}
	return m.Load()
}

// newPinChanges discloses each pin the sync created since before; nothing
// when before is nil or the lock can no longer be read.
func newPinChanges(ctx context.Context, cfg *config.Config, before *remote.Lockfile, m *remote.LockfileManager) []PinChange {
	if before == nil {
		return nil
	}
	after, err := m.Load()
	if err != nil {
		return nil
	}
	return pinChanges(ctx, cfg, before, after)
}

// constraintChangesIn is constraintChanges against the active lock in baseDir.
// An unreadable lock reports nothing: the pull itself has already said why.
func constraintChangesIn(cfg *config.Config, profileNames []string, baseDir string, fs afero.Fs) []ConstraintChange {
	lock, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs)).Load()
	if err != nil {
		return nil
	}
	return constraintChanges(cfg, profileNames, lock)
}

// constraintChanges names each unheld pin in lock whose manifest constraint is
// no longer the one it was resolved from. Pull, init and startup never move an
// existing pin, so without this a constraint edit would be silently ignored
// until the next `deps upgrade`. A declared bare commit equal to the pin is
// already satisfied and is not a change.
func constraintChanges(cfg *config.Config, profileNames []string, lock *remote.Lockfile) []ConstraintChange {
	var out []ConstraintChange
	for _, r := range closureBundleRefs(cfg, profileNames) {
		ref := r.parsed
		key, err := ref.LockKey()
		if err != nil {
			continue
		}
		e, ok := lock.GetEntry(remote.ItemTypeBundle, key)
		if !ok || e.Held || e.RequestedVersion == ref.ContentVersion || ref.ContentVersion == e.SHA {
			continue
		}
		out = append(out, ConstraintChange{Identity: string(key), Pinned: e.RequestedVersion, Declared: ref.ContentVersion, SHA: e.SHA})
	}
	return out
}

// summarizeSync settles the result's status and one-line tally.
func summarizeSync(result *SyncDependenciesResult) {
	if result.Errors > 0 {
		result.Status = "completed_with_errors"
	}
	result.Message = fmt.Sprintf("Synced %d items: %d installed, %d reinstalled, %d skipped, %d failed",
		result.Total, result.Installed, result.Reinstalled, len(result.Skipped), result.Errors)
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
// A single pass is not enough. collect can only see into profiles that
// currently RESOLVE: a bundle-profile parent is readable only once its bundle
// is installed, so the bundles IT composes are invisible until the pull that
// installs its bundle. Collecting once leaves part of the graph unpinned while
// still exiting 0, forcing the user to re-run `deps pull` until it happens to
// converge.
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
	if req.Lock && result.Installed+result.Reinstalled > 0 {
		// The puller already wrote the lockfile inline during this sync, so the
		// lock step only needs to surface it — SkipSync avoids a redundant
		// second sync pass.
		lock, err := syncLockStep(ctx, cfg, LockDependenciesRequest{FS: fs})
		if err != nil {
			clidiag.Warn("ctxloom", "failed to generate lockfile after sync: %v", err)
			zap.L().Warn("failed to generate lockfile", zap.Error(err))
		} else if lock != nil {
			result.Removed = lock.Removed
			result.Incomplete, result.Unreachable = lock.Incomplete, lock.Unreachable
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
	ref    string            // bundle base: the ref with any item selector stripped
	parsed *remote.Reference // ref, parsed: a ref that does not parse is never collected
	owner  string            // name of the profile that references it
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
	loader := cfg.GetProfileLoader()
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
	c.refs = append(c.refs, closureRef{ref: base, parsed: parsed, owner: owner})
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
func syncItem(ctx context.Context, puller Puller, ref string, itemType remote.ItemType, baseDir string, force bool, bundles remote.BundleByteSource) SyncItem {
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
	if !force && isInstalled(ctx, ref, baseDir, bundles) {
		item.Status = "skipped"
		return item
	}

	result, err := puller.Pull(ctx, ref, remote.PullOptions{ItemType: itemType})
	if err != nil {
		item.Status = "failed"
		item.Error = err.Error()
		item.cause = err
		return item
	}

	item.LocalPath = result.LocalPath
	if result.Reinstalled {
		item.Status = "reinstalled"
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
	case "reinstalled":
		result.Synced = append(result.Synced, item)
		result.Reinstalled++
	case "skipped":
		result.Skipped = append(result.Skipped, item)
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
// defaults) to its live tip. Best-effort throughout: a fetch failure
// leaves the cache as-is; the probe and sync paths surface any real
// problem — an unreadable registry included: nothing is refreshed.
func refreshReferencedClones(ctx context.Context, cfg *config.Config) {
	registered, err := registeredRepos(cfg)
	if err != nil {
		return
	}
	refreshRepoCaches(ctx, NewRepoCache(cfg), syncRefURLs(collectRemoteReferences(cfg, nil)), registered)
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
			Status:            "up_to_date",
			Message:           "All dependencies are already installed",
			ConstraintChanges: constraintChangesIn(cfg, nil, ProjectAppDir(cfg), getFS(nil)),
		}, nil
	}

	// Sync missing dependencies
	return SyncDependencies(ctx, app, SyncDependenciesRequest{
		Force:      false, // Don't overwrite existing
		Lock:       true,  // Update lockfile
		ApplyHooks: true,  // Apply hooks
	})
}
