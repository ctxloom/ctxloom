package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// PinnedRef is one resolved dependency in a flattened closure: a manifest
// reference whose version constraint has been resolved to a concrete commit.
type PinnedRef struct {
	Identity   string              // canonical ref without version: "<url>@<kind>/<path>"
	Hash       string              // the commit the constraint resolved to
	URL        string              // repo URL
	Type       remote.ItemType     // bundle or profile
	Constraint string              // the manifest's version expression (range/branch/tag/sha/empty)
	Version    string              // the concrete tag a semver constraint chose, else empty
	Kind       remote.SelectorKind // classified selector kind (sha/tag/version/branch)
}

// DependencyConflict reports a single item referenced at two or more differing
// hashes within a profile closure — the error the lock surfaces immediately.
type DependencyConflict struct {
	Item   string   // canonical identity ("<url>@<kind>/<path>")
	Hashes []string // the differing hashes, sorted
}

// FlattenDependencies walks the transitive closure of the project's local
// profiles, following hash-pinned references (remote profiles are read straight
// from their clone at the ref's hash — no lockfile needed, since a hash-pinned
// ref is self-describing). It returns every distinct remote item keyed by its
// hashless identity, plus any conflict where the same identity appears at more
// than one hash. Local (ctxloom:local) refs carry no hash and are not tracked.
//
// profileNames selects the roots; empty means every local profile — inline
// config.yaml definitions plus directory profiles on disk, plus any remote
// refs in the configured default profiles — the same root set sync's
// collectRemoteReferences walks (so nothing sync installs is erased by
// the post-sync lock rebuild).
//
// The third return lists remote parent profiles that could NOT be expanded
// (fetch/parse failure) — the closure is INCOMPLETE when it is non-empty, and
// a caller about to rewrite the lockfile wholesale must preserve existing
// entries instead of dropping the unexpanded subtrees (a transient fetch
// failure must never erase healthy lock entries).
func FlattenDependencies(ctx context.Context, cfg *config.Config, profileNames []string) ([]PinnedRef, []DependencyConflict, []string) {
	loader := profileLoader(cfg)
	var roots []*profiles.Profile
	var rootsUnexpanded []string
	if len(profileNames) == 0 {
		roots, rootsUnexpanded = closureRoots(cfg, loader)
	} else {
		roots, rootsUnexpanded = namedRoots(cfg, loader, profileNames)
	}
	pins, conflicts, unexpanded := flattenProfileRoots(ctx, cfg, loader, roots)
	// Merge root-build failures (a profile/directory-listing that
	// couldn't be loaded, discovered BEFORE the walker even exists) into the
	// same unexpanded-set contract the walker's own failures use, so every
	// caller of FlattenDependencies gets ONE complete signal instead of two
	// independent, easy-to-miss ones.
	return pins, conflicts, append(unexpanded, rootsUnexpanded...)
}

// namedRoots resolves profile names to in-memory root profiles. Inline
// config.yaml definitions win over a same-named directory profile, matching
// sync's collectProfileReferences resolution order; unknown names are skipped.
func namedRoots(cfg *config.Config, loader *profiles.Loader, names []string) ([]*profiles.Profile, []string) {
	var roots []*profiles.Profile
	var unexpanded []string
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if def, ok := cfg.GetProfileDefinitions()[name]; ok {
			roots = append(roots, &profiles.Profile{Name: name, Bundles: def.Bundles, Parents: def.Parents})
			continue
		}
		p, err := loader.Load(name)
		if err != nil {
			// A root that will not load NARROWS the closure, and a wholesale
			// lock rewrite built from a narrowed closure drops the entries only
			// that root reached. Save refuses the all-or-nothing case
			// (remote.ErrLockfileWouldErase); a PARTIAL narrowing like this one
			// still gets through, so it must at least be visible.
			clidiag.Warn("ctxloom", "profile %q could not be loaded (%v); dependencies reached only through it are missing from the closure", name, err)
			// The warning alone did not feed the `unexpanded` protection
			// FlattenDependencies' caller relies on (UpgradeDependencies) to
			// preserve existing lockfile entries under a narrowed closure — a
			// root failing to load here was narrowing the closure with NOTHING
			// recorded to stop the wholesale rewrite from erasing what it owned.
			unexpanded = append(unexpanded, name)
			continue
		}
		roots = append(roots, p)
	}
	return roots, unexpanded
}

// closureRoots builds the canonical root set for the WHOLE project closure:
// every inline config.yaml definition, every directory profile on disk, plus a
// synthetic root carrying the remote refs named in the configured default
// profiles (the init-seeded default, or home-config defaults — closure roots
// even though no local profile names them; the walker resolves remote parents
// from the clone cache directly, so this works on the very first lock before any
// lockfile entry exists). Lock AND upgrade must share this set: a wholesale lock
// rewrite built from a narrower set (e.g. directory profiles only) silently
// erases every entry rooted only in an inline or config-default profile.
func closureRoots(cfg *config.Config, loader *profiles.Loader) ([]*profiles.Profile, []string) {
	var names []string
	for name := range cfg.GetProfileDefinitions() {
		names = append(names, name)
	}
	ps, err := loader.List()
	var unexpanded []string
	if err != nil {
		// Losing the whole directory-profile listing is the same hazard as
		// losing a single root, one order of magnitude larger: every directory
		// profile drops out of the closure at once. Never silent.
		clidiag.Warn("ctxloom", "could not list directory profiles (%v); every dependency rooted only in one is missing from the closure", err)
		// As above, feed the unexpanded-set protection so a wholesale
		// lockfile rewrite built from this narrowed closure preserves existing
		// entries instead of silently erasing every directory-profile-rooted one.
		unexpanded = append(unexpanded, "<directory-profiles>")
	}
	for _, p := range ps {
		names = append(names, p.Name)
	}
	roots, rootsUnexpanded := namedRoots(cfg, loader, names)
	unexpanded = append(unexpanded, rootsUnexpanded...)
	if root := configDefaultsRoot(cfg); root != nil {
		roots = append(roots, root)
	}
	return roots, unexpanded
}

// configDefaultsRoot returns a synthetic root profile whose parents are the
// remote refs named in the default agent's composed profiles, or nil when there
// are none. See closureRoots.
func configDefaultsRoot(cfg *config.Config) *profiles.Profile {
	defaults := cfg.DefaultAgentProfiles()
	var remoteDefaults []string
	for _, name := range defaults {
		if isRemoteReference(name) {
			remoteDefaults = append(remoteDefaults, name)
		}
	}
	if len(remoteDefaults) == 0 {
		return nil
	}
	return &profiles.Profile{Name: "<config-defaults>", Parents: remoteDefaults}
}

// flattenProfileRoots flattens the closure of the given in-memory root
// profiles, anchored to the active lockfile (a held or unchanged-constraint
// entry is carried forward; a resolution failure falls back to its last
// known SHA rather than dropping the item). The third return is the
// unexpanded-parent set (see FlattenDependencies). Its own single caller is
// FlattenDependencies — upgrade.go's re-resolve path calls flattenRootsWith
// directly instead, since it needs reResolve=true rather than this
// lock-mode resolver.
func flattenProfileRoots(ctx context.Context, cfg *config.Config, loader *profiles.Loader, roots []*profiles.Profile) ([]PinnedRef, []DependencyConflict, []string) {
	factory := remote.FetcherFactory(NewCachedFetcherFactory(cfg))
	auth := remote.LoadAuth(getBaseDir(cfg))
	// The active lock anchors resolution: a held or unchanged-constraint entry is
	// carried forward (stability), and a resolution failure falls back to its last
	// known SHA rather than dropping the item. Lock mode (reResolve=false).
	//
	// The error is dropped BY DECISION, not by oversight, and this is the one
	// place that decision is recorded. FlattenDependencies (this function's
	// only caller) builds its profile loader first, and that seeds remote
	// bundles from this same lockfile — config.loadRemoteBundleSeed reports an
	// unreadable one fatal-class, naming the file and the fix, before this load
	// is ever reached. Repeating it here would say the same thing twice, and
	// this function cannot do anything better with it: FlattenDependencies
	// returns no error, and an anchorless rebuild is barred from being
	// PERSISTED downstream by Save's unreadable-file refusal rather
	// than by anything decidable here. A nil lockfile resolves as no anchor,
	// which is exactly the empty-lock behaviour of a first-ever lock.
	active, _ := remote.NewLockfileManager(getBaseDir(cfg)).Load()
	resolve := newConstraintResolver(ctx, active, factory, auth, false)
	return flattenRootsWith(ctx, loader, factory, auth, roots, resolve)
}

// flattenRootsWith walks the closure of roots using a caller-supplied hash
// resolver, so the lock path (carry-forward) and the upgrade path (re-resolve)
// share one traversal and differ only in how each ref's constraint resolves.
// The third return is the unexpanded-parent set (see FlattenDependencies).
func flattenRootsWith(ctx context.Context, loader *profiles.Loader, factory remote.FetcherFactory, auth remote.AuthConfig, roots []*profiles.Profile, resolve func(*remote.Reference) (string, string, remote.SelectorKind, bool)) ([]PinnedRef, []DependencyConflict, []string) {
	w := &depWalker{
		ctx:         ctx,
		loader:      loader,
		factory:     factory,
		auth:        auth,
		resolveHash: resolve,
		pins:        map[string]PinnedRef{},
		hashes:      map[string]map[string]struct{}{},
		visited:     map[string]struct{}{},
		unexpanded:  map[string]struct{}{},
	}
	for _, p := range roots {
		// A local root resolves its short sibling refs to ctxloom:local.
		w.walkProfile(p, remote.LocalSource, "")
	}
	return w.result()
}

type depWalker struct {
	ctx     context.Context
	loader  *profiles.Loader
	factory remote.FetcherFactory
	auth    remote.AuthConfig

	// resolveHash resolves a remote ref's version constraint to a concrete
	// commit (and the tag it chose, if any). ok=false means unresolvable — the
	// ref is skipped, never pinned at an empty hash. Both production
	// constructors (FlattenProfileRoots, upgrade.go) always set this; unit
	// tests that need "treat the ref's version expression as already
	// concrete" pass an identity resolver explicitly rather than relying on a
	// nil-means-identity production fallback.
	resolveHash func(ref *remote.Reference) (hash, version string, kind remote.SelectorKind, ok bool)

	pins    map[string]PinnedRef           // identity -> first-seen pin
	hashes  map[string]map[string]struct{} // identity -> set of hashes (for conflict detection)
	visited map[string]struct{}            // identity@hash, recursion guard

	// unexpanded records the identities of remote parent profiles whose content
	// could not be read or parsed during the walk: their subtrees are MISSING
	// from pins, so the closure is incomplete. Callers that rebuild the lockfile
	// wholesale consult this to avoid erasing entries under a failed subtree.
	// Initialised with the other three maps at construction — every one of this
	// walker's maps is owned by whoever builds it, so no method has to decide
	// whether it exists yet.
	unexpanded map[string]struct{}
}

// markUnexpanded records identity as a parent (local OR remote) whose
// subtree could not be walked, and emits the project-standard warning.
func (w *depWalker) markUnexpanded(identity string, cause error) {
	w.unexpanded[identity] = struct{}{}
	clidiag.Warn("ctxloom", "could not expand remote parent profile %s: %v (its dependencies are preserved from the existing lockfile)", identity, cause)
}

// resolvedHash resolves ref via the injected resolver.
func (w *depWalker) resolvedHash(ref *remote.Reference) (hash, version string, kind remote.SelectorKind, ok bool) {
	return w.resolveHash(ref)
}

// walkProfile resolves a profile's short refs against (sourceURL, sourceHash),
// then records each bundle and recurses into each parent.
func (w *depWalker) walkProfile(p *profiles.Profile, sourceURL, sourceHash string) {
	// Copy so resolution does not mutate a cached/seeded profile.
	cp := *p
	cp.Bundles = append([]string(nil), p.Bundles...)
	cp.Parents = append([]string(nil), p.Parents...)
	cp.ResolveShortRefs(sourceURL, sourceHash)

	for _, b := range cp.Bundles {
		w.record(b, remote.ItemTypeBundle)
	}
	for _, parent := range cp.Parents {
		w.recurseParent(parent)
	}
}

// record parses a ref, resolves its version constraint to a concrete commit,
// tracks its identity→hash for conflict detection, and returns the parsed
// canonical reference. It returns nil for local/unparseable refs and for a
// remote ref whose constraint could not be resolved (skipped, not pinned empty).
func (w *depWalker) record(refStr string, kind remote.ItemType) *remote.Reference {
	ref, err := remote.ParseReference(refStr)
	if err != nil || ref.IsLocal {
		return nil // unparseable or ctxloom:local — not a remote pin
	}
	if !ref.IsCanonical() {
		return nil
	}
	hash, version, selKind, ok := w.resolvedHash(ref)
	if !ok {
		return nil // unresolvable — skip rather than pin an empty hash
	}
	identity := ref.LockKey()
	if w.hashes[identity] == nil {
		w.hashes[identity] = map[string]struct{}{}
	}
	w.hashes[identity][hash] = struct{}{}
	if _, seen := w.pins[identity]; !seen {
		w.pins[identity] = PinnedRef{
			Identity:   identity,
			Hash:       hash,
			URL:        ref.URL,
			Type:       kind,
			Constraint: ref.ContentVersion,
			Version:    version,
			Kind:       selKind,
		}
	}
	return ref
}

// recurseParent records a parent reference and, for a remote parent, reads it
// from its clone at the pinned hash and walks its own dependencies.
func (w *depWalker) recurseParent(parentRef string) {
	// Local parent (ctxloom:local) — recurse via the loader.
	ref, err := remote.ParseReference(parentRef)
	if err != nil {
		// A malformed parent ref used to lose its whole subtree with
		// NO diagnostic at all — worse than the remote-fetch failures below,
		// which already warn-and-record. Same hazard: a wholesale lockfile
		// rewrite built from this narrowed closure would erase entries this
		// subtree owns, with nothing telling the caller why.
		w.markUnexpanded(parentRef, fmt.Errorf("parse parent ref: %w", err))
		return
	}
	if ref.IsLocal {
		if child, lerr := w.loader.Load(ref.Path); lerr == nil {
			w.walkProfile(child, remote.LocalSource, "")
		} else {
			// An unreadable LOCAL parent profile is the same
			// "subtree missing from pins" hazard as an unreadable remote one
			// (lines below) — it was silently skipped instead of recorded.
			w.markUnexpanded(ref.Path, lerr)
		}
		return
	}

	// The only remote profile parents are bundle profiles
	// (<url>@bundles/x#profiles/y) — top-level @profiles/ distribution was
	// retired. Pin the underlying bundle, then read the named profile OUT of the
	// bundle and walk ITS closure (its composed bundles + parents).
	bundleRef, profName, ok := remote.SplitBundleProfileRef(parentRef)
	if !ok {
		// Not a bundle-profile ref — a retired top-level @profiles/ parent (or a
		// malformed ref). No longer distributable; its subtree is gone.
		// This used to be silent — record it so a caller rebuilding
		// the lockfile wholesale doesn't erase entries this subtree owned.
		w.markUnexpanded(parentRef, errors.New("not a bundle-profile reference (<url>@bundles/x#profiles/y) — top-level @profiles/ parents are retired"))
		return
	}
	w.recurseBundleProfile(bundleRef, profName)
}

// recurseBundleProfile pins a bundle-profile parent's underlying BUNDLE, then
// reads the named profile out of that bundle at the resolved commit and walks
// its own closure. Split from recurseParent so the local/remote dispatch above
// reads as the two-way choice it is, with the remote half's fetch-parse-lookup
// chain — four distinct ways to end up with an unexpanded subtree — kept
// whole and by itself.
func (w *depWalker) recurseBundleProfile(bundleRef, profName string) {
	rec := w.record(bundleRef, remote.ItemTypeBundle)
	if rec == nil {
		return
	}
	// Recurse at the RESOLVED commit, not the (possibly symbolic) constraint —
	// the bundle's content and its profile's transitive refs are read at it.
	hash, _, _, hok := w.resolvedHash(rec)
	if !hok {
		return
	}
	guard := rec.LockKey() + remote.ProfileSelector + profName + "@" + hash
	if _, seen := w.visited[guard]; seen {
		return
	}
	w.visited[guard] = struct{}{}

	data, ferr := remote.FetchRefBytes(w.ctx, w.factory, w.auth, rec, hash)
	if ferr != nil {
		// Fault tolerant: a parent we can't read just isn't expanded — but the
		// closure is now INCOMPLETE, so warn and record it; a silent skip here
		// let a transient fetch failure permanently erase the subtree's lock
		// entries on the next wholesale rewrite.
		w.markUnexpanded(rec.LockKey(), ferr)
		return
	}
	b, berr := bundles.ParseBundle(data)
	if berr != nil {
		w.markUnexpanded(rec.LockKey(), berr)
		return
	}
	child, found := b.Profiles[profName]
	if !found {
		w.markUnexpanded(rec.LockKey(), fmt.Errorf("bundle has no profile %q", profName))
		return
	}
	w.walkProfile(&child, rec.URL, hash)
}

func (w *depWalker) result() ([]PinnedRef, []DependencyConflict, []string) {
	pins := make([]PinnedRef, 0, len(w.pins))
	for _, p := range w.pins {
		pins = append(pins, p)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Identity < pins[j].Identity })

	var conflicts []DependencyConflict
	for identity, hs := range w.hashes {
		if len(hs) < 2 {
			continue
		}
		list := make([]string, 0, len(hs))
		for h := range hs {
			list = append(list, h)
		}
		sort.Strings(list)
		conflicts = append(conflicts, DependencyConflict{Item: identity, Hashes: list})
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Item < conflicts[j].Item })

	var unexpanded []string
	for identity := range w.unexpanded {
		unexpanded = append(unexpanded, identity)
	}
	sort.Strings(unexpanded)
	return pins, conflicts, unexpanded
}

// ConflictError renders dependency conflicts as a single error suitable for
// surfacing immediately from lock/install. Returns nil when there are none.
func ConflictError(conflicts []DependencyConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("dependency hash conflict — the same item is referenced at differing commits:\n")
	for _, c := range conflicts {
		shortened := make([]string, len(c.Hashes))
		for i, h := range c.Hashes {
			shortened[i] = gitutil.ShortSHA(h)
		}
		b.WriteString("  ")
		b.WriteString(c.Item)
		b.WriteString(" @ {")
		b.WriteString(strings.Join(shortened, ", "))
		b.WriteString("}\n")
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}
