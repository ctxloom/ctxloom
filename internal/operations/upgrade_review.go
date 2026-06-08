package operations

import (
	"context"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// UpgradeResult reports the outcome of StageUpgrade.
type UpgradeResult struct {
	// TrustedApplied is the number of items from trusted remotes whose lock entry
	// was advanced and applied to the active lock automatically (no review).
	TrustedApplied int
	// Staged is the number of items from untrusted remotes whose new SHA was
	// staged in the pending lockfile for review.
	Staged int
}

// StageUpgrade re-resolves the project's dependency closure to the newest commit
// each manifest constraint allows, and moves the LOCK accordingly — the manifest
// is never rewritten. A change from a TrustBundles=true remote is applied
// straight to the active lock (trust means "apply without review"); a change from
// an untrusted remote is staged in the pending lockfile for `bundle review` /
// `approve`. A held entry is frozen and never advances. A hash conflict in the
// proposed closure is a hard error; nothing is applied.
//
// This is the review trigger under the version-constraint model: passive `sync`
// installs exactly what the lock pins and stages nothing; `upgrade` re-resolves
// within constraints and surfaces (or, for trusted remotes, applies) the moves.
func StageUpgrade(ctx context.Context, cfg *config.Config) (UpgradeResult, error) {
	var res UpgradeResult
	loader := profileLoader(cfg)
	locals, err := loader.List()
	if err != nil {
		return res, err
	}

	baseDir := getBaseDir(cfg)
	auth := remote.LoadAuth(baseDir)
	factory := remote.FetcherFactory(newCachedFetcherFactory(cfg))
	registry, _ := remote.NewRegistry(paths.RemotesPath(baseDir))
	active, _ := remote.NewLockfileManager(baseDir).Load()

	// Advance every referenced clone to live HEAD (and fetch tags) so resolution
	// sees the newest commit each constraint permits.
	refreshRepoCaches(ctx, newRepoCache(cfg), directRepoURLs(locals))

	// Re-resolve the whole closure (upgrade mode): every unheld ref advances to
	// the newest commit its constraint allows; held entries stay put. Conflicts
	// abort before anything is written.
	resolve := newConstraintResolver(ctx, active, factory, auth, true)
	proposed, conflicts := flattenRootsWith(ctx, loader, factory, auth, locals, resolve)
	if len(conflicts) > 0 {
		return res, ConflictError(conflicts)
	}

	// Route each item by trust. The full proposed closure is staged in pending so
	// `show_bundle_verbatim` can read at the new SHA; trusted entries are applied
	// to active too, so they match and don't surface in review.
	newActive := emptyLockfile()
	pending := emptyLockfile()
	for _, p := range proposed {
		cur, has := active.GetEntry(p.Type, p.Identity)
		entry := remote.LockEntry{SHA: p.Hash, URL: p.URL, RequestedVersion: p.Constraint, Version: p.Version}
		if has && cur.Pinned {
			entry.Pinned = true // hold survives a relock
		}
		pending.AddEntry(p.Type, p.Identity, entry)

		changed := !has || cur.SHA != p.Hash
		switch {
		case isTrustedRemote(registry, remoteNameForKey(registry, p.Identity)):
			newActive.AddEntry(p.Type, p.Identity, entry) // trusted → apply now
			if changed {
				res.TrustedApplied++
			}
		case has:
			newActive.AddEntry(p.Type, p.Identity, cur) // untrusted → active unchanged until approved
			if changed {
				res.Staged++
			}
		default:
			// Untrusted brand-new dependency: staged only, kept out of active until
			// the user approves it.
			res.Staged++
		}
	}

	// Persist active only when a trusted change actually landed — a pure-staging
	// upgrade leaves the active lock untouched until the user approves.
	if res.TrustedApplied > 0 {
		if serr := remote.NewLockfileManager(baseDir).Save(newActive); serr != nil {
			return res, serr
		}
	}

	pendingMgr := remote.NewLockfileManager(baseDir, remote.WithPendingLockfile())
	if res.Staged == 0 {
		_ = pendingMgr.Delete() // nothing to review → clear any stale pending
		return res, nil
	}
	if serr := pendingMgr.Save(pending); serr != nil {
		return res, serr
	}
	return res, nil
}

// ApproveUpgrade adopts the staged upgrade: it merges each pending lock entry
// into the active lock (advancing the recorded SHA) and clears pending. The
// manifest is not touched — approval moves the lock only. A held entry keeps its
// hold across approval. Returns the number of entries advanced.
func ApproveUpgrade(_ context.Context, cfg *config.Config) (int, error) {
	pending, err := LoadPendingLockfile(cfg)
	if err != nil || pending == nil || pending.IsEmpty() {
		return 0, err
	}
	activeMgr := remote.NewLockfileManager(getBaseDir(cfg))
	active, err := activeMgr.Load()
	if err != nil {
		return 0, err
	}

	applied := 0
	for _, e := range pending.AllEntries() {
		cur, has := active.GetEntry(e.Type, e.Ref)
		if has && cur.SHA == e.Entry.SHA {
			continue // already matches (e.g. a trusted change applied at stage time)
		}
		entry := e.Entry
		if has && cur.Pinned {
			entry.Pinned = true // preserve a hold across approval
		}
		active.AddEntry(e.Type, e.Ref, entry)
		applied++
	}

	if serr := activeMgr.Save(active); serr != nil {
		return applied, serr
	}
	_ = ClearPendingLockfile(cfg)
	return applied, nil
}

// directRepoURLs returns the unique repo URLs of the direct remote refs across
// the given profiles (for the per-URL clone refresh).
func directRepoURLs(profs []*profiles.Profile) []string {
	seen := map[string]struct{}{}
	var urls []string
	add := func(refs []string) {
		for _, r := range refs {
			base, _ := splitHashItem(r)
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

// splitHashItem splits a ref into its base (url@kind/path[@version]) and a
// trailing "#item-path" suffix.
func splitHashItem(ref string) (base, item string) {
	if i := strings.Index(ref, "#"); i >= 0 {
		return ref[:i], ref[i:]
	}
	return ref, ""
}

func emptyLockfile() *remote.Lockfile {
	return &remote.Lockfile{
		Version:  1,
		Bundles:  map[string]remote.LockEntry{},
		Profiles: map[string]remote.LockEntry{},
	}
}
