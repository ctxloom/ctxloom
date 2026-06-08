package operations

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// bundleListResolver builds the scheme-dispatched Resolver used to LIST bundles:
// a remote fetcher over each installed remote's already-downloaded clone (never
// fetching — see remote.ClonedRepoVCSFactory) plus a local fetcher over the
// committed .ctxloom/local/ working copy. It returns the resolver and the remote
// URLs it will walk.
//
// The remote source set is the LOCKFILE's installed repos — not the full
// registry. Those repos are materialized by `remote sync`, so a missing clone
// here is an anomaly (a corrupt/incomplete cache), which is exactly why
// ListItems treats it as a "should never happen" warning rather than a routine
// state: a merely-configured-but-unsynced remote contributes no installed
// bundles and is simply absent, not warned. Reads go through go-git on the local
// clone, so fs is used only for the local working copy.
func bundleListResolver(cfg *config.Config, fs afero.Fs) (*remote.Resolver, []string) {
	baseDir := getBaseDir(cfg)

	var urls []string
	mgr := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(fs))
	if lock, err := mgr.Load(); err == nil {
		seen := map[string]bool{}
		for _, entry := range lock.Bundles {
			if entry.URL == "" || seen[entry.URL] {
				continue
			}
			seen[entry.URL] = true
			urls = append(urls, entry.URL)
		}
	}
	sort.Strings(urls)

	remoteFetcher := remote.NewRemoteRefFetcher(
		remote.ClonedRepoVCSFactory(newRepoCache(cfg)),
		remote.WithRemoteSources(urls),
	)
	localFetcher := remote.NewLocalRefFetcher(remote.FSVCSFactory(fs), paths.LocalPath(baseDir))
	return remote.NewResolver(remoteFetcher, localFetcher), urls
}

// listBundleInfos enumerates every bundle across the configured remotes'
// downloaded clones and the local working copy, named by its CANONICAL reference
// — replacing the old fs-walk of the extracted cache (which named bundles by
// their filesystem relpath). Listing is fault-tolerant per CLAUDE.md: a remote
// whose clone is absent (or a bundle that fails to parse) is warned to stderr
// with next steps, and the rest still list.
func listBundleInfos(ctx context.Context, cfg *config.Config) ([]*bundles.BundleInfo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	resolver, _ := bundleListResolver(cfg, afero.NewOsFs())

	refs, warn := resolver.List(ctx, remote.ItemTypeBundle)
	if warn != nil {
		// Non-fatal: a configured-but-unsynced remote can't be listed. Surface it
		// (with the next-step hint the error already carries) and continue.
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %v\n", warn)
	}

	infos := make([]*bundles.BundleInfo, 0, len(refs))
	for _, ref := range refs {
		data, err := resolver.Resolve(ctx, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: read %s: %v\n", ref.CanonicalString(), err)
			continue
		}
		b, err := bundles.ParseBundle(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: parse %s: %v\n", ref.CanonicalString(), err)
			continue
		}
		infos = append(infos, &bundles.BundleInfo{
			Name:          ref.CanonicalString(),
			Version:       b.Version,
			Description:   b.Description,
			Tags:          b.Tags,
			FragmentCount: b.FragmentCount(),
			PromptCount:   b.PromptCount(),
			MCPCount:      b.MCPCount(),
		})
	}

	// Surface items removed upstream (present in an installed repo's history,
	// gone at HEAD) so the user sees that a bundle they may depend on no longer
	// exists. These carry only the canonical name — there is nothing to read.
	deleted, _ := resolver.ListDeleted(ctx, remote.ItemTypeBundle)
	present := make(map[string]bool, len(infos))
	for _, info := range infos {
		present[info.Name] = true
	}
	for _, ref := range deleted {
		name := ref.CanonicalString()
		if present[name] {
			continue // re-added since; the live entry wins
		}
		present[name] = true
		infos = append(infos, &bundles.BundleInfo{Name: name, Deleted: true})
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}
