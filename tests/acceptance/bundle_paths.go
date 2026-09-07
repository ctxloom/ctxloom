// Package acceptance: the corpus's ONE answer to "where does an authored
// bundle live on disk".
//
// UNTAGGED ON PURPOSE, like probe_assert.go and capability_hook_probe.go beside
// it: the acceptance-tagged step files and the untagged probe tests both write
// bundle fixtures, so the seam has to be visible to both halves of the package.
//
// WHY THIS EXISTS. The corpus used to build these paths by hand, as a string
// literal concatenated at ~60 call sites across two dozen files. That is not a
// style complaint — it was measured. A change that moved authored bundles into
// per-layout roots was built, merged, and REVERTED because it failed 207
// acceptance steps: the instant the bare content/bundles root stopped being
// searched, every hand-built writer wrote somewhere nothing looked. Those
// literals were a distributed copy of a production decision with nothing
// binding the copies to the original.
//
// So the helpers below DERIVE from production rather than restating it. The root
// comes from paths.RepoBundlesPrefixFor and the manifest leaf from
// bundles.DirectoryFormManifest; nothing here spells ".ctxloom", "content",
// "bundles" or "bundle.yaml". That is the whole point: when the layout moves,
// production moves and the corpus follows in the same edit, because there is
// only one edit to make.
package acceptance

import (
	"path"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// The FORMAT root each fixture family is authored into.
//
// A bundles root's `v` segment is the FORMAT VERSION, and PLACEMENT FOLLOWS
// FORMAT, NOT FILE SHAPE. Format v1 holds single-file documents AND directories
// that still carry INLINE item keys; format v2 holds TRUE TREES only — no
// inline item keys, every item a file. A DIRECTORY IS NOT A TREE, and getting
// that backwards is not hypothetical: a previous attempt at this relocation
// filed an inline-key directory under v2 purely because it was a directory,
// which asserts a migration that never happened.
//
// The predicate is bundles.BundleLayoutFor — the same one the READ path uses to
// decide whether to open a tree — so a fixture's root is decided by exactly what
// decides a real bundle's.
//
// The three families here are therefore three DIFFERENT formats, not three
// spellings of one:
const (
	// singleFileBundleLayout: <name>.yaml. A single file is a document, so it
	// is format v1 by construction and no envelope inspection is needed.
	singleFileBundleLayout = paths.LayoutV1

	// treeBundleLayout: <name>/bundle.yaml declaring NO inline item keys, with
	// the items in files beside it. This is what format v2 means.
	treeBundleLayout = paths.LayoutV2

	// inlineDirBundleLayout: <name>/bundle.yaml that still declares its items
	// INLINE. It is a directory, and it is format v1 — the wrapper is not the
	// format.
	inlineDirBundleLayout = paths.LayoutV1
)

// singleFileBundlesRoot is the repo-relative directory holding SINGLE-FILE
// (<name>.yaml) authored bundles.
func singleFileBundlesRoot() string {
	return paths.RepoBundlesPrefixFor(singleFileBundleLayout)
}

// treeBundlesRoot is the repo-relative directory holding TRUE-TREE authored
// bundles.
func treeBundlesRoot() string {
	return paths.RepoBundlesPrefixFor(treeBundleLayout)
}

// bundleFilePath is the repo-relative path of a SINGLE-FILE authored bundle —
// where Trent's own project keeps it, in the committed local content tree.
func bundleFilePath(name string) string {
	return path.Join(singleFileBundlesRoot(), name+".yaml")
}

// remoteSingleFilePublishPath is where a real `ctxloom bundle push` lands a
// SINGLE-FILE bundle on a remote repo — remote.PublishPath's answer, which is
// remote.RepoItemPrefix's CURRENT format root regardless of the bundle's own
// content shape.
//
// This is NOT bundleFilePath. bundleFilePath names singleFileBundlesRoot —
// format v1, because a single file IS that format by construction (see this
// file's package doc) — but a publish always writes under whichever format is
// CURRENT, not under the format the content happens to already be in. The two
// prefixes coincided only while RepoItemPrefix itself equalled LayoutV1; once
// it names a later format, a fixture that hand-seeds a bare remote
// (SeedRemote/AdvanceRemote/SeedSignedRemote/AdvanceSignedRemote/
// UnpublishFromRemote) and reuses bundleFilePath for the REMOTE side writes
// where a real push never would, and any consumer fetch — which goes through
// the same RepoItemPrefix — finds nothing there.
func remoteSingleFilePublishPath(name string) string {
	return remote.PublishPath(remote.ItemTypeBundle, name, false)
}

// treeBundlePath is the repo-relative directory of a TRUE-TREE authored bundle
// — the tree that carries the manifest and the item files.
func treeBundlePath(name string) string {
	return path.Join(treeBundlesRoot(), name)
}

// treeBundleManifestPath is the repo-relative path of a tree bundle's own
// envelope.
func treeBundleManifestPath(name string) string {
	return path.Join(treeBundlePath(name), bundles.DirectoryFormManifest)
}

// treeBundleItemPath is the repo-relative path of one file INSIDE a tree
// bundle, given that file's path relative to the bundle's own root (e.g.
// "fragments/guidance.md").
func treeBundleItemPath(name, rel string) string {
	return path.Join(treeBundlePath(name), rel)
}

// inlineDirBundlePath is the repo-relative directory of a DIRECTORY-WRAPPED
// FORMAT-V1 bundle: one whose bundle.yaml still declares its items inline.
func inlineDirBundlePath(name string) string {
	return path.Join(paths.RepoBundlesPrefixFor(inlineDirBundleLayout), name)
}

// inlineDirBundleManifestPath is the repo-relative path of such a bundle's
// envelope.
func inlineDirBundleManifestPath(name string) string {
	return path.Join(inlineDirBundlePath(name), bundles.DirectoryFormManifest)
}
