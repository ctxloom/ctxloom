package operations

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// listBundleInfos returns every bundle a `bundle list` should show, from the two
// standard sources presented through one interface, plus removed-upstream
// markers:
//
//   - PRESENT bundles come from the SeededBundleLoader — the codebase's standard
//     local+remote bundle reader. It fs-walks cache/bundles (locally-authored
//     bundles from `bundle create`) AND seeds every lockfile bundle, read
//     canonically from its git clone (remote bundles are not extracted to disk —
//     see remote.writePulledContent). Local bundles list by name, remote bundles
//     by canonical ref; the two sources don't overlap, so nothing is double-listed.
//   - DELETED bundles — present in an installed remote's history but gone at HEAD
//     — come from the Resolver/VCS history walk and are flagged Deleted so the
//     user sees a dependency has vanished upstream.
//
// Fault-tolerant per CLAUDE.md: the seeded loader already degrades a bad
// lockfile/remote to a warning, and the deleted-item walk is best-effort.
func listBundleInfos(ctx context.Context, cfg *config.Config) ([]*bundles.BundleInfo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	infos, err := cfg.SeededBundleLoader(cfg.ShouldUseDistilled()).List()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		seen[info.Name] = true
	}

	// Removed-upstream bundles via the Resolver's history walk over the installed
	// clones. Best-effort: a failure here must not break listing what's present.
	deleted, _ := bundleListDeletedResolver(cfg).ListDeleted(ctx, remote.ItemTypeBundle)
	for _, ref := range deleted {
		name := ref.CanonicalString()
		if seen[name] {
			continue
		}
		seen[name] = true
		infos = append(infos, &bundles.BundleInfo{Name: name, Deleted: true})
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// bundleListDeletedResolver builds a Resolver whose remote fetcher walks the
// already-downloaded clones of the lockfile's installed remotes (never fetching)
// so ListDeleted can surface items removed upstream. It carries only the remote
// fetcher — present-bundle listing is the SeededBundleLoader's job.
func bundleListDeletedResolver(cfg *config.Config) *remote.Resolver {
	baseDir := getBaseDir(cfg)

	var urls []string
	if lock, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(afero.NewOsFs())).Load(); err == nil {
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
	return remote.NewResolver(remoteFetcher)
}
