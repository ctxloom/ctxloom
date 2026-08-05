package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// UpgradeResult is what one `remote upgrade` round did. It is a struct rather
// than a tuple because the three facts are not independent: a caller that reads
// Advanced without reading Refused prints "Everything is up to date." over a
// refused advance, which is the exact silence this feature exists to prevent.
type UpgradeResult struct {
	// Advanced counts the entries whose SHA actually moved.
	Advanced int
	// Incomplete reports that part of the dependency closure could not be
	// reached this round, so Advanced==0 does not mean "everything checked out".
	Incomplete bool
	// Refused lists the pins that were NOT moved because the content at the
	// proposed commit failed publisher verification. Non-empty means the human
	// must be told: the lockfile deliberately did not change.
	Refused []RefusedAdvance
}

// UpgradeDependencies re-resolves the project's dependency closure to the newest
// commit each manifest constraint allows and writes the advances straight to the
// active lock — the manifest is never rewritten. A held entry (Pinned) stays
// frozen and never advances. A hash conflict in the proposed closure is a hard
// error; nothing is written.
//
// There is no review gate here: the lockfile is pure dependency pinning.
// Whether any newly-pinned content ever reaches the
// agent is decided per item at exposure by the content-hash trust gate
// (EffectiveTrust) — changed content from an untrusted source re-hashes to
// pending and is withheld until `ctxloom review` accepts it.
//
// ONE ADVANCE IS REFUSED OUTRIGHT, and it is the one exposure cannot rescue:
// content whose publisher signature does not verify over its own bytes. That
// content is withheld as TAMPERED and is deliberately not reviewable, so moving
// the pin past the last commit that DID verify leaves the consumer with
// nothing — the new copy refused, the old copy unreachable. Such an entry keeps
// its existing lockfile values verbatim and is reported in
// UpgradeResult.Refused, which the caller must tell the human about (a silent
// non-advance is indistinguishable from "already up to date"). See
// verifyAdvance for the exact rule and why unsigned content is not covered by
// it.
//
// UpgradeResult.Incomplete is true when part of the dependency closure could
// not be reached this round: the caller must not report "everything is up to
// date" on that basis alone — Advanced counts only what WAS resolved, and an
// incomplete closure means part of the project was never actually checked
// against upstream.
func UpgradeDependencies(ctx context.Context, cfg *config.Config) (UpgradeResult, error) {
	loader := profileLoader(cfg)
	// The closure roots must match FlattenDependencies' canonical set (inline
	// config.yaml definitions, directory profiles, and config-default remote
	// profiles). A narrower set omits deps rooted in inline/config-default
	// profiles, and the wholesale Save(newActive) below would then erase their
	// active lock entries.
	roots, rootsUnexpanded := closureRoots(cfg, loader)
	var result UpgradeResult

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
		return result, err
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
		return result, ConflictError(conflicts)
	}
	// closureRoots' OWN failures (a root that could not load) used
	// to be invisible to the preserve-existing-entries guard below — only
	// the walker's internal unexpanded set fed it. Merge both.
	unexpanded = append(unexpanded, rootsUnexpanded...)
	result.Incomplete = len(unexpanded) > 0
	incomplete := result.Incomplete

	newActive := &remote.Lockfile{Version: 1, Bundles: map[string]remote.LockEntry{}}
	for _, p := range proposed {
		cur, has := active.GetEntry(p.Type, p.Identity)
		// A held entry never advances — carry its current pin forward unchanged.
		if has && cur.Pinned {
			newActive.AddEntry(p.Type, p.Identity, cur)
			continue
		}
		// A REAL advance — an entry that already exists and would move to a
		// different commit — must land on content whose publisher signature
		// verifies. It is checked only here, and only for a move: a FIRST pin
		// has no last-verified value to keep, so there is nothing to refuse
		// back to, and holding one back would simply install nothing while the
		// exposure gate would have withheld it anyway with a reason.
		if has && cur.SHA != p.Hash {
			if detail, refuse := verifyAdvance(ctx, cfg, factory, auth, p); refuse {
				newActive.AddEntry(p.Type, p.Identity, cur)
				result.Refused = append(result.Refused, RefusedAdvance{
					Identity:    p.Identity,
					KeptSHA:     cur.SHA,
					ProposedSHA: p.Hash,
					Detail:      detail,
				})
				continue
			}
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
			result.Advanced++
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
		return result, serr
	}

	// Persist this round's refusals AFTER the lockfile write, never before: a
	// record says "the pin for X is being KEPT at <sha>", and a record written
	// ahead of a Save that then failed would claim a pin the lockfile does not
	// hold. Writing second means the record can only ever describe state that
	// is already on disk.
	//
	// A failed write does NOT fail the upgrade. The lockfile — the thing the
	// user asked to change — is correct and saved, and the caller reports every
	// refusal on stdout regardless; losing the durable copy costs the
	// after-the-fact `doctor` advisory and nothing else. Warned rather than
	// swallowed, because a silently missing record is how the advisory would
	// quietly stop existing.
	if rerr := saveRefusedAdvances(cfg, result.Refused); rerr != nil {
		clidiag.Warn("ctxloom", "could not record this upgrade's refusal(s) for later inspection (`ctxloom doctor` will not report them): %v", rerr)
	}
	return result, nil
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
