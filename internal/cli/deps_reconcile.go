package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// reconcilePlan is what a reconcile decided, split by whether it is authority.
type reconcilePlan struct {
	// Gone is every installed reference upstream demonstrably no longer serves.
	Gone []string
	// Unreachable is every repository whose state could not be established,
	// with the dependencies left untouched because of it.
	Unreachable []uncheckedRemote
}

// uncheckedRemote is one repository the reconcile could not establish the state
// of, and what that cost. Reason is carried because "could not check" with no
// cause is something a user can neither act on nor decide to ignore.
type uncheckedRemote struct {
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
func planReconcile(ctx context.Context, installed []string, reach reachProbe, content contentProbe) reconcilePlan {
	byRepo, unparseable := groupRefsByRepo(installed)

	var plan reconcilePlan
	if len(unparseable) > 0 {
		// No repository to prove reachable, so nothing this reference could be
		// authority about. Reported, never removed: a lockfile entry this build
		// cannot read is something the user has to look at, not content to
		// delete on their behalf.
		plan.Unreachable = append(plan.Unreachable, uncheckedRemote{
			Refs:   unparseable,
			Reason: "the reference could not be parsed, so no repository could be checked for it",
		})
	}

	for _, repoURL := range sortedKeys(byRepo) {
		refs := byRepo[repoURL]
		if err := reach(ctx, repoURL); err != nil {
			plan.Unreachable = append(plan.Unreachable, uncheckedRemote{
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
				plan.Unreachable = append(plan.Unreachable, uncheckedRemote{
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

// renderReconcile says what the reconcile did and what it could not do.
//
// Both halves are load-bearing and neither substitutes for the other. A
// removal nobody can name is a removal nobody can undo: the user finds out by
// missing the content later, with no record of which pull took it. And
// "could not reach this remote" is the sentence that stops them concluding
// their installation was verified against upstream when part of it was never
// checked at all.
func renderReconcile(w io.Writer, plan reconcilePlan) {
	if len(plan.Gone) > 0 {
		fmt.Fprintf(w, "\nRemoved %d dependency(ies) no longer published by their remote:\n", len(plan.Gone))
		for _, ref := range plan.Gone {
			fmt.Fprintf(w, "  - %s\n", ref)
		}
		fmt.Fprintln(w, "  Re-adding them upstream and pulling again restores them; nothing authored here was touched.")
	}

	for _, u := range plan.Unreachable {
		where := u.URL
		if where == "" {
			where = "an unidentifiable repository"
		}
		fmt.Fprintf(w, "\n%s could not be reached, so its dependencies were left exactly as they are (%s).\n", where, u.Reason)
		for _, ref := range u.Refs {
			fmt.Fprintf(w, "  - kept: %s\n", ref)
		}
		fmt.Fprintln(w, "  Nothing is removed on the strength of a remote that could not be read.")
	}
}

// reconcileInstalled runs the reconcile against the real remotes behind cfg's
// active lockfile and applies what it decided.
//
// Failures here are reported and never fatal: reconciliation is a SECOND
// guarantee layered on a pull that has already succeeded, and a pull that
// installed everything asked of it must not be reported as failed because the
// tidy-up half could not run.
func reconcileInstalled(ctx context.Context, cfg *config.Config, out io.Writer) {
	appDir := projectAppDir(cfg)
	lockManager := remote.NewLockfileManager(appDir)
	lockfile, err := lockManager.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "reconcile: read the lockfile: %v", err)
		return
	}

	var installed []string
	for _, e := range lockfile.AllEntries() {
		installed = append(installed, e.Ref)
	}
	if len(installed) == 0 {
		return
	}

	reach, content := upstreamProbes(cfg)
	plan := planReconcile(ctx, installed, reach, content)
	renderReconcile(out, plan)

	if len(plan.Gone) == 0 {
		return
	}
	items := make([]operations.RemovedItem, 0, len(plan.Gone))
	for _, ref := range plan.Gone {
		items = append(items, operations.RemovedItem{Type: remote.ItemTypeBundle, Ref: ref})
	}
	res, err := operations.RemoveLocalItems(operations.RemoveLocalItemsRequest{
		AppDir:      appDir,
		Items:       items,
		Lockfile:    lockfile,
		LockManager: lockManager,
		FS:          afero.NewOsFs(),
	})
	if err != nil {
		clidiag.Warn("ctxloom", "reconcile: remove withdrawn dependencies: %v", err)
		return
	}
	for _, w := range res.Warnings {
		clidiag.Warn("ctxloom", "reconcile: %s", w)
	}
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
// CONTENT is a fetch of ONE reference's own path, with the directory form
// checked when the single file is absent, mirroring how a pull resolves a
// bundle (remote.Puller.fetchItemBytes). Only when BOTH shapes report not-found
// is the bundle absent — a publisher who migrated a single-file bundle to its
// directory form has not withdrawn anything. It is safe to read through the
// cached fetcher because it only ever runs after REACH has already proved,
// this run, that the repository is live — by which point the clone UpdateRepo
// just produced is current.
func upstreamProbes(cfg *config.Config) (reachProbe, contentProbe) {
	factory := operations.NewCachedFetcherFactory(cfg)
	cache := operations.NewRepoCache(cfg)
	auth := remote.LoadAuth(projectAppDir(cfg))

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
		if _, ferr := f.FetchFile(ctx, owner, repo, filePath, ""); ferr == nil {
			return nil
		} else if !errors.Is(ferr, errs.ErrRemoteContentNotFound) {
			return ferr
		}
		// The single file is absent; the directory form is the other shape a
		// bundle legitimately takes, and a publisher who migrated between them
		// has withdrawn nothing.
		if _, derr := f.ListDir(ctx, owner, repo, remote.BundleTreeRoot(filePath), ""); derr != nil {
			return derr
		}
		return nil
	}

	return reach, content
}
