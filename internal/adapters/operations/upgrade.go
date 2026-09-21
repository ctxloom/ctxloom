package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// UpgradeResult is what one `deps upgrade` round did. It is a struct rather
// than a tuple because the three facts are not independent: a caller that reads
// Advanced without reading Refused prints "Everything is up to date." over a
// refused advance, which is the exact silence this feature exists to prevent.
type UpgradeResult struct {
	// Advanced counts the entries whose SHA actually moved.
	Advanced int `json:"advanced"`
	// Incomplete reports that part of the dependency closure could not be
	// reached this round, so Advanced==0 does not mean "everything checked out".
	Incomplete bool `json:"incomplete"`
	// NothingDeclared reports that the resolved closure was EMPTY and there was
	// no existing lock state either: nothing in reach of this run declares a
	// dependency at all. It is a different fact from "your dependencies are all
	// current", and a caller that collapses the two tells a user who ran the
	// command outside their project that everything is fine. Advanced==0 alone
	// cannot distinguish them — it counts moves among what was resolved, and
	// here nothing was resolved because nothing was asked for.
	NothingDeclared bool `json:"nothing_declared"`
	// Refused lists the pins that were NOT moved because the content at the
	// proposed commit failed publisher verification. Non-empty means the human
	// must be told: the lockfile deliberately did not change.
	Refused []RefusedAdvance `json:"refused"`
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
// UpgradeResult.NothingDeclared is true when the closure resolved to nothing
// and no lock state existed either. Such a round writes NO lockfile: a file
// that pins nothing is not a record of a successful check, and creating one
// where none existed marks a directory as a project that never was.
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

	baseDir := ProjectAppDir(cfg)
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
	proposed, conflicts, unexpanded := flattenRootsWith(ctx, loader, factory, auth, cfg.TrustRoot(), roots, resolve)
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
		if has && cur.Held {
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
		// a retraction. Without this, `ctxloom deps upgrade` silently
		// un-retracted every non-held bundle by building a zero-valued entry
		// here, exactly the invariant LockDependencies' prevRetracted already
		// protects on the sibling full-rebuild path (internal/adapters/operations/lockfile.go).
		if has && cur.Retracted {
			entry.Retracted = true
			entry.RetractedReason = cur.RetractedReason
		}
		newActive.AddEntry(p.Type, p.Identity, entry)
		if !has || cur.SHA != p.Hash {
			result.Advanced++
			// A MOVED PIN MOVES THE TREE WITH IT. The worktree is a git
			// checkout detached at a commit, so "which commit is this tree?"
			// is a question the tree itself answers — and advancing the pin
			// without advancing the checkout would leave the two disagreeing
			// while both look well-formed.
			movePinnedWorktree(ctx, cfg, p)
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

	// NOTHING DECLARED, AND NOTHING ON DISK TO PROTECT. Writing here would
	// CREATE a lockfile that pins nothing — a project marker for a project that
	// does not exist — or re-stamp LockedAt on an empty one to record a check
	// that had nothing to check. Save's own guard is the mirror image of this
	// one and deliberately does not cover it: ErrLockfileWouldErase protects
	// entries that EXIST, and by construction there are none here, so an empty
	// write is a legitimate success at that layer (a genuinely empty project
	// must still be able to lock). Only this caller knows the emptiness came
	// from resolving nothing rather than from meaning nothing, so the refusal
	// to fabricate belongs here.
	//
	// The Incomplete case skips the write for the same reason but is NOT
	// "nothing declared": something may well be declared behind the part of the
	// closure that could not be reached, and saying otherwise would be a
	// confident wrong answer rather than an honest empty one.
	nothingToRecord := newActive.IsEmpty() && active.IsEmpty()
	result.NothingDeclared = nothingToRecord && !result.Incomplete
	if !nothingToRecord {
		if serr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(lockFS)).Save(newActive); serr != nil {
			return result, serr
		}
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

// movePinnedWorktree re-checks a bundle's worktree out at the commit its pin
// just moved to.
//
// IT MOVES THE TREE RATHER THAN DELETING IT. Deleting was only ever correct if
// a pull always followed, and nothing enforced that: a read taken between the
// upgrade and the pull found the lockfile naming a bundle whose tree was gone,
// and every such read failed telling the user to run the pull they were in the
// middle of. Because git owns the checkout and the sidecars travel with the
// commit, moving the pin needs no fetch — the same call that installs a bundle
// re-installs it at the new commit.
//
// Best-effort by design: an upgrade whose lockfile work is already correct must
// not be aborted by the cache. A worktree left at the old commit is a
// divergence the next pull repairs, and a refused upgrade is one the user has
// to work around.
func movePinnedWorktree(ctx context.Context, cfg *config.Config, p PinnedRef) {
	if p.Type != remote.ItemTypeBundle {
		return // only a bundle materializes a tree
	}
	ref, err := remote.ParseReference(p.Identity)
	if err != nil || !ref.IsCanonical() {
		return
	}
	baseDir := ProjectAppDir(cfg)
	if baseDir == "" {
		return
	}
	if p.Hash == "" {
		return
	}
	install := remotetree.WorktreeInstaller(NewRepoCache(cfg))
	if _, err := install(ctx, ref.URL, p.Hash, ref.TreeRepoPath(), ref.LocalWorktreePath(baseDir)); err != nil {
		clidiag.Warn("ctxloom", "%s was upgraded to %s but its cached tree could not be moved to that commit (%v); "+
			"run `ctxloom deps pull` to re-install it", p.Identity, p.Hash, err)
	}
}
