package paths

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
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

// layoutV2Segment is the directory name that separates the two layouts beneath
// a bundles root.
const layoutV2Segment = "v2"

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
// LayoutV1 adds NOTHING today: the single-file bundles have not moved, so
// LayoutV1 resolves to exactly the path LocalBundlesPath has always returned and
// every accessor below is a no-op for it. When v1 is relocated into its own
// directory this is the ONE line that changes, and the move and this segment
// must land in the same commit — a window in which the bytes are in one place
// and the resolver looks in another produces a bundle that resolves nowhere.
func (l BundleLayout) Segment() (string, error) {
	switch l {
	case LayoutV1:
		return "", nil
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
// authored-bundles directory. LayoutV1 is LocalBundlesPath unchanged.
func LocalBundlesPathFor(appPath string, l BundleLayout) string {
	return BundlesLayoutRoot(LocalBundlesPath(appPath), l)
}

// CacheBundlesPathFor returns one layout's subtree of the CACHE bundles
// directory — the install root a pull writes into. LayoutV1 is
// CacheBundlesPath unchanged.
func CacheBundlesPathFor(appPath string, l BundleLayout) string {
	return BundlesLayoutRoot(CacheBundlesPath(appPath), l)
}

// RepoBundlesPrefixFor returns one layout's repo-relative bundles prefix — the
// publishing half of the same layout LocalBundlesPathFor gives a project.
//
// It joins with path rather than filepath: this is a REPO-relative path that
// travels to a git host and is compared against forward-slash refs, so it must
// not pick up a separator from whatever OS happens to be publishing.
func RepoBundlesPrefixFor(l BundleLayout) string {
	if seg := l.mustSegment(); seg != "" {
		return path.Join(RepoContentPrefix, BundlesDir, seg)
	}
	return path.Join(RepoContentPrefix, BundlesDir)
}

// BundlesLayoutRoot returns the subdirectory of an ALREADY-RESOLVED bundles
// root that holds one layout. It is the shared body of the three accessors
// above, exported for the reader, which is handed its search directories and
// never an appPath.
func BundlesLayoutRoot(bundlesDir string, l BundleLayout) string {
	if seg := l.mustSegment(); seg != "" {
		return filepath.Join(bundlesDir, seg)
	}
	return bundlesDir
}
