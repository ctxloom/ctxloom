package operations

import (
	"context"
	"errors"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/release"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// UpgradeRequest is one `deps upgrade` invocation.
type UpgradeRequest struct {
	// Apply writes what the round computed (`--yes`). Without it the round is
	// a preview: it computes and reports the same result and changes nothing.
	Apply bool `json:"apply"`
	// AllowDowngrade names the refs whose signed version floor the operator
	// waives for this run (`--allow-downgrade <ref>`); every other pin is held
	// to the signed version it recorded (see verifyAdvance).
	AllowDowngrade []string `json:"allow_downgrade,omitempty"`
}

// UpgradeResult is what one `deps upgrade` round would do (a preview) or did
// (Applied). It is a struct rather than a tuple because the facts are not
// independent: a caller that reads Changes without reading Refused prints
// "Everything is up to date." over a refused advance, which is the exact
// silence this feature exists to prevent.
type UpgradeResult struct {
	// Applied reports that this round wrote what it computed.
	Applied bool `json:"applied"`
	// Changes discloses each pin that moves (or is first created), with
	// everything the move brings in, sorted by identity.
	Changes []PinChange `json:"changes"`
	// Incomplete reports that part of the dependency closure could not be
	// reached this round — a parent that did not expand, or a repository that
	// could not be fetched — so no Changes does not mean "everything checked
	// out".
	Incomplete bool `json:"incomplete"`
	// NothingDeclared reports that the resolved closure was EMPTY and there was
	// no existing lock state either: nothing in reach of this run declares a
	// dependency at all. It is a different fact from "your dependencies are all
	// current", and a caller that collapses the two tells a user who ran the
	// command outside their project that everything is fine. No Changes alone
	// cannot distinguish them — it lists moves among what was resolved, and
	// here nothing was resolved because nothing was asked for.
	NothingDeclared bool `json:"nothing_declared"`
	// Refused lists the pins that do NOT move because the content at the
	// proposed commit failed publisher verification. Non-empty means the human
	// must be told: the lockfile deliberately does not change for them.
	Refused []RefusedAdvance `json:"refused"`
	// Removed names, sorted, the lockfile entries this round drops because the
	// closure no longer reaches them. The lock is rewritten wholesale, so
	// without this a removal is indistinguishable from never having been
	// pinned.
	Removed []string `json:"removed"`
}

// UpgradeDependencies re-resolves the project's dependency closure to the newest
// commit each manifest constraint allows. It is the ONLY operation that moves
// an existing pin: pull, init and startup create first pins and keep the rest.
//
// It runs in two phases. The compute phase (planUpgrade) resolves the new lock
// in memory and discloses every pin that would move (UpgradeResult.Changes); it
// writes nothing and moves no worktree. Without req.Apply that is the whole
// round — a preview. With req.Apply the apply phase writes the lock, moves each
// moved bundle's worktree, and records the round's refusals. Apply recomputes
// rather than replaying a preview, so a tip that moved since the preview is
// what lands, and what Changes reports.
//
// The manifest is never rewritten. A held entry stays frozen and never
// advances. A hash conflict in the proposed closure is a hard error; nothing is
// written.
//
// There is no review gate here: the lockfile is pure dependency pinning.
//
// ONE ADVANCE IS REFUSED OUTRIGHT: content whose publisher signature does not
// verify over its own bytes. A reader refuses that content as TAMPERED, so
// moving the pin past the last commit that DID verify leaves the consumer with
// nothing. Such an entry keeps its existing
// lockfile values verbatim and is reported in UpgradeResult.Refused, which the
// caller must tell the human about. See verifyAdvance for the exact rule.
//
// UpgradeResult.NothingDeclared is true when the closure resolved to nothing
// and no lock state existed either. Such a round writes NO lockfile: a file
// that pins nothing is not a record of a successful check, and creating one
// where none existed marks a directory as a project that never was.
//
// UpgradeResult.Incomplete is true when part of the dependency closure could
// not be reached this round: the caller must not report "everything is up to
// date" on that basis alone.
func UpgradeDependencies(ctx context.Context, cfg *config.Config, req UpgradeRequest) (UpgradeResult, error) {
	plan, err := planUpgrade(ctx, cfg, req.AllowDowngrade)
	if err != nil {
		return UpgradeResult{}, err
	}
	result := UpgradeResult{
		Incomplete: plan.incomplete,
		Refused:    plan.refused,
		Removed:    droppedEntries(plan.active, plan.next),
		Changes:    pinChanges(ctx, cfg, plan.active, plan.next),
	}
	// Every lockfile manager here carries WithLockfileFS(cfg's FS): the closure
	// walk enumerates roots from cfg's FS, so a manager on any other
	// filesystem would read and write a DIFFERENT lock.yaml than the one the
	// rest of the resolution sees.
	lockManager := remote.NewLockfileManager(ProjectAppDir(cfg), remote.WithLockfileFS(getFS(cfg.FS())))
	record, err := checkUpgradedLock(lockManager, plan, &result)
	if err != nil || !req.Apply {
		return result, err
	}
	if record {
		if err := lockManager.Save(plan.next); err != nil {
			return result, err
		}
	}
	result.Applied = true
	// A MOVED PIN MOVES THE TREE WITH IT. The worktree is a git checkout
	// detached at a commit, so "which commit is this tree?" is a question the
	// tree itself answers — and advancing the pin without advancing the
	// checkout would leave the two disagreeing while both look well-formed.
	for _, p := range plan.moved {
		movePinnedWorktree(ctx, cfg, p)
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

// upgradePlan is the compute phase's answer: the lock the round read, the lock
// it would write, and what moving to it involves.
type upgradePlan struct {
	active, next *remote.Lockfile
	// moved is every proposed pin whose SHA moves (or is first created).
	moved      []PinnedRef
	refused    []RefusedAdvance
	incomplete bool
}

// planUpgrade is the compute phase: it resolves the new lock in memory and
// writes nothing.
func planUpgrade(ctx context.Context, cfg *config.Config, allowDowngrade []string) (*upgradePlan, error) {
	downgrades, err := newDowngradeSet(allowDowngrade)
	if err != nil {
		return nil, err
	}
	loader := cfg.GetProfileLoader()
	// The closure roots must match FlattenDependencies' canonical set (inline
	// config.yaml definitions, directory profiles, and config-default remote
	// profiles). A narrower set omits deps rooted in inline/config-default
	// profiles, and the wholesale Save(next) would then erase their active
	// lock entries.
	roots, rootsUnexpanded := closureRoots(cfg, loader)

	baseDir := ProjectAppDir(cfg)
	auth := remote.LoadAuth(baseDir)
	factory := remote.FetcherFactory(NewCachedFetcherFactory(cfg))
	active, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(getFS(cfg.FS()))).Load()
	if err != nil {
		return nil, err
	}

	proposed, unexpanded, fetchFailed, err := reResolveClosure(ctx, cfg, loader, roots, active, factory, auth)
	if err != nil {
		return nil, err
	}
	// closureRoots' OWN failures (a root that could not load) feed the
	// preserve-existing-entries guard below alongside the walker's internal
	// unexpanded set.
	unexpanded = append(unexpanded, rootsUnexpanded...)

	round := upgradeRound{
		ctx: ctx, cfg: cfg, factory: factory, auth: auth, downgrades: downgrades,
		plan: &upgradePlan{
			active: active,
			next:   &remote.Lockfile{Version: remote.LockfileVersion, Bundles: map[trust.BundleKey]remote.LockEntry{}},
			// A repository that could not be fetched was resolved from its
			// stale clone, so "nothing moves" is not "everything is current"
			// for it either. It does not widen the carry-forward below, which
			// is about subtrees the walk never reached.
			incomplete: len(unexpanded) > 0 || fetchFailed,
		},
	}
	for _, p := range proposed {
		round.decide(p)
	}

	// An INCOMPLETE closure (a remote parent profile could not be expanded) must
	// not erase healthy entries: carry forward every active entry the proposed
	// closure no longer reaches, so the wholesale Save(next) cannot lose lock
	// state to a transient fetch failure. The unexpanded subtrees' entries
	// simply don't advance this round.
	if len(unexpanded) > 0 {
		preserveUnreachedEntries(active, round.plan.next, len(unexpanded))
	}
	return round.plan, nil
}

// reResolveClosure re-resolves the closure of roots in upgrade mode: every
// unheld ref advances to the newest commit its constraint allows; held entries
// stay put. Conflicts are an error, returned before anything is written.
//
// It first advances every registered clone the closure reaches to live HEAD
// (and fetches tags) so resolution sees the newest commit each constraint
// permits. The direct refs alone miss repos reached only through transitive
// parents, so every repo URL the active lock records is refreshed too. An
// unregistered repository is neither refreshed nor resolved: the walk refuses
// it (remote.NotRegisteredError).
func reResolveClosure(ctx context.Context, cfg *config.Config, loader *profiles.Loader, roots []*profiles.Profile, active *remote.Lockfile, factory remote.FetcherFactory, auth remote.AuthConfig) (proposed []PinnedRef, unexpanded []string, fetchFailed bool, err error) {
	registered, err := registeredRepos(cfg)
	if err != nil {
		return nil, nil, false, err
	}
	fetchFailed = refreshRepoCaches(ctx, NewRepoCache(cfg), unionLockedRepoURLs(directRepoURLs(roots), active), registered)
	resolve := newConstraintResolver(ctx, active, factory, auth, true)
	proposed, conflicts, unexpanded, err := flattenRootsWith(ctx, loader, factory, auth, cfg.TrustRoot(), roots, resolve, registered)
	if err != nil {
		return nil, nil, false, err
	}
	if len(conflicts) > 0 {
		return nil, nil, false, ConflictError(conflicts)
	}
	return proposed, unexpanded, fetchFailed, nil
}

// upgradeRound is one planUpgrade pass: the plan it builds, and what it
// needs to verify each proposed advance.
type upgradeRound struct {
	ctx        context.Context
	cfg        *config.Config
	factory    remote.FetcherFactory
	auth       remote.AuthConfig
	downgrades downgradeSet
	plan       *upgradePlan
}

// decide places one proposed pin in the plan: a held entry carries forward
// unchanged, a refused advance keeps its current pin, anything else lands at
// the proposed commit (and, if it moved, is listed for the apply phase).
func (u *upgradeRound) decide(p PinnedRef) {
	cur, has := u.plan.active.GetEntry(p.Type, p.Identity)
	// A held entry never advances — carry its current pin forward unchanged.
	if has && cur.Held {
		u.plan.next.AddEntry(p.Type, p.Identity, cur)
		return
	}
	moved := !has || cur.SHA != p.Hash
	// A REAL advance — an entry that already exists and would move to a
	// different commit — must land on content whose publisher signature
	// verifies, at or above the version its last pin was signed at. A FIRST
	// pin is read too, to record its floor, but never refused: it has no
	// last-verified value to keep, so there is nothing to refuse back to,
	// and holding one back would simply install nothing while the exposure
	// gate would have withheld it anyway with a reason.
	var verified remote.Verified
	if moved {
		v, refusal := verifyAdvance(u.ctx, u.cfg, u.factory, u.auth, p, cur, u.downgrades.allows(p.Identity))
		verified = v
		if refusal != nil && has {
			u.refuse(p, cur, refusal)
			return
		}
	}
	u.plan.next.AddEntry(p.Type, p.Identity, upgradedEntry(p, cur, has, moved, verified))
	if moved {
		u.plan.moved = append(u.plan.moved, p)
	}
}

// refuse keeps cur verbatim and records why p's advance was refused.
func (u *upgradeRound) refuse(p PinnedRef, cur remote.LockEntry, refusal error) {
	u.plan.next.AddEntry(p.Type, p.Identity, cur)
	u.plan.refused = append(u.plan.refused, RefusedAdvance{
		Identity:    string(p.Identity),
		KeptSHA:     cur.SHA,
		ProposedSHA: p.Hash,
		Detail:      refusal.Error(),
		Cause:       refusalCauseOf(refusal),
	})
}

// refusalCauseOf classifies a verifyAdvance refusal. Only a withheld tree is a
// signature failure, and not even that when what withheld it is a manifest in
// the retired format: that is a format the publisher must re-sign in, not a
// signature lying about its bytes. Everything else verifyAdvance refuses on
// is a read that established nothing about the signature at all.
func refusalCauseOf(refusal error) RefusalCause {
	switch {
	case errors.Is(refusal, release.ErrRollback) || errors.Is(refusal, release.ErrSignatureDowngrade):
		return RefusalBelowFloor
	case errors.Is(refusal, bundles.ErrTreeBundleWithheld) && !errors.Is(refusal, content.ErrManifestSuperseded):
		return RefusalSignature
	default:
		return RefusalUnreadable
	}
}

// upgradedEntry is the lock entry p lands as.
func upgradedEntry(p PinnedRef, cur remote.LockEntry, has, moved bool, verified remote.Verified) remote.LockEntry {
	entry := pinnedEntry(p)
	// The floor moves only with the content it was read from: an unmoved pin
	// keeps what its last verified pin recorded, a moved one records what
	// verifyAdvance just established.
	if moved {
		entry.SignedVersion, entry.Publisher = verified.LockFields()
	} else {
		entry.SignedVersion, entry.Publisher = cur.SignedVersion, cur.Publisher
		keepFetchedAt(&entry, cur)
	}
	// A full re-resolve is NOT a fresh retraction check — only
	// sync's installed-ref re-check (checkInstalledRetraction) or the next
	// Pull actually reads the publisher's manifest and is entitled to lift
	// a retraction; the same invariant LockDependencies' prevRetracted
	// protects on the sibling full-rebuild path.
	if has && cur.Retracted {
		entry.Retracted = true
		entry.RetractedReason = cur.RetractedReason
	}
	return entry
}

// preserveUnreachedEntries carries every active entry the new lock does not
// reach into it, warning when any were carried.
func preserveUnreachedEntries(active, newActive *remote.Lockfile, unexpandedCount int) {
	preserved := 0
	for _, e := range active.AllEntries() {
		if _, ok := newActive.GetEntry(e.Type, e.Ref); !ok {
			newActive.AddEntry(e.Type, e.Ref, e.Entry)
			preserved++
		}
	}
	if preserved > 0 {
		clidiag.Warn("ctxloom", "dependency closure is incomplete (%d parent profile(s) unreachable); preserving %d existing lockfile entry(ies)", unexpandedCount, preserved)
	}
}

// checkUpgradedLock decides whether plan.next is to be recorded, sets
// result.NothingDeclared, and returns the refusal Save would give — so a
// preview refuses exactly what applying it would.
//
// NOTHING DECLARED, AND NOTHING ON DISK TO PROTECT. Writing then would
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
func checkUpgradedLock(m *remote.LockfileManager, plan *upgradePlan, result *UpgradeResult) (record bool, err error) {
	nothingToRecord := plan.next.IsEmpty() && plan.active.IsEmpty()
	result.NothingDeclared = nothingToRecord && !result.Incomplete
	if nothingToRecord {
		return false, nil
	}
	return true, m.CheckSave(plan.next)
}

// droppedEntries names, sorted, the entries of before that after no longer
// holds.
func droppedEntries(before, after *remote.Lockfile) []string {
	var dropped []string
	for _, e := range before.AllEntries() {
		if _, ok := after.GetEntry(e.Type, e.Ref); !ok {
			dropped = append(dropped, string(e.Ref))
		}
	}
	sort.Strings(dropped)
	return dropped
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
	ref, err := remote.ParseReference(string(p.Identity))
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
	worktree, err := ref.LocalWorktreePath(baseDir)
	if err == nil {
		_, err = install(ctx, ref.URL, p.Hash, ref.TreeRepoPath(), worktree)
	}
	if err != nil {
		clidiag.Warn("ctxloom", "%s was upgraded to %s but its cached tree could not be moved to that commit (%v); "+
			"run `ctxloom deps pull` to re-install it", p.Identity, p.Hash, err)
	}
}
