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
)

// The layout each on-disk FORM of an authored bundle is written into.
//
// The two forms live in SIBLING roots: paths.LayoutV1 holds the single-file
// document form and paths.LayoutV2 the directory tree form, and neither root
// contains the other. Anything that means "every authored bundle" must
// therefore visit BOTH — a scan of one root silently misses the other form and
// stays green while proving less.
const (
	singleFileBundleLayout = paths.LayoutV1
	dirFormBundleLayout    = paths.LayoutV2
)

// authoredBundlesRoots is every repo-relative root an authored bundle can sit
// in, for the scans that mean "all of them" rather than one form.
func authoredBundlesRoots() []string {
	return []string{singleFileBundlesRoot(), dirFormBundlesRoot()}
}

// singleFileBundlesRoot is the repo-relative directory holding SINGLE-FILE
// (<name>.yaml) authored bundles.
func singleFileBundlesRoot() string {
	return paths.RepoBundlesPrefixFor(singleFileBundleLayout)
}

// dirFormBundlesRoot is the repo-relative directory holding DIRECTORY-form
// (<name>/bundle.yaml) authored bundles.
func dirFormBundlesRoot() string {
	return paths.RepoBundlesPrefixFor(dirFormBundleLayout)
}

// bundleFilePath is the repo-relative path of a SINGLE-FILE authored bundle.
func bundleFilePath(name string) string {
	return path.Join(singleFileBundlesRoot(), name+".yaml")
}

// bundleDirPath is the repo-relative directory of a DIRECTORY-form authored
// bundle — the tree that carries the manifest and the item files.
func bundleDirPath(name string) string {
	return path.Join(dirFormBundlesRoot(), name)
}

// bundleDirManifestPath is the repo-relative path of a directory-form bundle's
// own manifest.
func bundleDirManifestPath(name string) string {
	return path.Join(bundleDirPath(name), bundles.DirectoryFormManifest)
}

// bundleDirItemPath is the repo-relative path of one file INSIDE a
// directory-form bundle, given that file's path relative to the bundle's own
// root (e.g. "fragments/guidance.md").
func bundleDirItemPath(name, rel string) string {
	return path.Join(bundleDirPath(name), rel)
}
