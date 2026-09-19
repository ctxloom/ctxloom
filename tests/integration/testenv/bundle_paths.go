package testenv

import (
	"path"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Where a test fixture puts an authored bundle, derived from production rather
// than restated.
//
// Both suites author bundles: the acceptance corpus (which imports this
// package) and the integration tests beside it. They therefore need ONE answer
// to "where does an authored bundle live", and it has to be a DERIVED one.
// When the format root moved, every integration fixture that had spelled the
// old root as a string literal wrote to a directory nothing looked in, and the
// suite failed 44 tests at once — while production, the acceptance corpus and
// every other gate stayed green. A literal path in a fixture is a copy of a
// production decision with nothing binding it to the original; nothing below
// spells ".ctxloom", "content", "bundles", a layout segment, or "bundle.yaml".
//
// SHAPE is a separate question from ROOT, and these helpers keep it separate.
// Three shapes share the one root and all three still load
// (bundles.localFSReader.readLocalTreeForm names them): a single-file
// "<name>.yaml" document, a "<name>/bundle.yaml" that declares its items
// inline, and a true tree whose bundle.yaml declares NO items and whose
// fragments/commands/skills are files beside it. Which one a fixture wants is
// the fixture's decision — it is made by choosing a function here, so a reader
// can tell from the call site.

// BundlesRoot is the repo-relative directory authored bundles live under: the
// project-relative half (".ctxloom/..."), which is also the path a remote repo
// carries them at.
func BundlesRoot() string {
	return paths.RepoBundlesPrefixFor(bundleLayout)
}

// bundleLayout is the layout every authored fixture is written in. It is a
// symbol rather than a literal so that a layout change is one edit in
// production plus zero here.
const bundleLayout = paths.LayoutV2

// LocalBundlesDir is BundlesRoot's absolute counterpart for a fixture that
// builds an app directory itself (an operations-level test handed an AppPaths
// entry) rather than writing project-relative through TestEnvironment.
func LocalBundlesDir(appDir string) string {
	return paths.LocalBundlesPathFor(appDir, bundleLayout)
}

// SingleFileBundlePath is the repo-relative path of a SINGLE-FILE
// ("<name>.yaml") authored bundle — one document carrying its items inline.
// This is the shape `ctxloom bundle create` writes by default, so a fixture
// that reads back what the real CLI produced wants this one.
func SingleFileBundlePath(name string) string {
	return path.Join(BundlesRoot(), name+".yaml")
}

// TreeBundlePath is the repo-relative directory of a DIRECTORY-form bundle:
// the directory holding bundle.yaml.
func TreeBundlePath(name string) string {
	return path.Join(BundlesRoot(), name)
}

// TreeBundleManifestPath is the repo-relative path of a directory-form
// bundle's own envelope.
func TreeBundleManifestPath(name string) string {
	return path.Join(TreeBundlePath(name), bundles.DirectoryFormManifest)
}

// TreeBundleItemPath is the repo-relative path of one file INSIDE a tree
// bundle, given that file's path relative to the bundle's own root (e.g.
// "fragments/guidance.md"). A tree's envelope must declare no items inline;
// bundles.readEnvelope refuses a bundle that says both.
func TreeBundleItemPath(name, rel string) string {
	return path.Join(TreeBundlePath(name), filepath.ToSlash(rel))
}
