package operations

import (
	"context"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// PinnedRef is one resolved dependency in a flattened closure: a manifest
// reference whose version constraint has been resolved to a concrete commit.
type PinnedRef struct {
	Identity   string          // canonical ref without version: "<url>@<kind>/<path>"
	Hash       string          // the commit the constraint resolved to
	URL        string          // repo URL
	Type       remote.ItemType // bundle or profile
	Constraint string          // the manifest's version expression (range/branch/tag/sha/empty)
	Version    string          // the concrete tag a semver constraint chose, else empty
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
func FlattenDependencies(ctx context.Context, cfg *config.Config, profileNames []string) ([]PinnedRef, []DependencyConflict) {
	loader := profileLoader(cfg)
	var roots []*profiles.Profile
	if len(profileNames) == 0 {
		roots = closureRoots(cfg, loader)
	} else {
		roots = namedRoots(cfg, loader, profileNames)
	}
	return FlattenProfileRoots(ctx, cfg, loader, roots)
}

// namedRoots resolves profile names to in-memory root profiles. Inline
// config.yaml definitions win over a same-named directory profile, matching
// sync's collectProfileReferences resolution order; unknown names are skipped.
func namedRoots(cfg *config.Config, loader *profiles.Loader, names []string) []*profiles.Profile {
	var roots []*profiles.Profile
	seen := map[string]struct{}{}
	for _, name := range names {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if def, ok := cfg.Profiles.Definitions[name]; ok {
			roots = append(roots, &profiles.Profile{Name: name, Bundles: def.Bundles, Parents: def.Parents})
			continue
		}
		if p, err := loader.Load(name); err == nil {
			roots = append(roots, p)
		}
	}
	return roots
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
func closureRoots(cfg *config.Config, loader *profiles.Loader) []*profiles.Profile {
	var names []string
	for name := range cfg.Profiles.Definitions {
		names = append(names, name)
	}
	if ps, err := loader.List(); err == nil {
		for _, p := range ps {
			names = append(names, p.Name)
		}
	}
	roots := namedRoots(cfg, loader, names)
	if root := configDefaultsRoot(cfg); root != nil {
		roots = append(roots, root)
	}
	return roots
}

// configDefaultsRoot returns a synthetic root profile whose parents are the
// remote refs named in the configured default profiles, or nil when there are
// none. See closureRoots.
func configDefaultsRoot(cfg *config.Config) *profiles.Profile {
	defaults := cfg.ExplicitDefaultProfiles()
	if len(defaults) == 0 {
		defaults = homeDefaultProfiles()
	}
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

// FlattenProfileRoots flattens the closure of the given in-memory root profiles.
// Used by `update` to flatten the PROPOSED state (roots with refs re-pinned to
// HEAD) without writing the proposed profiles to disk first.
func FlattenProfileRoots(ctx context.Context, cfg *config.Config, loader *profiles.Loader, roots []*profiles.Profile) ([]PinnedRef, []DependencyConflict) {
	factory := remote.FetcherFactory(newCachedFetcherFactory(cfg))
	auth := remote.LoadAuth(getBaseDir(cfg))
	// The active lock anchors resolution: a held or unchanged-constraint entry is
	// carried forward (stability), and a resolution failure falls back to its last
	// known SHA rather than dropping the item. Lock mode (reResolve=false).
	active, _ := remote.NewLockfileManager(getBaseDir(cfg)).Load()
	resolve := newConstraintResolver(ctx, active, factory, auth, false)
	return flattenRootsWith(ctx, loader, factory, auth, roots, resolve)
}

// flattenRootsWith walks the closure of roots using a caller-supplied hash
// resolver, so the lock path (carry-forward) and the upgrade path (re-resolve)
// share one traversal and differ only in how each ref's constraint resolves.
func flattenRootsWith(ctx context.Context, loader *profiles.Loader, factory remote.FetcherFactory, auth remote.AuthConfig, roots []*profiles.Profile, resolve func(*remote.Reference) (string, string, bool)) ([]PinnedRef, []DependencyConflict) {
	w := &depWalker{
		ctx:         ctx,
		loader:      loader,
		factory:     factory,
		auth:        auth,
		resolveHash: resolve,
		pins:        map[string]PinnedRef{},
		hashes:      map[string]map[string]struct{}{},
		visited:     map[string]struct{}{},
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
	// ref is skipped, never pinned at an empty hash. Nil means "treat the ref's
	// version expression as already concrete" (the pre-resolution passthrough used
	// by closure/conflict unit tests that inject pre-pinned hashes directly).
	resolveHash func(ref *remote.Reference) (hash, version string, ok bool)

	pins    map[string]PinnedRef           // identity -> first-seen pin
	hashes  map[string]map[string]struct{} // identity -> set of hashes (for conflict detection)
	visited map[string]struct{}            // identity@hash, recursion guard
}

// resolvedHash resolves ref via the injected resolver, or — when none is set —
// passes the ref's version expression through as the hash (the pre-resolution
// contract the depgraph unit tests rely on).
func (w *depWalker) resolvedHash(ref *remote.Reference) (hash, version string, ok bool) {
	if w.resolveHash != nil {
		return w.resolveHash(ref)
	}
	return ref.ContentVersion, "", true
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
	hash, version, ok := w.resolvedHash(ref)
	if !ok {
		return nil // unresolvable — skip rather than pin an empty hash
	}
	identity := ref.CanonicalString()
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
		return
	}
	if ref.IsLocal {
		if child, lerr := w.loader.Load(ref.Path); lerr == nil {
			w.walkProfile(child, remote.LocalSource, "")
		}
		return
	}

	rec := w.record(parentRef, remote.ItemTypeProfile)
	if rec == nil {
		return
	}
	// Recurse at the RESOLVED commit, not the (possibly symbolic) constraint —
	// the parent's content and its own transitive refs are read from that commit.
	hash, _, ok := w.resolvedHash(rec)
	if !ok {
		return
	}
	guard := rec.CanonicalString() + "@" + hash
	if _, seen := w.visited[guard]; seen {
		return
	}
	w.visited[guard] = struct{}{}

	data, ferr := remote.FetchRefBytes(w.ctx, w.factory, w.auth, rec, hash)
	if ferr != nil {
		return // fault tolerant: a parent we can't read just isn't expanded
	}
	child, perr := profiles.ParseProfile(data)
	if perr != nil {
		return
	}
	w.walkProfile(child, rec.URL, hash)
}

func (w *depWalker) result() ([]PinnedRef, []DependencyConflict) {
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
	return pins, conflicts
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
		b.WriteString("  ")
		b.WriteString(c.Item)
		b.WriteString(" @ {")
		b.WriteString(strings.Join(shortHashes(c.Hashes), ", "))
		b.WriteString("}\n")
	}
	return &dependencyConflictError{msg: strings.TrimRight(b.String(), "\n")}
}

func shortHashes(hashes []string) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = shortSHA(h)
	}
	return out
}

type dependencyConflictError struct{ msg string }

func (e *dependencyConflictError) Error() string { return e.msg }
