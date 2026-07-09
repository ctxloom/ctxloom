package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// UpgradeDependencies re-resolves the project's dependency closure to the newest
// commit each manifest constraint allows and writes the advances straight to the
// active lock — the manifest is never rewritten. A held entry (Pinned) stays
// frozen and never advances. A hash conflict in the proposed closure is a hard
// error; nothing is written.
//
// There is no review gate here: the lockfile is pure dependency pinning
// (trust-simplify slice 3). Whether any newly-pinned content ever reaches the
// agent is decided per item at exposure by the content-hash trust gate
// (EffectiveTrust) — changed content from an untrusted source re-hashes to
// pending and is withheld until `ctxloom review` accepts it. Returns the number
// of entries whose SHA advanced.
func UpgradeDependencies(ctx context.Context, cfg *config.Config) (int, error) {
	loader := profileLoader(cfg)
	// The closure roots must match FlattenDependencies' canonical set (inline
	// config.yaml definitions, directory profiles, and config-default remote
	// profiles). A narrower set omits deps rooted in inline/config-default
	// profiles, and the wholesale Save(newActive) below would then erase their
	// active lock entries.
	roots := closureRoots(cfg, loader)

	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)
	factory := remote.FetcherFactory(newCachedFetcherFactory(cfg))
	active, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		return 0, err
	}

	// Advance every referenced clone to live HEAD (and fetch tags) so resolution
	// sees the newest commit each constraint permits. The direct refs alone miss
	// repos reached only through transitive parents; union in every repo URL the
	// active lock already records so the whole known closure refreshes.
	refreshRepoCaches(ctx, newRepoCache(cfg), unionLockedRepoURLs(directRepoURLs(roots), active))

	// Re-resolve the whole closure (upgrade mode): every unheld ref advances to
	// the newest commit its constraint allows; held entries stay put. Conflicts
	// abort before anything is written.
	resolve := newConstraintResolver(ctx, active, factory, auth, true)
	proposed, conflicts, unexpanded := flattenRootsWith(ctx, loader, factory, auth, roots, resolve)
	if len(conflicts) > 0 {
		return 0, ConflictError(conflicts)
	}

	newActive := emptyLockfile()
	advanced := 0
	for _, p := range proposed {
		cur, has := active.GetEntry(p.Type, p.Identity)
		// A held entry never advances — carry its current pin forward unchanged.
		if has && cur.Pinned {
			newActive.AddEntry(p.Type, p.Identity, cur)
			continue
		}
		entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version, Kind: p.Kind}
		newActive.AddEntry(p.Type, p.Identity, entry)
		if !has || cur.SHA != p.Hash {
			advanced++
		}
	}

	// An INCOMPLETE closure (a remote parent profile could not be expanded) must
	// not erase healthy entries: carry forward every active entry the proposed
	// closure no longer reaches, so the wholesale Save(newActive) below cannot
	// lose lock state to a transient fetch failure. The unexpanded subtrees'
	// entries simply don't advance this round.
	if len(unexpanded) > 0 {
		preserved := 0
		for _, e := range active.AllEntries() {
			if _, ok := newActive.GetEntry(e.Type, e.Ref); !ok {
				newActive.AddEntry(e.Type, e.Ref, e.Entry)
				preserved++
			}
		}
		if preserved > 0 {
			clidiag.Warn("ctxloom", "dependency closure is incomplete (%d parent profile(s) unreachable); preserving %d existing lockfile entry(ies)", len(unexpanded), preserved)
		}
	}

	if serr := remote.NewLockfileManager(baseDir).Save(newActive); serr != nil {
		return advanced, serr
	}
	return advanced, nil
}

// directRepoURLs returns the unique repo URLs of the direct remote refs across
// the given profiles (for the per-URL clone refresh).
func directRepoURLs(profs []*profiles.Profile) []string {
	seen := map[string]struct{}{}
	var urls []string
	add := func(refs []string) {
		for _, r := range refs {
			base, _ := remote.SplitItemPath(r)
			if parsed, err := remote.ParseReference(base); err == nil && parsed.IsCanonical() {
				if _, dup := seen[parsed.URL]; !dup {
					seen[parsed.URL] = struct{}{}
					urls = append(urls, parsed.URL)
				}
			}
		}
	}
	for _, p := range profs {
		add(p.Bundles)
		add(p.Parents)
	}
	return urls
}

// unionLockedRepoURLs appends every repo URL recorded in the lock's entries to
// urls (dedup'd), covering repos reached only through transitive parent
// profiles — directRepoURLs sees only the roots' direct refs.
func unionLockedRepoURLs(urls []string, lock *remote.Lockfile) []string {
	if lock == nil {
		return urls
	}
	seen := map[string]struct{}{}
	for _, u := range urls {
		seen[u] = struct{}{}
	}
	for _, e := range lock.AllEntries() {
		if e.Entry.URL == "" {
			continue
		}
		if _, dup := seen[e.Entry.URL]; dup {
			continue
		}
		seen[e.Entry.URL] = struct{}{}
		urls = append(urls, e.Entry.URL)
	}
	return urls
}

func emptyLockfile() *remote.Lockfile {
	return &remote.Lockfile{
		Version: 1,
		Bundles: map[string]remote.LockEntry{},
	}
}
