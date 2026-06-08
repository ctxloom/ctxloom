package operations

import (
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// newConstraintResolver builds the dependency walker's hash resolver: it turns a
// manifest reference's version constraint into the concrete commit the lockfile
// should pin. Resolution follows a fixed precedence, so the lock is stable across
// relocks yet always satisfies the manifest:
//
//  1. Active lock carry-forward — a held entry, or one whose constraint is
//     unchanged, keeps its locked SHA (npm-style stability; `upgrade` is what
//     re-resolves within a range).
//  2. Bare commit — a ref already pinned to a hex SHA is recorded verbatim, with
//     no clone (this is also every legacy "@<sha>" ref).
//  3. Fresh resolution — a branch, semver range, or empty (default-branch)
//     constraint is resolved against the repo's version space.
//  4. Fault-tolerant fallback — if resolution fails (offline, no matching tag),
//     fall back to the last locked SHA when one exists; otherwise skip the item
//     with a warning rather than pin an empty SHA (CLAUDE.md: degrade, never
//     fabricate).
//
// reResolve selects the mode: false (lock) carries an unchanged-constraint entry
// forward for stability; true (upgrade) re-resolves it to the newest commit the
// constraint allows. A HELD entry is frozen in BOTH modes — hold always wins.
//
// Results are memoized per (identity, constraint) within a single flatten, and a
// RepoVersions is built at most once per repo URL.
func newConstraintResolver(ctx context.Context, active *remote.Lockfile, factory remote.FetcherFactory, auth remote.AuthConfig, reResolve bool) func(*remote.Reference) (string, string, bool) {
	type resolved struct{ sha, version string }
	cache := map[string]resolved{}
	rvByURL := map[string]remote.RepoVersions{}

	repoVersions := func(url string) remote.RepoVersions {
		if rv, ok := rvByURL[url]; ok {
			return rv
		}
		var rv remote.RepoVersions
		if f, err := factory(url, auth); err == nil {
			if owner, repo, perr := remote.ParseRepoURL(url); perr == nil {
				rv = remote.NewFetcherRepoVersions(f, owner, repo)
			}
		}
		rvByURL[url] = rv // cache the failure (nil) too — don't retry per ref
		return rv
	}

	lockEntry := func(ref *remote.Reference) (remote.LockEntry, bool) {
		if active == nil {
			return remote.LockEntry{}, false
		}
		return active.GetEntry(ref.ItemType, ref.CanonicalString())
	}

	return func(ref *remote.Reference) (string, string, bool) {
		identity := ref.CanonicalString()
		expr := ref.ContentVersion
		key := identity + "\x00" + expr
		if r, ok := cache[key]; ok {
			return r.sha, r.version, true
		}
		store := func(sha, version string) (string, string, bool) {
			cache[key] = resolved{sha, version}
			return sha, version, true
		}

		// 1. Carry forward a held entry always; an unchanged-constraint entry only
		//    in lock mode (upgrade re-resolves it).
		if e, ok := lockEntry(ref); ok && e.SHA != "" {
			if e.Pinned {
				return store(e.SHA, e.Version)
			}
			if !reResolve && e.RequestedVersion == expr {
				return store(e.SHA, e.Version)
			}
		}
		// 2. A bare commit name is already concrete — no clone needed.
		if remote.LooksLikeCommit(expr) {
			return store(expr, "")
		}
		// 3. Resolve the constraint against the repo's version space.
		if rv := repoVersions(ref.URL); rv != nil {
			if res, err := remote.ResolveConstraint(ctx, expr, rv); err == nil {
				return store(res.SHA, res.Version)
			}
		}
		// 4. Fault tolerant: keep the last locked SHA, else skip.
		if e, ok := lockEntry(ref); ok && e.SHA != "" {
			return store(e.SHA, e.Version)
		}
		fmt.Fprintf(os.Stderr, "ctxloom: warning: could not resolve %s@%s; skipping\n", identity, expr)
		return "", "", false
	}
}
