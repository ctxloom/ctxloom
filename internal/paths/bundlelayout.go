package paths

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BundleLayout names one of the two on-disk shapes a bundle is stored in.
//
// # Why a versioned accessor rather than a second pair of constants
//
// The bundles root appears in THREE places — the project's committed content
// tree (LocalBundlesPathFor), the prefix a publishing repo exposes
// (RepoBundlesPrefixFor), and the install root a pull writes into
// (CacheBundlesPathFor). LocalBundlesPath's doc states the invariant those three
// share: "a bundle repo and a consuming project lay their bundles out
// identically." A layout split applied to only one of them breaks that
// invariant SILENTLY — a consuming project looks somewhere the publisher does
// not write, and the symptom is a bundle that resolves to nothing rather than an
// error anyone can read. Routing all three through one enum is what makes
// "these three agree" a property of the code instead of a thing to remember.
//
// # The other v1 on these paths versions something else entirely
//
// A real path after this change reads
//
//	bundles/v2/<name>/.sigs/SHA256SUMS.publish.v1.ctxloom.dev.<hash>.sig
//
// and the two numbers version DIFFERENT AXES. The one this type names is the
// on-disk LAYOUT. The one inside a signature filename is signing.NamespacePublish
// — the signature SCHEME's namespace, which has nothing to do with where a file
// sits. Neither moves when the other does.
type BundleLayout int

const (
	// LayoutUnknown is the zero value and names no layout. It exists so that a
	// bundle read from somewhere with no layout at all — an installed remote
	// tree, a companion, a builtin compiled into the binary — reports "no
	// layout" rather than being silently attributed to one of the real ones.
	// Every accessor here REFUSES it.
	LayoutUnknown BundleLayout = 0

	// LayoutV1 is the single-file document form: <name>.yaml with a sibling
	// <name>.yaml.sig.
	LayoutV1 BundleLayout = 1

	// LayoutV2 is the tree form: <name>/bundle.yaml plus item files, a
	// SHA256SUMS manifest and a .sigs/ directory.
	LayoutV2 BundleLayout = 2
)

// ErrUnknownBundleLayout is returned — and panicked with, see mustSegment — for
// a BundleLayout that names no layout: the zero value, or an integer nothing
// minted. It is a sentinel so a caller tests for it rather than matching a
// message.
var ErrUnknownBundleLayout = errors.New("not a known bundle layout")

// The directory name each FORMAT occupies beneath a bundles root.
//
// The `v` here is the FORMAT VERSION, not a file shape: v1 holds single-file
// documents AND directories that still carry inline item keys, v2 holds only
// TRUE TREES (no inline item keys, items as files), and the next format
// migration adds the next root. A directory is therefore not by itself v2 —
// treeFormEnvelope's rule, not the entry's type, decides.
const (
	layoutV1Segment = "v1"
	layoutV2Segment = "v2"
)

// String names the layout for a diagnostic.
func (l BundleLayout) String() string {
	switch l {
	case LayoutV1:
		return "v1"
	case LayoutV2:
		return "v2"
	default:
		return "unknown"
	}
}

// Segment is the path segment this layout adds beneath a bundles root.
//
// EVERY layout has one, so the bundles root itself holds no bundles: it is the
// parent the format roots are siblings under. Nothing may resolve a bundle
// against the bare root, because a bundle sitting there belongs to no format
// and is invisible to the reader.
func (l BundleLayout) Segment() (string, error) {
	switch l {
	case LayoutV1:
		return layoutV1Segment, nil
	case LayoutV2:
		return layoutV2Segment, nil
	default:
		return "", fmt.Errorf("paths: %w: %d", ErrUnknownBundleLayout, int(l))
	}
}

// mustSegment panics on a layout that names nothing.
//
// The accessors below return a bare path because every caller has one correct
// answer and threading an error through them buys nothing: a BundleLayout is a
// closed enum minted in this package, so an unrecognised one is a programming
// error and not a runtime condition. The alternative — defaulting an unknown
// layout to v1 — is the silent wrong answer this whole type exists to prevent.
func (l BundleLayout) mustSegment() string {
	seg, err := l.Segment()
	if err != nil {
		panic(err.Error())
	}
	return seg
}

// LocalBundlesPathFor returns one layout's subtree of the COMMITTED
// authored-bundles directory.
func LocalBundlesPathFor(appPath string, l BundleLayout) string {
	return BundlesLayoutRoot(LocalBundlesPath(appPath), l)
}

// CacheBundlesPathFor returns one layout's subtree of the CACHE bundles
// directory — the install root a pull writes into.
func CacheBundlesPathFor(appPath string, l BundleLayout) string {
	return BundlesLayoutRoot(CacheBundlesPath(appPath), l)
}

// ContentBundlesPrefixFor returns one layout's bundles prefix RELATIVE TO THE
// CONTENT ROOT — the same subtree RepoBundlesPrefixFor names, minus the
// leading RepoContentPrefix.
//
// It exists because a reference resolved against an already-open content root
// must not re-state that root, and rebuilding such a path by trimming
// RepoContentPrefix back off is a second expression of the layout that can
// disagree with the first.
//
// It joins with path rather than filepath: these are REPO-relative paths that
// travel to a git host and are compared against forward-slash refs, so they
// must not pick up a separator from whatever OS happens to be publishing.
func ContentBundlesPrefixFor(l BundleLayout) string {
	return path.Join(BundlesDir, l.mustSegment())
}

// RepoBundlesPrefixFor returns one layout's repo-relative bundles prefix — the
// publishing half of the same layout LocalBundlesPathFor gives a project.
func RepoBundlesPrefixFor(l BundleLayout) string {
	return path.Join(RepoContentPrefix, ContentBundlesPrefixFor(l))
}

// RepoBundlesRoot returns the repo-relative directory that CONTAINS every
// layout's bundles subtree — the parent RepoBundlesPrefixFor composes a segment
// underneath.
//
// This is the root a LISTING walks, and it is deliberately not any one layout's
// prefix: a walk anchored at one layout cannot see the other, so a repo
// publishing both forms would list half its bundles and report success. Names
// harvested from such a walk are relative to THIS root and therefore still
// carry a layout segment — reduce them with TrimBundlesLayoutSegment.
func RepoBundlesRoot() string {
	return path.Join(RepoContentPrefix, BundlesDir)
}

// ContentBundlesRoot returns the bundles root RELATIVE TO THE CONTENT ROOT —
// what RepoBundlesRoot names, for a reader that has already resolved
// .ctxloom/content/ itself.
//
// Like RepoBundlesRoot this is the parent of every layout, not one layout's
// prefix, because it is what a LISTING walks.
func ContentBundlesRoot() string {
	return BundlesDir
}

// bundleLayouts is every layout that names a real on-disk shape, longest
// segment first so TrimBundlesLayoutSegment cannot strip a shorter segment that
// happens to prefix a longer one.
//
// LayoutUnknown is absent by construction: it names no layout and its segment
// accessor refuses.
// BundleLayouts is every layout that names a real on-disk FORMAT root.
//
// It exists so a caller that must visit EVERY format root — a second
// enumeration such as `sign --all`'s — derives the set from here instead of
// listing the layouts itself. A hand-listed set is how one enumeration comes to
// disagree with another, and the disagreement is silent: the listing reports
// success having seen a subset.
func BundleLayouts() []BundleLayout {
	return bundleLayouts()
}

func bundleLayouts() []BundleLayout {
	ls := []BundleLayout{LayoutV1, LayoutV2}
	sort.SliceStable(ls, func(i, j int) bool {
		return len(ls[i].mustSegment()) > len(ls[j].mustSegment())
	})
	return ls
}

// TrimBundlesLayoutSegment reduces a path relative to RepoBundlesRoot to the
// item's BARE name, stripping whatever layout segment it sits under.
//
// A listing walks RepoBundlesRoot and names each item by its path relative to
// that root. The moment a layout has a segment, those names come back
// layout-qualified ("v2/atelier"), and a layout-qualified name resolves to
// NOTHING: it is not what the publisher published, not what a lockfile pins,
// and not what a consumer asks for. This is the same defect class that once
// made `sign --all` sign zero bytes while reporting success, so the reduction
// is done HERE, once, rather than at each listing site.
//
// Only a segment followed by a separator is stripped. An item whose own name
// equals a segment ("v2.yaml", listed as "v2") keeps it — trimming that would
// reduce a real bundle to the empty name. A user directory colliding with a
// segment name is indistinguishable from the layout root and is claimed by it;
// that ambiguity is inherent to putting the layouts under the bundles root at
// all, not something this function can resolve.
//
// With LayoutV1's segment empty this is the identity for every v1 path, which
// is why it can be adopted before any layout moves.
func TrimBundlesLayoutSegment(rel string) string {
	for _, l := range bundleLayouts() {
		seg := l.mustSegment()
		if trimmed, ok := strings.CutPrefix(rel, seg+"/"); ok {
			return trimmed
		}
	}
	return rel
}

// BundlesLayoutRoot returns the subdirectory of an ALREADY-RESOLVED bundles
// root that holds one layout. It is the shared body of the three accessors
// above, exported for the reader, which is handed its search directories and
// never an appPath.
func BundlesLayoutRoot(bundlesDir string, l BundleLayout) string {
	return filepath.Join(bundlesDir, l.mustSegment())
}
