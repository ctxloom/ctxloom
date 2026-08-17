package bundles

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The filesystem adapter must satisfy the Store port (ADR 0026).
var _ Store = (*fsStore)(nil)

func TestFSStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFSStore(nil, []string{dir})

	b := &Bundle{
		Path:      filepath.Join(dir, "rt.yaml"),
		Version:   "1.0",
		Fragments: map[string]BundleFragment{"a": {Content: "hello"}},
	}
	if err := store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Load("rt")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != "1.0" || got.Fragments["a"].Content != "hello" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := store.Delete("rt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load("rt"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

// TestFSStore_Delete_DirectoryFormBundleLeavesItsSubtreesOnDisk is U031-F13,
// CHARACTERIZED, NOT FIXED — this pins what the code does TODAY, not what it
// ought to do. The row is ESCALATED: making Delete remove the whole bundle
// directory would delete user-AUTHORED files (fragments/*.md, commands/,
// skills/<name>/) on a `ctxloom bundle remove`, which is a product decision
// about destroying content, not a sweep's call. Whoever takes that decision
// will need this test to change, and that is the point: the blast radius is
// stated here rather than discovered.
//
// What it pins: fsStore.Delete removes only the file Find resolved. For a
// directory-form bundle that file is `<dir>/bundle.yaml`, so afterwards the
// bundle no longer resolves — Find looks for exactly that file — while every
// fragment, command and skill it shipped is still on disk, reachable by nothing.
func TestFSStore_Delete_DirectoryFormBundleLeavesItsSubtreesOnDisk(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundles"
	require.NoError(t, afero.WriteFile(fsys, dir+"/kit/bundle.yaml",
		[]byte("version: \"1.0\"\n"), 0o644))
	require.NoError(t, afero.WriteFile(fsys, dir+"/kit/fragments/notes.md", []byte("authored\n"), 0o644))
	require.NoError(t, afero.WriteFile(fsys, dir+"/kit/skills/humanize/SKILL.md",
		[]byte("---\nname: humanize\ndescription: d\n---\nbody\n"), 0o644))

	store := NewFSStore(fsys, []string{dir})
	require.NoError(t, store.Delete("kit"))

	gone, err := afero.Exists(fsys, dir+"/kit/bundle.yaml")
	require.NoError(t, err)
	assert.False(t, gone, "the resolved bundle file is what Delete removes")

	frag, err := afero.Exists(fsys, dir+"/kit/fragments/notes.md")
	require.NoError(t, err)
	assert.True(t, frag, "TODAY: an authored fragment outlives the bundle that named it (U031-F13, escalated)")

	skill, err := afero.Exists(fsys, dir+"/kit/skills/humanize/SKILL.md")
	require.NoError(t, err)
	assert.True(t, skill, "TODAY: a skill package outlives the bundle that named it (U031-F13, escalated)")
}
