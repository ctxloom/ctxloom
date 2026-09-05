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
// Both are paths.LayoutV1 today, and the pair is not redundant. paths.LayoutV1
// names the single-file document form and paths.LayoutV2 the directory tree
// form, but LayoutV2's segment ALREADY resolves to a "v2" subdirectory while
// the corpus's directory-form fixtures have not moved there — they still sit at
// the bare root the reader still searches. Writing paths.LayoutV2 here today
// would relocate every directory-form fixture out from under the resolver.
//
// Flipping dirFormBundleLayout to paths.LayoutV2 IS the relocation, and it must
// land in the same commit as the production move: a window where the bytes are
// in one place and the resolver looks in another produces a bundle that
// resolves nowhere.
const (
	singleFileBundleLayout = paths.LayoutV1
	dirFormBundleLayout    = paths.LayoutV1
)

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
