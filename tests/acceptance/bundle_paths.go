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
// binding the copies to the original. The integration suite proved the same
// point a second time from the other side: its fixtures kept their literals,
// and when the format root moved they wrote where nothing looked and failed 44
// tests at once.
//
// So the names below DERIVE from production rather than restating it, and they
// do it through the ONE seam both suites share — testenv's, which composes the
// root from paths.RepoBundlesPrefixFor and the manifest leaf from
// bundles.DirectoryFormManifest. Nothing here or there spells ".ctxloom",
// "content", "bundles", a layout segment, or "bundle.yaml". That is the whole
// point: when the layout moves, production moves and BOTH suites follow in the
// same edit, because there is only one edit to make.
//
// What survives here rather than moving is the SHAPE vocabulary. Three shapes
// share the one root — a bare "<name>.yaml" document, a directory whose
// bundle.yaml still declares items inline, and a true tree with item files —
// and bundles.treeFormEnvelope tells the last two apart by the envelope's own
// content, never by path. A previous attempt at the v1 removal filed an
// inline-key directory as though shape and root were the same fact, which is
// exactly the bug treeFormEnvelope exists to prevent. The names below therefore
// stay distinct even where they resolve to identical bytes: a call site is
// asserting a SHAPE, and a reader must be able to see which one without
// chasing two names back to one expression.
package acceptance

import (
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// singleFileBundlesRoot is the repo-relative directory holding SINGLE-FILE
// (<name>.yaml) authored bundles.
func singleFileBundlesRoot() string {
	return testenv.BundlesRoot()
}

// treeBundlesRoot is the repo-relative directory holding TRUE-TREE authored
// bundles. Identical to singleFileBundlesRoot's value (see the package doc) —
// kept as its own name because callers are asserting a SHAPE.
func treeBundlesRoot() string {
	return testenv.BundlesRoot()
}

// bundleFilePath is the repo-relative path a fixture WRITES a bundle's envelope
// to when the fixture is authoring the tree SHAPE itself (a directory it
// constructs by writing to a nested path) — which is the tree's manifest.
//
// It is NOT where `ctxloom bundle create` puts a bundle it created for you:
// that command still writes the bare single-file form by DEFAULT (no --tree —
// see internal/cli/bundle_edit.go's bundleCreateTree, and singleFileBundlePath
// below). A fixture that creates a bundle through the real CLI and then wants
// to read or rewrite the file that command produced must use
// singleFileBundlePath, not this one — the two name genuinely different
// on-disk shapes, not two names for the same fact. Call sites that write
// their OWN content here (and so decide the shape by where they write) are
// unaffected; a fixture that also authors ITEMS this way must write them as
// files beside this manifest — inline item keys under a tree root are not a
// tree and bundles.readEnvelope refuses a bundle that declares both.
func bundleFilePath(name string) string {
	return treeBundleManifestPath(name)
}

// singleFileBundlePath is the repo-relative path of a SINGLE-FILE (bare
// "<name>.yaml") authored bundle — the shape `ctxloom bundle create` still
// produces by default. "The single-file form is gone" is true only of what a
// PUBLISH accepts (remote.Puller.installPulledItem refuses a document
// outright); the ordinary, entirely-local authoring path never stopped
// writing one, it only moved under the single remaining format root.
func singleFileBundlePath(name string) string {
	return testenv.SingleFileBundlePath(name)
}

// remoteSingleFilePublishPath is where a real `ctxloom bundle push` lands a
// bundle on a remote repo — remote.PublishPath's answer.
//
// The name is a holdover from when this differed from the tree path; it no
// longer does. remote.PublishPath composes ONE path per bundle name
// regardless of shape — RepoItemPath's leaf carries no extension for any
// bundle now (paths.BundleLayout.ItemFileName(LayoutV2) returns the bare
// name) — so this is BY VALUE the same expression as treeBundlePath(name).
// The two names are kept separate for the same reason singleFileBundlesRoot
// and treeBundlesRoot are: a call site naming this one is asserting "this is
// where a PUBLISH lands", not "this is a tree's own root", even though today
// they resolve to the same bytes.
//
// A body written raw at this path (no "/bundle.yaml" suffix, no manifest) is
// exactly what a real single-file `bundle push` produces, and it remains
// FETCHABLE: internal/remote's Puller.fetchItemBytes tries a single file at
// exactly this composed path first, before ever probing for a tree, which is
// the mechanism `ctxloom deps pull`/`remote sync` exercise. It is NOT
// reachable through remote.BundleReader.ReadBundleBytes, which internal/core/config
// uses for a different, now-dead read path (see that package's v1-removal
// commit) — a fixture seeded here is honest input for a PULL-shaped journey,
// not for anything that reads through BundleReader directly.
func remoteSingleFilePublishPath(name string) string {
	return remote.PublishPath(remote.ItemTypeBundle, name)
}

// treeBundlePath is the repo-relative directory of a TRUE-TREE authored bundle
// — the tree that carries the manifest and the item files.
func treeBundlePath(name string) string {
	return testenv.TreeBundlePath(name)
}

// treeBundleManifestPath is the repo-relative path of a tree bundle's own
// envelope.
func treeBundleManifestPath(name string) string {
	return testenv.TreeBundleManifestPath(name)
}

// treeBundleItemPath is the repo-relative path of one file INSIDE a tree
// bundle, given that file's path relative to the bundle's own root (e.g.
// "fragments/guidance.md").
func treeBundleItemPath(name, rel string) string {
	return testenv.TreeBundleItemPath(name, rel)
}

// inlineDirBundlePath is the repo-relative directory of a DIRECTORY-WRAPPED,
// still-inline-declaring bundle: one whose bundle.yaml still declares its
// items inline. Identical to treeBundlePath's value (see the package doc) —
// kept separate because a call site naming this one is asserting the RETIRED
// shape, not a true tree.
func inlineDirBundlePath(name string) string {
	return testenv.TreeBundlePath(name)
}

// inlineDirBundleManifestPath is the repo-relative path of such a bundle's
// envelope.
func inlineDirBundleManifestPath(name string) string {
	return testenv.TreeBundleManifestPath(name)
}
