package remote

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The defect here IS the absent named type, so a test that spells it cannot
// be RED first -- it fails to compile, which is not the same thing. So the
// behaviour AllEntries owes its callers is pinned here first, at the public
// seam, and the pin is unchanged by the collapse: it
// reads only the field names, which a named struct keeps.
//
// Five call sites across internal/cli and internal/operations range over this
// result and read .Type, .Ref and .Entry -- including the rebuild in
// operations/lockfile.go that carries Pinned and Retracted forward, where
// losing a field would silently un-hold or un-retract content.
func TestLockfile_AllEntries_CarriesKeyTypeAndEntry(t *testing.T) {
	lockfile := &Lockfile{
		Version: 1,
		Bundles: map[string]LockEntry{
			"alice/go-tools": {SHA: "sha-a", URL: "https://github.com/alice/ctxloom", Held: true},
			"bob/py-tools":   {SHA: "sha-b", URL: "https://github.com/bob/ctxloom", Retracted: true, RetractedReason: "withdrawn"},
		},
	}

	entries := lockfile.AllEntries()
	require.Len(t, entries, 2)

	byRef := map[string]LockEntry{}
	var refs []string
	for _, e := range entries {
		assert.Equal(t, ItemTypeBundle, e.Type, "only bundles are locked")
		byRef[e.Ref] = e.Entry
		refs = append(refs, e.Ref)
	}
	sort.Strings(refs)
	assert.Equal(t, []string{"alice/go-tools", "bob/py-tools"}, refs,
		"the map KEY is the ref; callers rebuild the lockfile from it")

	assert.True(t, byRef["alice/go-tools"].Held, "a user hold must survive the round trip")
	assert.Equal(t, "sha-a", byRef["alice/go-tools"].SHA)
	assert.True(t, byRef["bob/py-tools"].Retracted, "a publisher retraction must survive the round trip")
	assert.Equal(t, "withdrawn", byRef["bob/py-tools"].RetractedReason)
}

func TestLockfile_AllEntries_EmptyIsEmpty(t *testing.T) {
	assert.Empty(t, (&Lockfile{Bundles: map[string]LockEntry{}}).AllEntries())
}

// The point of the collapse: the element type can now be named, so a caller
// can hold or pass one. Pre-fix this did not compile.
func TestLockfile_AllEntries_ElementTypeIsNameable(t *testing.T) {
	// This helper is the whole assertion: a function taking one entry cannot
	// be written at all while the element type is anonymous, which is what
	// forced every caller to range over the result in place.
	describe := func(e LockedEntry) (ItemType, string, string) { return e.Type, e.Ref, e.Entry.SHA }

	entries := (&Lockfile{Bundles: map[string]LockEntry{"a/b": {SHA: "s"}}}).AllEntries()
	require.Len(t, entries, 1)

	kind, ref, sha := describe(entries[0])
	assert.Equal(t, ItemTypeBundle, kind)
	assert.Equal(t, "a/b", ref)
	assert.Equal(t, "s", sha)
}
