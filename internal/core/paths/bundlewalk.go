package paths

import (
	"path/filepath"
)

// The boundary a walk over a BUNDLES ROOT stops at.
//
// # Why this is one symbol and not a rule each walker follows
//
// A bundle is a TREE: "<name>/bundle.yaml" plus item files — fragments/,
// profiles/, skills/<n>/. A tree OWNS everything beneath it: those files are
// its ITEMS, not further bundles. So every walk that enumerates bundles must
// stop descending the moment it identifies a directory as one. A file is
// never a bundle, whatever its extension.
//
// Three independent walkers each failed to, with three different symptoms:
// the loader raised one spurious "malformed bundle" finding per item file on
// every command; `bundle sign --all` enumerated "agent-ensemble/profiles/
// coordinator" as a bundle and died resolving it; a remote listing offered
// item paths as installable bundle names. Each was fixed where it was found,
// which is why it recurred. Classification and the stop live here TOGETHER —
// a caller cannot ask "is this a bundle" without being handed the answer to
// "may I descend", so the two cannot come apart again.
//
// It sits in internal/core/paths because it is a fact about on-disk LAYOUT, and
// because internal/core/bundles imports internal/adapters/remote — the two heaviest callers
// are on opposite sides of that edge, and only a package below both can be
// shared by both. It stays filesystem-free for the same reason: the manifest's
// presence arrives as an argument, so an afero walker (stat) and a forge-tree
// walker (a directory listing it already holds) reach the same decision.

// BundleManifestName is the file whose presence makes a DIRECTORY a bundle:
// "<name>/bundle.yaml".
const BundleManifestName = "bundle.yaml"

// BundleManifestPath is the manifest of the bundle rooted at dir. Callers stat
// this to decide whether dir is a bundle at all and then READ the same path,
// so the file that was probed is provably the file that gets read.
func BundleManifestPath(dir string) string {
	return filepath.Join(dir, BundleManifestName)
}

// BundleWalkStep is the decision a walk over a bundles root takes at one
// entry: whether the entry IS a bundle, and what it is named.
type BundleWalkStep struct {
	// Name is the bundle's resolution name — its path relative to the walked
	// root, slash-separated. Empty unless IsBundle.
	//
	// It is NOT reduced to a bare name here: a listing over a repo root still
	// owes its result a TrimBundlesLayoutSegment, and a loader's search root is
	// already anchored at one layout. Doing it here would silently strip a
	// legitimate authored directory ("personal/foo") from the loader's names.
	Name string

	// IsBundle reports that this entry resolves as a bundle in its own right.
	// It is also the stop condition: everything below a bundle belongs to it.
	IsBundle bool
}

// WalkSkip is the value a filepath.WalkDir or afero.Walk callback must return
// for this entry: filepath.SkipDir for a bundle's own directory, nil for
// everything else, which the walk passes through.
func (s BundleWalkStep) WalkSkip() error {
	if s.IsBundle {
		return filepath.SkipDir
	}
	return nil
}

// ClassifyBundleWalkEntry decides one entry of a walk over a bundles root.
//
// rel is the entry's path relative to the walked root; isDir says whether it is
// a directory; hasManifest says whether that directory holds
// BundleManifestName, and is ignored for a file.
//
// The ROOT ITSELF (rel "." or empty) is never a bundle: every bundles root is
// the parent bundles sit under. A walker that named it would produce the name
// "." — which resolves to nothing, with success reported.
func ClassifyBundleWalkEntry(rel string, isDir, hasManifest bool) BundleWalkStep {
	rel = filepath.ToSlash(rel)
	if !isDir || rel == "." || rel == "" || !hasManifest {
		return BundleWalkStep{}
	}
	return BundleWalkStep{Name: rel, IsBundle: true}
}
