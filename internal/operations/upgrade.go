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
// UpgradeDependencies's third return, incomplete, is true when part of the
// dependency closure could not be reached this round: the caller
// must not report "everything is up to date" on that basis alone — advanced
// counts only what WAS resolved, and an incomplete closure means part of the
// project was never actually checked against upstream.
func UpgradeDependencies(ctx context.Context, cfg *config.Config) (advanced int, incomplete bool, err error) {
	loader := profileLoader(cfg)
	// The closure roots must match FlattenDependencies' canonical set (inline
	// config.yaml definitions, directory profiles, and config-default remote
	// profiles). A narrower set omits deps rooted in inline/config-default
	// profiles, and the wholesale Save(newActive) below would then erase their
	// active lock entries.
	roots, rootsUnexpanded := closureRoots(cfg, loader)

	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)
	factory := remote.FetcherFactory(NewCachedFetcherFactory(cfg))
	// Both lockfile manager constructions in
	// this function used to omit WithLockfileFS, so under an injected
	// filesystem (tests, or any future FS-scoped caller) the closure walk
	// would enumerate roots from cfg's FS while this function's own Load/Save
	// silently fell back to the real OS filesystem — reading and writing a
	// DIFFERENT lock.yaml than the one the rest of the resolution sees.
	// Contained today only by ErrLockfileWouldErase (an empty write over a
	// populated file refuses), but that guard should never be the only thing
	// standing between an FS mismatch and a wiped lockfile. Match
	// trust.go:499 and lockfile.go:74, the two call sites that already do
	// this correctly.
	lockFS := getFS(cfg.FS())
	active, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(lockFS)).Load()
	if err != nil {
		return 0, false, err
	}

	// Advance every referenced clone to live HEAD (and fetch tags) so resolution
	// sees the newest commit each constraint permits. The direct refs alone miss
	// repos reached only through transitive parents; union in every repo URL the
	// active lock already records so the whole known closure refreshes.
	refreshRepoCaches(ctx, NewRepoCache(cfg), unionLockedRepoURLs(directRepoURLs(roots), active))

	// Re-resolve the whole closure (upgrade mode): every unheld ref advances to
	// the newest commit its constraint allows; held entries stay put. Conflicts
	// abort before anything is written.
	resolve := newConstraintResolver(ctx, active, factory, auth, true)
	proposed, conflicts, unexpanded := flattenRootsWith(ctx, loader, factory, auth, roots, resolve)
	if len(conflicts) > 0 {
		return 0, false, ConflictError(conflicts)
	}
	// closureRoots' OWN failures (a root that could not load) used
	// to be invisible to the preserve-existing-entries guard below — only
	// the walker's internal unexpanded set fed it. Merge both.
	unexpanded = append(unexpanded, rootsUnexpanded...)
	incomplete = len(unexpanded) > 0

	newActive := &remote.Lockfile{Version: 1, Bundles: map[string]remote.LockEntry{}}
	advanced = 0
	for _, p := range proposed {
		cur, has := active.GetEntry(p.Type, p.Identity)
		// A held entry never advances — carry its current pin forward unchanged.
		if has && cur.Pinned {
			newActive.AddEntry(p.Type, p.Identity, cur)
			continue
		}
		entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version, Kind: p.Kind}
		// A full re-resolve is NOT a fresh retraction check — only
		// sync's installed-ref re-check (checkInstalledRetraction) or the next
		// Pull actually reads the publisher's manifest and is entitled to lift
		// a retraction. Without this, `ctxloom remote upgrade` silently
		// un-retracted every non-held bundle by building a zero-valued entry
		// here, exactly the invariant LockDependencies' prevRetracted already
		// protects on the sibling full-rebuild path (internal/operations/lockfile.go).
		if has && cur.Retracted {
			entry.Retracted = true
			entry.RetractedReason = cur.RetractedReason
		}
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
	if incomplete {
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

	if serr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(lockFS)).Save(newActive); serr != nil {
		return advanced, incomplete, serr
	}
	return advanced, incomplete, nil
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
