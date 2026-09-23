// Package acceptance: the corpus's ONE answer to "where does an authored
// bundle live on disk".
//
// UNTAGGED ON PURPOSE, like probe_assert.go and capability_hook_probe.go beside
// it: the acceptance-tagged step files and the untagged probe tests both write
// bundle fixtures, so the seam has to be visible to both halves of the package.
//
// WHY THIS EXISTS. The corpus used to build these paths by hand, as a string
// literal concatenated at ~60 call sites across two dozen files. A change that
// moved authored bundles into per-layout roots was built, merged, and REVERTED
// because it failed 207 acceptance steps: the instant the old root stopped
// being searched, every hand-built writer wrote somewhere nothing looked.
// Those literals were a distributed copy of a production decision with nothing
// binding the copies to the original.
//
// So the names below DERIVE from production through the ONE seam both suites
// share — testenv's, which composes the root from paths.RepoBundlesPrefixFor
// and the manifest leaf from bundles.DirectoryFormManifest. Nothing here or
// there spells ".ctxloom", "content", "bundles", a layout segment, or
// "bundle.yaml".
package acceptance

import (
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// bundleFilePath is the repo-relative path of an authored bundle's envelope.
func bundleFilePath(name string) string {
	return treeBundleManifestPath(name)
}

// treeBundlePath is the repo-relative directory of an authored bundle — the
// tree that carries the manifest and the item files. It is also where a
// publish lands the bundle on a remote repo.
func treeBundlePath(name string) string {
	return testenv.TreeBundlePath(name)
}

// treeBundleManifestPath is the repo-relative path of a bundle's own envelope.
func treeBundleManifestPath(name string) string {
	return testenv.TreeBundleManifestPath(name)
}

// treeBundleItemPath is the repo-relative path of one file INSIDE a bundle
// tree, given that file's path relative to the bundle's own root (e.g.
// "fragments/guidance.md").
func treeBundleItemPath(name, rel string) string {
	return testenv.TreeBundleItemPath(name, rel)
}
