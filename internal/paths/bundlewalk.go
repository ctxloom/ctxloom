package paths

import (
	"path"
	"path/filepath"
	"strings"
)

// The boundary a walk over a BUNDLES ROOT stops at.
//
// # Why this is one symbol and not a rule each walker follows
//
// A bundle exists in two on-disk forms: the single-file document "<name>.yaml"
// (LayoutV1) and the TREE "<name>/bundle.yaml" plus item files — fragments/,
// profiles/, skills/<n>/ (LayoutV2). A tree OWNS everything beneath it: those
// files are its ITEMS, not further bundles. So every walk that enumerates
// bundles must stop descending the moment it identifies a directory as one.
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
// It sits in internal/paths because it is a fact about on-disk LAYOUT, and
// because internal/bundles imports internal/remote — the two heaviest callers
// are on opposite sides of that edge, and only a package below both can be
// shared by both. It stays filesystem-free for the same reason: the manifest's
// presence arrives as an argument, so an afero walker (stat) and a forge-tree
// walker (a directory listing it already holds) reach the same decision.

// BundleManifestName is the file whose presence makes a DIRECTORY a bundle:
// "<name>/bundle.yaml", as against the single-file "<name>.yaml".
const BundleManifestName = "bundle.yaml"

// BundleDocumentExt is the extension of a single-file bundle document, and of
// the item documents inside a tree — which is exactly why a walk that reaches
// into a tree cannot tell the two apart by name alone, and must be stopped at
// the tree root instead.
const BundleDocumentExt = ".yaml"

// BundleManifestPath is the manifest of the directory-form bundle rooted at
// dir. Callers stat this to decide whether dir is a bundle at all and then
// READ the same path, so the file that was probed is provably the file that
// gets read.
func BundleManifestPath(dir string) string {
	return filepath.Join(dir, BundleManifestName)
}

// BundleWalkStep is the decision a walk over a bundles root takes at one
// entry: whether the entry IS a bundle, what it is named, and whether the walk
// may continue below it.
type BundleWalkStep struct {
	// Name is the bundle's resolution name — its path relative to the walked
	// root, slash-separated, with a single-file bundle's extension removed.
	// Empty unless IsBundle.
	//
	// It is NOT reduced to a bare name here: a listing over a repo root still
	// owes its result a TrimBundlesLayoutSegment, and a loader's search root is
	// already anchored at one layout. Doing it here would silently strip a
	// legitimate authored directory ("personal/foo") from the loader's names.
	Name string

	// IsBundle reports that this entry resolves as a bundle in its own right.
	IsBundle bool

	// IsTree reports the DIRECTORY form. It is the stop condition: everything
	// below such an entry belongs to this bundle.
	IsTree bool
}

// Descend reports whether the walk may continue below this entry.
func (s BundleWalkStep) Descend() bool { return !s.IsTree }

// WalkSkip is the value a filepath.WalkDir or afero.Walk callback must return
// for this entry.
//
// filepath.SkipDir from a FILE callback abandons the rest of the containing
// directory — it would hide every sibling bundle — so the sentinel is returned
// only for a tree root, where it means what the caller intends.
func (s BundleWalkStep) WalkSkip() error {
	if s.IsTree {
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
	if isDir {
		if rel == "." || rel == "" || !hasManifest {
			return BundleWalkStep{}
		}
		return BundleWalkStep{Name: rel, IsBundle: true, IsTree: true}
	}
	base := path.Base(rel)
	// A tree's own manifest is not a second bundle beside the directory that
	// holds it — the directory already answered for it.
	if base == BundleManifestName || !strings.HasSuffix(base, BundleDocumentExt) {
		return BundleWalkStep{}
	}
	return BundleWalkStep{Name: strings.TrimSuffix(rel, BundleDocumentExt), IsBundle: true}
}
