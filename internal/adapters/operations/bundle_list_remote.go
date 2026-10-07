package operations

import (
	"context"
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/ident"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// listBundleInfos returns every bundle a `bundle list` should show, from the
// config bundle loader — the codebase's standard local+remote bundle reader. It
// fs-walks content/bundles (locally-authored bundles from `bundle create`) AND
// seeds every lockfile bundle, read canonically from its git clone (remote
// bundles are not extracted to disk — see remote.writePulledContent). Local
// bundles list by name, remote bundles by canonical ref; the two sources don't
// overlap, so nothing is double-listed. A dependency that vanished upstream is
// reported by `deps check` and `deps pull`, not here.
//
// The listing's contract: `bundle list` lists what is INSTALLED — local
// content under .ctxloom/content/bundles, the remotes pinned in the lockfile,
// and the companion loadouts this machine acquired. ctxloom's OWN loadout is
// excluded, and that is the contract rather than a preference: it is
// intrinsic — nobody installed it and nobody can remove it — so counting it
// under "Installed bundles (N)" states something false and makes `bundle
// remove` name a bundle the user has no way to act on. It stays addressable
// by its ref (`bundle show ctxloom:companion@ctxloom`).
//
// Fault-tolerant per CLAUDE.md: the seeded loader already degrades a bad
// lockfile/remote to a warning.
func listBundleInfos(ctx context.Context, cfg *config.Config) ([]*bundles.BundleInfo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	var infos []*bundles.BundleInfo
	for _, info := range cfg.BundleLoader().Catalog().
		Scoped(bundles.ProvenanceProject, bundles.ProvenanceRemote, bundles.ProvenanceCompanion).
		Infos() {
		if info.Self {
			continue
		}
		infos = append(infos, info)
	}

	stampLockState(cfg, infos)

	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// stampLockState copies the per-entry lockfile state a LISTING must show — the
// hold — onto the infos the loader produced.
//
// The loader reads bundle CONTENT and knows nothing about pins; a hold is a
// property of the lockfile entry, not of the bundle document, so it can only be
// joined here. Without the join, `bundle list` renders a frozen bundle and a
// failing sync identically.
//
// A lockfile that cannot be read degrades the listing rather than failing it —
// listing what IS present must survive a bad lockfile — but it says so, because
// a silent degrade here means the markers simply stop appearing and the listing
// looks healthy.
func stampLockState(cfg *config.Config, infos []*bundles.BundleInfo) {
	lock, err := remote.NewLockfileManager(ProjectAppDir(cfg), remote.WithLockfileFS(afero.NewOsFs())).Load()
	if err != nil {
		clidiag.Warn("ctxloom",
			"cannot read the lockfile: %v — held bundles will not be flagged in this listing", err)
		return
	}
	for _, info := range infos {
		// Keyed through the parser, not a cast: a name that is not a bundle
		// reference (a project bundle) has no lock entry to stamp.
		br, perr := ident.ParseBundleRef(info.Name)
		if perr != nil {
			continue
		}
		entry, ok := lock.Bundles[br.BundleIdentity()]
		if !ok {
			continue
		}
		info.Held = entry.Held
	}
}
