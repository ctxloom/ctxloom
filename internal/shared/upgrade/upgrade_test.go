package upgrade

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// renameUpgrade is a test-only Upgrader that renames one top-level key,
// honoring the idempotent no-op contract (returns false when the key is absent).
type renameUpgrade struct {
	name     string
	from, to string
}

func (r renameUpgrade) Name() string { return r.name }

func (r renameUpgrade) Apply(root *yaml.Node) bool {
	v := MapValue(root, r.from)
	if v == nil {
		return false
	}
	MapDelete(root, r.from)
	MapSet(root, r.to, v)
	return true
}

// versionStampUpgrade bumps a version-gated step: a no-op once the doc is at or
// past target, otherwise stamps the version (and is what every real versioned
// upgrader looks like).
type versionStampUpgrade struct{ target int }

func (versionStampUpgrade) Name() string { return "version-stamp" }

func (u versionStampUpgrade) Apply(root *yaml.Node) bool {
	if v, ok := Version(root, "version"); !ok || v >= u.target {
		return false
	}
	SetVersion(root, "version", u.target)
	return true
}

func TestPipeline_Run_AppliesStagesInOrder(t *testing.T) {
	// b renames a key only a produced, proving stages compose front-to-back.
	p := Pipeline{
		renameUpgrade{name: "a", from: "one", to: "two"},
		renameUpgrade{name: "b", from: "two", to: "three"},
	}
	out, applied := p.Run([]byte("one: x\n"))
	assert.Equal(t, []string{"a", "b"}, applied)

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	assert.NotContains(t, root, "one")
	assert.NotContains(t, root, "two")
	assert.Equal(t, "x", root["three"])
}

func TestPipeline_Run_NoStageApplies_ReturnsBytesVerbatim(t *testing.T) {
	in := []byte("# pristine\nkept: yes\n")
	p := Pipeline{renameUpgrade{name: "a", from: "absent", to: "x"}}
	out, applied := p.Run(in)
	assert.Empty(t, applied)
	assert.Same(t, &in[0], &out[0], "no-op must return the same backing bytes, not a copy")
}

func TestPipeline_Run_MalformedYAML_ReturnsVerbatim(t *testing.T) {
	in := []byte("key: [unterminated\n")
	p := Pipeline{renameUpgrade{name: "a", from: "key", to: "x"}}
	out, applied := p.Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}

func TestPipeline_Run_PreservesCommentsAndIndent(t *testing.T) {
	// Comments on untouched structure survive the parse/re-encode round-trip
	// (a renamed key's own head comment is not guaranteed — that's why the
	// comment here sits on the sibling key that the upgrade leaves alone).
	in := []byte("# top comment\nkept: 1\nold:\n  nested: 2\n")
	p := Pipeline{renameUpgrade{name: "a", from: "old", to: "new"}}
	out, applied := p.Run(in)
	require.Equal(t, []string{"a"}, applied)
	assert.Contains(t, string(out), "# top comment")
	assert.Contains(t, string(out), "new:")
	assert.NotContains(t, string(out), "old:")
}

func TestPipeline_Run_Idempotent_SecondRunIsNoOp(t *testing.T) {
	p := Pipeline{renameUpgrade{name: "a", from: "old", to: "new"}}
	once, applied := p.Run([]byte("old: v\n"))
	require.NotEmpty(t, applied)
	twice, appliedAgain := p.Run(once)
	assert.Empty(t, appliedAgain, "already-upgraded input must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// A multi-document stream must survive Run byte-for-byte. yaml.Unmarshal into a
// yaml.Node silently decodes only the FIRST document of a stream and reports no
// error, so re-encoding that node emits a single-document file: every later
// document is deleted. The upgraded bytes are handed to the caller as
// Pending.Data and written verbatim over the user's file on consent, so the loss
// is permanent and silent.
func TestPipeline_Run_MultiDocumentStream_ReturnsVerbatim(t *testing.T) {
	in := []byte("one: x\n---\nsecond: doc\n")
	p := Pipeline{renameUpgrade{name: "a", from: "one", to: "two"}}
	out, applied := p.Run(in)

	assert.Empty(t, applied, "a multi-document stream must not report an upgrade it cannot safely re-encode")
	assert.Equal(t, string(in), string(out), "every document in the stream must survive")
}

// A duplicate mapping key must stop the pipeline dead. The DOM helpers all act
// on the FIRST match, so renaming `old` in "old: 1\nold: 2\n" produced
// "old: 2\nnew: 1\n": a document that no longer has a duplicate key, therefore
// parses cleanly, and carries the migrated value taken from one of the two
// entries while the legacy key survives holding the other. That converts a loud
// duplicate-key parse error into a silent load of the wrong values, and the
// rewritten bytes are what the user is offered to persist.
func TestPipeline_Run_DuplicateKey_ReturnsVerbatim(t *testing.T) {
	in := []byte("one: first\none: second\n")
	p := Pipeline{renameUpgrade{name: "a", from: "one", to: "two"}}
	out, applied := p.Run(in)

	assert.Empty(t, applied, "a document with a duplicate key must not be silently normalized")
	assert.Equal(t, string(in), string(out))
}

// The same rule applies below the root: the helpers walk nested mappings too.
func TestPipeline_Run_NestedDuplicateKey_ReturnsVerbatim(t *testing.T) {
	in := []byte("outer:\n  dup: 1\n  dup: 2\n")
	p := Pipeline{renameUpgrade{name: "a", from: "outer", to: "renamed"}}
	out, applied := p.Run(in)

	assert.Empty(t, applied)
	assert.Equal(t, string(in), string(out))
}

// A key repeated in two DIFFERENT mappings is not a duplicate and must still
// upgrade — the check is per-mapping, not per-document.
func TestPipeline_Run_SameKeyInDifferentMappings_StillUpgrades(t *testing.T) {
	in := []byte("one:\n  name: a\ntwo:\n  name: b\n")
	p := Pipeline{renameUpgrade{name: "a", from: "one", to: "renamed"}}
	_, applied := p.Run(in)

	assert.Equal(t, []string{"a"}, applied)
}

func TestVersion_MissingIsZero_RoundTrips(t *testing.T) {
	p := Pipeline{versionStampUpgrade{target: 2}}

	// Unversioned doc upgrades by gaining version: 2.
	out, applied := p.Run([]byte("k: v\n"))
	require.Equal(t, []string{"version-stamp"}, applied)
	assert.Contains(t, string(out), "version: 2")

	// Re-running is a no-op once stamped.
	_, again := p.Run(out)
	assert.Empty(t, again)

	// A doc already at a higher version is left alone.
	_, none := p.Run([]byte("version: 5\nk: v\n"))
	assert.Empty(t, none)
}

func TestSetVersion_IsIntScalar(t *testing.T) {
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("k: v\n"), &doc))
	root := doc.Content[0]
	SetVersion(root, "version", 3)
	v, ok := Version(root, "version")
	assert.True(t, ok)
	assert.Equal(t, 3, v)
}

// "the key is absent" and "the key is present and unreadable" are different
// facts. Only the first is generation 0; the second must not be migrated as if
// it were, because doing so re-runs every step over a probably-corrupt document
// and stamps the current version on the way out.
func TestVersion_PresentButUnreadable_IsNotGenerationZero(t *testing.T) {
	parse := func(t *testing.T, doc string) *yaml.Node {
		t.Helper()
		var d yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(doc), &d))
		return d.Content[0]
	}

	v, ok := Version(parse(t, "k: v\n"), "version")
	assert.True(t, ok, "an absent version key is the pre-versioning generation")
	assert.Equal(t, 0, v)

	for _, doc := range []string{
		"version: banana\nk: v\n",
		"version: 6.5\nk: v\n",
		"version: \"\"\nk: v\n",
		"version:\n  nested: 6\n",
		"version:\n  - 6\n",
	} {
		_, ok := Version(parse(t, doc), "version")
		assert.False(t, ok, "input %q", doc)
	}

	v, ok = Version(parse(t, "version: 4\n"), "version")
	assert.True(t, ok)
	assert.Equal(t, 4, v)
}
