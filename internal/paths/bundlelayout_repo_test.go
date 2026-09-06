package paths

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepoBundlesRoot_ContainsEveryLayout is the invariant the listing root
// exists for: EVERY layout's prefix must sit at or under the root a listing
// walks. A layout whose prefix escapes the root is invisible to every listing —
// the repo lists a subset of its bundles and reports success.
//
// Asserted over the layouts rather than over today's two values, so a third
// layout added without a root that contains it fails here.
func TestRepoBundlesRoot_ContainsEveryLayout(t *testing.T) {
	root := RepoBundlesRoot()
	for _, l := range bundleLayouts() {
		t.Run(l.String(), func(t *testing.T) {
			prefix := RepoBundlesPrefixFor(l)
			assert.True(t, prefix == root || strings.HasPrefix(prefix, root+"/"),
				"%s publishes to %q, which is not under the listing root %q", l, prefix, root)
		})
	}
}

// TestRepoBundlesPrefix_ComposesContentPrefix pins that the two prefix
// accessors are one expression, not two that happen to agree: the repo-relative
// prefix must be exactly the content root joined to the content-relative one.
//
// They are used on opposite sides of the same fetch — a repo read builds the
// full path, a local read resolves against an already-open content root — so
// two independent expressions here means a local and a remote read of the same
// bundle can disagree about where it lives.
func TestRepoBundlesPrefix_ComposesContentPrefix(t *testing.T) {
	for _, l := range bundleLayouts() {
		t.Run(l.String(), func(t *testing.T) {
			assert.Equal(t, RepoContentPrefix+"/"+ContentBundlesPrefixFor(l), RepoBundlesPrefixFor(l))
		})
	}
}

// TestBundleLayoutPaths_LiteralValues pins the bytes. These are wire values —
// the directory every published repo already committed into — so a change to
// them must be a deliberate edit here, not a side effect somewhere else.
func TestBundleLayoutPaths_LiteralValues(t *testing.T) {
	assert.Equal(t, ".ctxloom/content/bundles", RepoBundlesRoot())
	assert.Equal(t, ".ctxloom/content/bundles/v1", RepoBundlesPrefixFor(LayoutV1))
	assert.Equal(t, ".ctxloom/content/bundles/v2", RepoBundlesPrefixFor(LayoutV2))
	assert.Equal(t, "bundles/v1", ContentBundlesPrefixFor(LayoutV1))
	assert.Equal(t, "bundles/v2", ContentBundlesPrefixFor(LayoutV2))
}

// TestTrimBundlesLayoutSegment_RemovesExactlyTheLayoutSegment drives the
// reduction from the layouts themselves, so it keeps testing something real
// once LayoutV1 stops being the empty segment.
func TestTrimBundlesLayoutSegment_RemovesExactlyTheLayoutSegment(t *testing.T) {
	const name = "lang/go/testing"
	for _, l := range bundleLayouts() {
		t.Run(l.String(), func(t *testing.T) {
			seg, err := l.Segment()
			require.NoError(t, err)

			qualified := name
			if seg != "" {
				qualified = seg + "/" + name
			}
			assert.Equal(t, name, TrimBundlesLayoutSegment(qualified),
				"a name listed under %s must reduce to the name a consumer asks for", l)
		})
	}
}

// TestTrimBundlesLayoutSegment_LeavesNonLayoutPathsAlone is the other half: the
// trim must not eat a path that merely resembles a layout root. Without the
// separator requirement, a bundle whose own name IS a segment reduces to the
// empty string — a bundle that resolves to nothing, reported as success.
func TestTrimBundlesLayoutSegment_LeavesNonLayoutPathsAlone(t *testing.T) {
	assert.Equal(t, "lang/go/testing", TrimBundlesLayoutSegment("lang/go/testing"))

	for _, l := range bundleLayouts() {
		seg, err := l.Segment()
		require.NoError(t, err)
		if seg == "" {
			continue
		}
		assert.Equal(t, seg, TrimBundlesLayoutSegment(seg),
			"a bundle NAMED %q is an item, not a layout root", seg)
		assert.Equal(t, seg+"x/thing", TrimBundlesLayoutSegment(seg+"x/thing"),
			"only a whole segment followed by a separator is a layout root")
	}
}

// TestBundleLayouts_OrderedLongestSegmentFirst pins the ordering the trim
// depends on. If one segment ever prefixes another ("v1" and "v10"), scanning
// shortest-first strips the wrong one and yields a corrupt name; ordering is
// the entire defence and nothing else checks it.
func TestBundleLayouts_OrderedLongestSegmentFirst(t *testing.T) {
	ls := bundleLayouts()
	require.NotEmpty(t, ls)
	for i := 1; i < len(ls); i++ {
		prev, err := ls[i-1].Segment()
		require.NoError(t, err)
		cur, err := ls[i].Segment()
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(prev), len(cur),
			"bundleLayouts must be ordered longest segment first")
	}
}
