package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
//     fabricate). Skipping drops a dependency out of the lockfile silently
//     apart from that one line, so it carries the CAUSE — an unusable fetcher,
//     an unparseable repository URL and an unsatisfiable constraint need
//     different remedies and used to be indistinguishable.
//
// reResolve selects the mode: false (lock) carries an unchanged-constraint entry
// forward for stability; true (upgrade) re-resolves it to the newest commit the
// constraint allows. A HELD entry is frozen in BOTH modes — hold always wins.
//
// Results are memoized per (identity, constraint) within a single flatten, and a
// RepoVersions is built at most once per repo URL.
func newConstraintResolver(ctx context.Context, active *remote.Lockfile, factory remote.FetcherFactory, auth remote.AuthConfig, reResolve bool) func(*remote.Reference) (string, string, remote.SelectorKind, bool) {
	type resolved struct {
		sha, version string
		kind         remote.SelectorKind
	}
	cache := map[string]resolved{}
	failed := map[string]bool{} // negative cache: don't re-resolve (or re-warn) a known failure

	// A skipped dependency is reported by exactly one warning line, and the walk
	// then continues without the item — so that line is the user's only signal
	// and has to distinguish the causes. Cache the cause alongside the (nil)
	// RepoVersions rather than discarding it.
	type repoLookup struct {
		rv    remote.RepoVersions
		cause error
	}
	rvByURL := map[string]repoLookup{}

	repoVersions := func(url string) (remote.RepoVersions, error) {
		if e, ok := rvByURL[url]; ok {
			return e.rv, e.cause
		}
		var e repoLookup
		f, err := factory(url, auth)
		switch {
		case err != nil:
			e.cause = fmt.Errorf("no fetcher for %s: %w", url, err)
		default:
			owner, repo, perr := remote.ParseOwnerRepo(url)
			if perr != nil {
				e.cause = fmt.Errorf("unparseable repository URL %q: %w", url, perr)
			} else {
				e.rv = remote.NewFetcherRepoVersions(f, owner, repo)
			}
		}
		rvByURL[url] = e // cache the failure too — don't retry per ref
		return e.rv, e.cause
	}

	lockEntry := func(ref *remote.Reference) (remote.LockEntry, bool) {
		if active == nil {
			return remote.LockEntry{}, false
		}
		return active.GetEntry(ref.ItemType, ref.LockKey())
	}

	return func(ref *remote.Reference) (string, string, remote.SelectorKind, bool) {
		identity := ref.LockKey()
		expr := ref.ContentVersion
		key := identity + "\x00" + expr
		if r, ok := cache[key]; ok {
			return r.sha, r.version, r.kind, true
		}
		if failed[key] {
			return "", "", "", false // already resolved-and-failed this flatten; skip the network + warning
		}
		store := func(sha, version string, kind remote.SelectorKind) (string, string, remote.SelectorKind, bool) {
			cache[key] = resolved{sha, version, kind}
			return sha, version, kind, true
		}

		// 1. Carry forward a held entry always; an unchanged-constraint entry only
		//    in lock mode (upgrade re-resolves it). The kind rides along, derived
		//    for entries locked before it was persisted.
		if e, ok := lockEntry(ref); ok && e.SHA != "" {
			if e.Held {
				return store(e.SHA, e.Version, e.SelectorKind())
			}
			if !reResolve && e.RequestedVersion == expr {
				return store(e.SHA, e.Version, e.SelectorKind())
			}
		}
		// 2. A bare commit name is already concrete — no clone needed.
		if remote.LooksLikeCommit(expr) {
			return store(expr, "", remote.SelectorSHA)
		}
		// 3. Resolve the constraint against the repo's version space.
		rv, cause := repoVersions(ref.URL)
		if cause == nil && rv == nil {
			cause = fmt.Errorf("no version space for %s", ref.URL)
		}
		if cause == nil {
			res, err := remote.ResolveConstraint(ctx, expr, rv)
			if err == nil {
				return store(res.SHA, res.Version, res.Kind)
			}
			cause = err
		}
		// 4. Fault tolerant: keep the last locked SHA, else skip.
		if e, ok := lockEntry(ref); ok && e.SHA != "" {
			return store(e.SHA, e.Version, e.SelectorKind())
		}
		failed[key] = true
		clidiag.Warn("ctxloom", "could not resolve %s@%s; skipping: %v", identity, expr, cause)
		return "", "", "", false
	}
}
