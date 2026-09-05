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
// # v1 currently DIVERGES, on purpose, and the divergence is bounded
//
// LayoutV2 resolves to the same v2 segment in all three. LayoutV1 does not: the
// LOCAL tree has relocated its v1 bundles into v1/, while the published prefix
// and the cache install root still answer with the bare bundles directory. That
// is the one deliberate exception to the paragraph above, it is temporary, and
// LocalSegment carries the reasoning and the condition that ends it. The two
// resolvers are LocalSegment and publishedSegment; every accessor names which
// one it uses, so the exception cannot spread by accident.
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

// ErrUnknownBundleLayout is returned — and panicked with, see mustLocalSegment
// — for a BundleLayout that names no layout: the zero value, or an integer
// nothing minted. It is a sentinel so a caller tests for it rather than
// matching a message.
var ErrUnknownBundleLayout = errors.New("not a known bundle layout")

// The directory names that separate the two layouts beneath a bundles root.
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

// LocalSegment is the path segment this layout adds beneath the LOCAL,
// committed content bundles root — the tree a project AUTHORS in. Every layout
// contributes one: v1/ and v2/ are siblings, and no bundle sits loose at the
// root between them.
//
// # This deliberately DISAGREES with publishedSegment, and the disagreement is temporary
//
// The local tree's v1 bundles have been relocated into v1/; the published repo
// prefix (RepoBundlesPrefixFor) and the cache install root
// (CacheBundlesPathFor) have NOT moved, so their v1 is still the bare bundles
// directory. That asymmetry is the whole reason there are two resolvers here
// instead of one.
//
// It is not a preference. Flipping the published prefix renames the path every
// EXISTING consumer of an unmoved bundle repo already fetches from, and no gate
// in this repo exercises a real cross-repo pull — so that half is a human
// decision, made with the bundle repos in hand, not something to infer from
// this side of the wire.
//
// The two sides are independently safe to move because they are different
// ROOTS, not two names for one: a pull installs under CacheBundlesPath
// (gitignored, derived) and authoring reads LocalBundlesPath (committed). A
// project can therefore find its own relocated bundles while still installing
// remote ones exactly where the publisher writes them.
//
// When the published half moves, these two collapse back into one resolver and
// BundleLayout's doc — "these three agree" as a property of the code — is true
// again without qualification.
func (l BundleLayout) LocalSegment() (string, error) {
	switch l {
	case LayoutV1:
		return layoutV1Segment, nil
	case LayoutV2:
		return layoutV2Segment, nil
	default:
		return "", fmt.Errorf("paths: %w: %d", ErrUnknownBundleLayout, int(l))
	}
}

// publishedSegment is the segment this layout adds beneath the PUBLISHED repo
// prefix and the CACHE install root. LayoutV1 adds NOTHING there: a published
// bundle repo's single-file bundles have not been relocated, so a consumer
// still fetches them from the bare bundles directory. See LocalSegment for why
// this side has not moved with the local one.
func (l BundleLayout) publishedSegment() (string, error) {
	switch l {
	case LayoutV1:
		return "", nil
	case LayoutV2:
		return layoutV2Segment, nil
	default:
		return "", fmt.Errorf("paths: %w: %d", ErrUnknownBundleLayout, int(l))
	}
}

// mustLocalSegment and mustPublishedSegment panic on a layout that names
// nothing.
//
// The accessors below return a bare path because every caller has one correct
// answer and threading an error through them buys nothing: a BundleLayout is a
// closed enum minted in this package, so an unrecognised one is a programming
// error and not a runtime condition. The alternative — defaulting an unknown
// layout to v1 — is the silent wrong answer this whole type exists to prevent.
func (l BundleLayout) mustLocalSegment() string {
	seg, err := l.LocalSegment()
	if err != nil {
		panic(err.Error())
	}
	return seg
}

func (l BundleLayout) mustPublishedSegment() string {
	seg, err := l.publishedSegment()
	if err != nil {
		panic(err.Error())
	}
	return seg
}

// LocalBundlesPathFor returns one layout's subtree of the COMMITTED
// authored-bundles directory. Both layouts are a real subdirectory: LayoutV1 is
// LocalBundlesPath/v1, LayoutV2 is LocalBundlesPath/v2.
func LocalBundlesPathFor(appPath string, l BundleLayout) string {
	return BundlesLayoutRoot(LocalBundlesPath(appPath), l)
}

// CacheBundlesPathFor returns one layout's subtree of the CACHE bundles
// directory — the install root a pull writes into. LayoutV1 is
// CacheBundlesPath unchanged; see LocalSegment for why this has not moved with
// the local tree.
func CacheBundlesPathFor(appPath string, l BundleLayout) string {
	if seg := l.mustPublishedSegment(); seg != "" {
		return filepath.Join(CacheBundlesPath(appPath), seg)
	}
	return CacheBundlesPath(appPath)
}

// RepoBundlesPrefixFor returns one layout's repo-relative bundles prefix — the
// prefix a PUBLISHING repo exposes. LayoutV1 is the bare bundles prefix
// unchanged; see LocalSegment for why this has not moved with the local tree.
//
// It joins with path rather than filepath: this is a REPO-relative path that
// travels to a git host and is compared against forward-slash refs, so it must
// not pick up a separator from whatever OS happens to be publishing.
func RepoBundlesPrefixFor(l BundleLayout) string {
	if seg := l.mustPublishedSegment(); seg != "" {
		return path.Join(RepoContentPrefix, BundlesDir, seg)
	}
	return path.Join(RepoContentPrefix, BundlesDir)
}

// BundlesLayoutRoot returns the subdirectory of an ALREADY-RESOLVED LOCAL
// bundles root that holds one layout. It is the shared body of
// LocalBundlesPathFor, exported for the reader, which is handed its search
// directories and never an appPath.
//
// LOCAL only: it resolves through LocalSegment, so handing it a cache bundles
// root would place v1 somewhere no pull writes.
func BundlesLayoutRoot(bundlesDir string, l BundleLayout) string {
	return filepath.Join(bundlesDir, l.mustLocalSegment())
}
