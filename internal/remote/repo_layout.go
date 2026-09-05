package remote

import (
	"github.com/ctxloom/ctxloom/internal/paths"
)

// The remote half of the bundle layout: where a publish WRITES, where a fetch
// LOOKS, and what a listing WALKS.
//
// WHY THESE EXIST. paths.RepoBundlesPrefixFor is the declared authority for the
// repo-relative bundles prefix, and it had ZERO production callers: publish,
// fetch and listing each re-joined RepoContentPrefix with a directory name of
// their own. A layout change therefore moved the accessor and nothing else, and
// the relocation it was written to serve failed twice — the second time with
// 152 acceptance scenarios red, overwhelmingly on the remote path. Routing all
// three through here is what makes "publish, fetch and listing agree" a
// property of the code rather than a thing three files independently get right.
//
// WHY kind IS ACCEPTED AND NOT BRANCHED ON. ItemTypeBundle is the only
// distributed item type (see types.go), so every remote item is a bundle and
// the bundles accessors are the whole mapping. The parameter is kept, exactly
// as PublishPath keeps it, so a second ItemType does not require re-widening
// every signature at once.

// RepoItemPrefix is the repo-relative directory an item PUBLISHES into and a
// fetch READS from, in the single-file document layout.
//
// Publish and fetch must name the same file or a bundle is written where no
// consumer looks, so both sides resolve it here rather than each composing a
// prefix of its own.
func RepoItemPrefix(_ ItemType) string {
	return paths.RepoBundlesPrefixFor(paths.LayoutV1)
}

// ContentItemPrefix is RepoItemPrefix relative to an already-open content root,
// for a reader that has resolved .ctxloom/content/ itself and must not re-state
// it.
func ContentItemPrefix(_ ItemType) string {
	return paths.ContentBundlesPrefixFor(paths.LayoutV1)
}

// RepoItemRoot is the repo-relative directory a LISTING walks: the parent that
// contains every layout's subtree, not any one layout's prefix.
//
// A walk anchored at a single layout cannot see the other, and the failure is
// silent — the repo lists half its bundles and reports success.
func RepoItemRoot(_ ItemType) string {
	return paths.RepoBundlesRoot()
}

// RepoItemName reduces a RepoItemRoot-relative path to the item's BARE name.
//
// A listing names each item by its path relative to the root it walked, so the
// moment a layout has a segment those names come back layout-qualified
// ("v2/atelier") and resolve to nothing. Every listing site passes its names
// through here so that a layout gaining a segment does not silently rename
// every item in the repo.
func RepoItemName(_ ItemType, rel string) string {
	return paths.TrimBundlesLayoutSegment(rel)
}
