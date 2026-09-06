package remote

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

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

// ContentItemRoot is RepoItemRoot relative to an already-open content root —
// the root a FILESYSTEM listing walks, for a backend handed .ctxloom/content/
// rather than a repository.
func ContentItemRoot(_ ItemType) string {
	return paths.ContentBundlesRoot()
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

// BundleTreeRoots names every repository root at which a bundle's DIRECTORY
// form may live, in the order a probe should try them.
//
// A bundles root's `v` segment is the FORMAT VERSION: the single-file document
// form is format v1, the tree form is format v2, and each format migration adds
// the next root, migrates, then deletes the old one. An overlap is therefore
// normal and its length varies per migration, so more than one root can hold a
// real tree at the same time and resolution must consider all of them.
//
// The list is ordered NEWEST FORMAT FIRST: during an overlap a bundle that has
// been migrated is the one the publisher means, and a probe that answered from
// the older root would keep serving the copy the migration is retiring.
//
// It stays a PROBE rather than a search: the candidates are enumerated from the
// layout accessors, not discovered by walking, so a bundle of a given name has
// exactly one possible location per format and no other.
//
// filePath is the bundle's single-file path in either of the two prefix
// families a reference is built in — repo-relative or content-root-relative
// (see Reference.BuildFilePath) — and the candidates are composed in the same
// family the path arrived in. A path under neither yields the extension-trimmed
// path alone, which is all that can honestly be said about a location this
// layout does not describe.
func BundleTreeRoots(filePath string) []string {
	trimmed := strings.TrimSuffix(filePath, ".yaml")
	for _, fam := range []struct {
		root      string
		prefixFor func(paths.BundleLayout) string
	}{
		{paths.RepoBundlesRoot(), paths.RepoBundlesPrefixFor},
		{paths.ContentBundlesRoot(), paths.ContentBundlesPrefixFor},
	} {
		rel, ok := strings.CutPrefix(trimmed, fam.root+"/")
		if !ok {
			continue
		}
		// The name is taken RELATIVE TO THE ROOT that contains every format's
		// subtree and then reduced, so a path that already carries a format
		// segment yields the same bare name as one that does not — otherwise a
		// probe would compose a root with the segment in it twice.
		name := paths.TrimBundlesLayoutSegment(rel)
		roots := make([]string, 0, 2)
		for _, l := range []paths.BundleLayout{paths.LayoutV2, paths.LayoutV1} {
			root := path.Join(fam.prefixFor(l), name)
			if !slices.Contains(roots, root) {
				roots = append(roots, root)
			}
		}
		return roots
	}
	return []string{trimmed}
}

// ProbeBundleTreeRoots runs probe against each root BundleTreeRoots names, in
// order, and reports the first one that ANSWERS, together with the root it
// answered from.
//
// A candidate is adopted only on POSITIVE evidence that it holds the tree: any
// failure moves to the next root. That rule is forced rather than chosen —
// classifying a tree fetch's failure as "absent" versus "broken" means reading
// internal/content's sentinel, and the content layer sits above this package —
// so an unclassifiable error cannot be allowed to stop a probe that has another
// root left to try.
//
// When no root answers, the failure quotes EVERY root tried, and the root
// returned is the LAST candidate. Reporting only one root is how a publisher
// mid-migration is told their bundle is missing from a place they have already
// left, and reporting only the last failure would hide a tree that exists but
// cannot be read.
func ProbeBundleTreeRoots[T any](filePath string, probe func(root string) (T, error)) (result T, root string, err error) {
	roots := BundleTreeRoots(filePath)
	failures := make([]error, 0, len(roots))
	for _, root := range roots {
		got, perr := probe(root)
		if perr == nil {
			return got, root, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", root, perr))
	}
	var zero T
	return zero, roots[len(roots)-1], errors.Join(failures...)
}
