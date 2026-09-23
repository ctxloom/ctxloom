package operations

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// Reconciliation: making the installed closure match upstream, in both
// directions.
//
// WHY PULL REMOVES AT ALL, AND WHY IT DOES NOT ASK. Installed remote content is
// a PROJECTION of remote state. Every byte of it can be re-fetched from the
// address it came from, none of it is authored here, and nothing a person typed
// lives in it — the authored things are the profile that names the bundle and
// the lockfile that pins it. So dropping a bundle a remote has stopped
// publishing is synchronization, in the same sense installing one it has
// started publishing is. A `--yes` on it would put a prompt in front of the
// routine half of a sync, which is how prompts become reflexes.
//
// WHAT MAKES IT SAFE IS NOT THE PROMPT, IT IS THE EVIDENCE RULE below.

// THE EVIDENCE RULE. Content is treated as deleted upstream ONLY on a
// not-found answer from a repository this run has separately proved it can
// read.
//
// The two halves are both necessary and the ordering is not negotiable.
//
//	NOT FOUND IS NOT ENOUGH. A private repository fetched with an expired or
//	revoked credential does not answer 401 — forges answer 404, because
//	confirming a repository exists is itself a disclosure. So a per-item
//	not-found is indistinguishable, at the fetcher seam, from "you have lost
//	your right to look". Every dependency from that remote would report gone,
//	and a reconcile that believed it would empty the installation and exit 0.
//
//	REACHABILITY IS PROVED PER REPOSITORY, ONCE, BEFORE ANY ITEM IS ASKED
//	ABOUT. A repository that fails that proof has none of its dependencies
//	touched, whatever the item probe would have said — and its items are never
//	probed at all, because the answers are known to be worthless.
//
// AND ABSENCE IS NEVER DERIVED FROM A LISTING. The item probe asks about ONE
// reference's own content path. An API that answers a directory listing with an
// empty page — pagination, a rate limit, a moved layout — therefore cannot
// contribute a single deletion, because no code path here reads a listing to
// decide what is gone.
//
// The cost is a false NEGATIVE: a remote that is down keeps content installed
// that has really been withdrawn. That is the right way round. The withdrawn
// content is still gated at exposure by the trust and retraction machinery, and
// the next reachable pull removes it; an emptied installation reported as a
// successful sync has no such second chance.

// reachProbe proves a repository can be READ this run. Any error means it could
// not be, and that is the whole contract — a reach probe never distinguishes
// kinds of failure, because every kind has the same consequence here.
type reachProbe func(ctx context.Context, repoURL string) error

// contentProbe reports whether one reference's content is retrievable from its
// repository's current tip. It answers about that reference's own path and
// never about a directory listing (see THE EVIDENCE RULE above).
type contentProbe func(ctx context.Context, ref string) error

// ReconcilePlan is what a reconcile decided, split by whether it is authority.
type ReconcilePlan struct {
	// Gone is every installed reference upstream demonstrably no longer serves.
	Gone []string
	// Unreachable is every repository whose state could not be established,
	// with the dependencies left untouched because of it.
	Unreachable []UncheckedRemote
}

// UncheckedRemote is one repository the reconcile could not establish the state
// of, and what that cost. Reason is carried because "could not check" with no
// cause is something a user can neither act on nor decide to ignore.
type UncheckedRemote struct {
	URL    string
	Refs   []string
	Reason string
}

// planReconcile decides, for every installed reference, whether upstream has
// stopped serving it, under THE EVIDENCE RULE above.
//
// It is a pure function of its two probes so the rule can be exercised against
// every shape of failure without a network: an unreachable host, a revoked
// credential, a transport error mid-listing, an unparseable lockfile entry.
func planReconcile(ctx context.Context, installed []string, reach reachProbe, content contentProbe) ReconcilePlan {
	byRepo, unparseable := groupRefsByRepo(installed)

	var plan ReconcilePlan
	if len(unparseable) > 0 {
		// No repository to prove reachable, so nothing this reference could be
		// authority about. Reported, never removed: a lockfile entry this build
		// cannot read is something the user has to look at, not content to
		// delete on their behalf.
		plan.Unreachable = append(plan.Unreachable, UncheckedRemote{
			Refs:   unparseable,
			Reason: "the reference could not be parsed, so no repository could be checked for it",
		})
	}

	for _, repoURL := range collections.SortedKeys(byRepo) {
		refs := byRepo[repoURL]
		if err := reach(ctx, repoURL); err != nil {
			plan.Unreachable = append(plan.Unreachable, UncheckedRemote{
				URL: repoURL, Refs: refs, Reason: err.Error(),
			})
			continue
		}
		for _, ref := range refs {
			switch err := content(ctx, ref); {
			case err == nil:
				// Still served.
			case errors.Is(err, errs.ErrRemoteContentNotFound):
				plan.Gone = append(plan.Gone, ref)
			default:
				// Reachable repository, failed lookup. Not-found is a fact
				// about the repository; anything else is a fact about the
				// attempt, and an attempt says nothing about what exists.
				plan.Unreachable = append(plan.Unreachable, UncheckedRemote{
					URL: repoURL, Refs: []string{ref}, Reason: err.Error(),
				})
			}
		}
	}
	return plan
}

// groupRefsByRepo buckets installed references by the repository they come
// from, so reachability is proved once per repository rather than once per
// dependency — and so a repository can never be observed reachable for one of
// its bundles and unreachable for another inside one reconcile.
func groupRefsByRepo(installed []string) (byRepo map[string][]string, unparseable []string) {
	byRepo = map[string][]string{}
	for _, ref := range installed {
		parsed, err := remote.ParseReference(ref)
		if err != nil || parsed.URL == "" {
			unparseable = append(unparseable, ref)
			continue
		}
		byRepo[parsed.URL] = append(byRepo[parsed.URL], ref)
	}
	return byRepo, unparseable
}

// ReconcileResult is the plan a reconcile applied and what it could not do
// while applying it: a removal that failed, or a warning the removal itself
// raised. Warnings are reported and never fatal — reconciliation is a
// SECOND guarantee layered on a pull that has already succeeded, and a pull
// that installed everything asked of it must not be reported as failed
// because the tidy-up half could not run.
type ReconcileResult struct {
	Plan     ReconcilePlan
	Warnings []string
}

// ReconcileInstalled runs the reconcile against the real remotes behind cfg's
// active lockfile and applies what it decided. The one error is a lockfile
// that could not be read: nothing was decided, so there is no plan to render.
func ReconcileInstalled(ctx context.Context, cfg *config.Config) (ReconcileResult, error) {
	appDir := ProjectAppDir(cfg)
	lockManager := remote.NewLockfileManager(appDir)
	lockfile, err := lockManager.Load()
	if err != nil {
		return ReconcileResult{}, err
	}

	var installed []string
	for _, e := range lockfile.AllEntries() {
		installed = append(installed, e.Ref)
	}
	if len(installed) == 0 {
		return ReconcileResult{}, nil
	}

	reach, content := upstreamProbes(cfg)
	res := ReconcileResult{Plan: planReconcile(ctx, installed, reach, content)}

	if len(res.Plan.Gone) == 0 {
		return res, nil
	}
	items := make([]RemovedItem, 0, len(res.Plan.Gone))
	for _, ref := range res.Plan.Gone {
		items = append(items, RemovedItem{Type: remote.ItemTypeBundle, Ref: ref})
	}
	removed, err := RemoveLocalItems(RemoveLocalItemsRequest{
		Items:       items,
		Lockfile:    lockfile,
		LockManager: lockManager,
	})
	if err != nil {
		res.Warnings = append(res.Warnings, "remove withdrawn dependencies: "+err.Error())
		return res, nil
	}
	res.Warnings = append(res.Warnings, removed.Warnings...)
	return res, nil
}

// upstreamProbes builds the production pair.
//
// REACH is a live UpdateRepo against the repository: a clone if the local
// cache holds nothing for it yet, otherwise a `git fetch` against the actual
// remote. Either shape touches the network on every call, so an expired
// credential, a deleted repository and a dead host all fail it — which is the
// point. GetDefaultBranch was tried here first and rejected: routed through
// the cached fetcher it resolves via RepoCache.EnsureRef, which returns an
// EXISTING local clone without touching the network at all — a remote whose
// bare repo had been renamed away still answered reachable from the stale
// clone, and reconcile reported nothing. A probe that a local cache can
// satisfy proves nothing about the remote, which is why this one goes through
// UpdateRepo instead of through the fetcher's cached read path.
//
// CONTENT is a listing of ONE reference's bundle tree, probed the way a pull
// resolves a bundle (remote.Puller.fetchItemBytes). It is safe to read through the
// cached fetcher because it only ever runs after REACH has already proved,
// this run, that the repository is live — by which point the clone UpdateRepo
// just produced is current.
func upstreamProbes(cfg *config.Config) (reachProbe, contentProbe) {
	factory := NewCachedFetcherFactory(cfg)
	cache := NewRepoCache(cfg)
	auth := remote.LoadAuth(ProjectAppDir(cfg))

	fetcherFor := func(repoURL string) (remote.Fetcher, string, string, error) {
		owner, repo, err := remote.ParseOwnerRepo(repoURL)
		if err != nil {
			// Plain git has no owner/repo pair; the clone-backed fetcher
			// ignores both, so empty strings travel harmlessly.
			owner, repo = "", ""
		}
		f, ferr := factory(repoURL, auth)
		if ferr != nil {
			return nil, "", "", ferr
		}
		return f, owner, repo, nil
	}

	reach := func(ctx context.Context, repoURL string) error {
		forgeType, _, err := remote.DetectForge(repoURL)
		if err != nil {
			return err
		}
		if _, err := cache.UpdateRepo(ctx, repoURL, forgeType); err != nil {
			return err
		}
		return nil
	}

	content := func(ctx context.Context, refStr string) error {
		ref, err := remote.ParseReference(refStr)
		if err != nil {
			return err
		}
		f, owner, repo, err := fetcherFor(ref.URL)
		if err != nil {
			return err
		}
		filePath := ref.BuildFilePath(remote.ItemTypeBundle)
		if _, _, derr := remote.ProbeBundleTreeRoots(filePath, func(root string) ([]remote.DirEntry, error) {
			return f.ListDir(ctx, owner, repo, root, "")
		}); derr != nil {
			return derr
		}
		return nil
	}

	return reach, content
}
