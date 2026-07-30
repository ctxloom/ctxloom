package bundles

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBundleWarner_DedupesPerRef verifies a ref warns once even across repeated
// calls (startup assembles context more than once via independent loaders).
func TestBundleWarner_DedupesPerRef(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner(&buf)
	err := errors.New("bundle not found")

	w.unresolved("personal/core-practices", err)
	w.unresolved("personal/core-practices", err)
	w.unresolved("personal/developer-mindset", err)

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/core-practices"`), "repeated ref must warn once")
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/developer-mindset"`))
	assert.Equal(t, 2, strings.Count(out, "skipping unresolved bundle"))
}

// TestBundleWarner_UnresolvedAndAmbiguousDoNotShareAKeyspace is U031-F19.
//
// One `seen` map served two kinds of warning asymmetrically: `unresolved` keyed
// on the bare ref, `ambiguous` keyed on the string "ambiguous:"+name. A bundle
// ref literally named "ambiguous:foo" therefore occupied the same key as the
// ambiguity warning for fragment "foo", and whichever fired first silenced the
// other. String-prefixed namespacing inside a shared keyspace is a collision
// waiting for the right name; a typed key has no such shape.
func TestBundleWarner_UnresolvedAndAmbiguousDoNotShareAKeyspace(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner(&buf)

	w.unresolved("ambiguous:foo", errors.New("bundle not found"))
	w.ambiguous("foo", []string{"a", "b"}, "a")

	out := buf.String()
	assert.Contains(t, out, "skipping unresolved bundle", "the unresolved-bundle warning must be emitted")
	assert.Contains(t, out, "exists in multiple bundles",
		"the ambiguity warning must not be suppressed by an unrelated bundle ref that merely spells its dedup key")
}

// TestBundleWarner_AmbiguousDedupesPerName pins that collapsing the two
// keyspaces did not cost the ambiguous warning its own dedup.
func TestBundleWarner_AmbiguousDedupesPerName(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner(&buf)

	w.ambiguous("shared", []string{"a", "b"}, "a")
	w.ambiguous("shared", []string{"a", "b"}, "a")
	w.ambiguous("other", []string{"a", "b"}, "a")

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "exists in multiple bundles"))
	assert.Equal(t, 1, strings.Count(out, `fragment "shared"`))
}
