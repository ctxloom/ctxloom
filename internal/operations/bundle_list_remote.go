package operations

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// listBundleInfos returns every bundle a `bundle list` should show, from the two
// standard sources presented through one interface, plus removed-upstream
// markers:
//
//   - PRESENT bundles come from the SeededBundleLoader — the codebase's standard
//     local+remote bundle reader. It fs-walks content/bundles (locally-authored
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

	stampLockState(cfg, infos)

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// stampLockState copies the per-entry lockfile state a LISTING must show — held
// and retracted — onto the infos the loader produced.
//
// The loader reads bundle CONTENT and knows nothing about pins; both of these
// are properties of the lockfile entry, not of the bundle document, so they can
// only be joined here. Without the join, `bundle list` renders a frozen bundle
// and a failing sync identically, and renders a publisher's retraction not at
// all while continuing to serve the content.
//
// A lockfile that cannot be read degrades the listing rather than failing it —
// listing what IS present must survive a bad lockfile — but it says so, because
// a silent degrade here means the markers simply stop appearing and the listing
// looks healthy.
func stampLockState(cfg *config.Config, infos []*bundles.BundleInfo) {
	lock, err := remote.NewLockfileManager(getBaseDir(cfg), remote.WithLockfileFS(afero.NewOsFs())).Load()
	if err != nil {
		clidiag.Warn("ctxloom",
			"cannot read the lockfile: %v — held and retracted bundles will not be flagged in this listing", err)
		return
	}
	for _, info := range infos {
		entry, ok := lock.Bundles[info.Name]
		if !ok {
			continue
		}
		info.Held = entry.Pinned
		info.Retracted = entry.Retracted
		info.RetractedReason = entry.RetractedReason
	}
}

// bundleListDeletedResolver builds a Resolver whose remote fetcher walks the
// already-downloaded clones of the lockfile's installed remotes (never fetching)
// so ListDeleted can surface items removed upstream. It carries only the remote
// fetcher — present-bundle listing is the SeededBundleLoader's job.
func bundleListDeletedResolver(cfg *config.Config) *remote.Resolver {
	baseDir := getBaseDir(cfg)

	var urls []string
	lock, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(afero.NewOsFs())).Load()
	if err != nil {
		// An UNREADABLE lockfile is not an empty one. Every remote source this
		// resolver walks comes from here, so swallowing the error leaves it with
		// nothing to walk and `bundle list` quietly stops flagging bundles that
		// vanished upstream — the listing looks complete and healthy. Degrading
		// to a partial listing is right (CLAUDE.md: listing what IS present must
		// survive a bad lockfile); doing it silently is not.
		clidiag.Warn("ctxloom",
			"cannot read %s: %v — bundles removed upstream will not be flagged in this listing",
			paths.LockPath(baseDir), err)
	} else {
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
		remote.ClonedRepoVCSFactory(NewRepoCache(cfg)),
		remote.WithRemoteSources(urls),
	)
	return remote.NewResolver(remoteFetcher)
}
