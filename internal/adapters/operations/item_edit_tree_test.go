package operations

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Item edits on a v2 TREE bundle. A tree is bundle.yaml (envelope only) plus
// one file per item; an edit must land in the edited item's file and nowhere
// else. Writing the whole bundle back as one document would put its items
// inline in bundle.yaml — the half-migrated shape readEnvelope refuses — so the
// bundle stops loading at all.

// vaultTree creates tree bundle "vault" with two fragments and returns the
// bundle directory.
func vaultTree(t *testing.T) (afero.Fs, *config.Config, string, func() bundles.BundleRead) {
	t.Helper()
	fsys, cfg, appPath := treeAuthoringFixture(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "vault", Tree: true, FS: fsys,
		Fragments: map[string]BundleFragmentInput{
			"house-style": {Content: "HOUSE-BODY-MARKER\n", Notes: "old notes", Tags: []string{"keep"}, NoDistill: true},
			"other":       {Content: "OTHER-BODY-MARKER\n", Premise: "You are about to do the other thing.", NoDistill: true},
		},
	})
	require.NoError(t, err)
	dir := filepath.Join(paths.LocalBundlesPathFor(appPath, paths.LayoutV2), "vault")
	return fsys, cfg, dir, func() bundles.BundleRead { return readBackTree(t, fsys, appPath, "vault") }
}

// snapshotTree maps every file under dir (relative path) to its bytes.
func snapshotTree(t *testing.T, fsys afero.Fs, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := afero.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	}))
	return out
}

// assertOnlyFileChanged asserts before→after differ in exactly one file,
// whose relative path contains want, and returns its new bytes.
func assertOnlyFileChanged(t *testing.T, before, after map[string]string, want string) string {
	t.Helper()
	var changed []string
	for k, v := range after {
		if before[k] != v {
			changed = append(changed, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			changed = append(changed, k+" (removed)")
		}
	}
	require.Len(t, changed, 1, "exactly one file may change; changed: %v", changed)
	require.Contains(t, changed[0], want)
	return after[changed[0]]
}

func TestSetFragmentPremise_TreeBundle_WritesOnlyThatFragmentsFrontmatter(t *testing.T) {
	fsys, cfg, dir, read := vaultTree(t)
	before := snapshotTree(t, fsys, dir)

	_, err := SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{
		Bundle: "vault", Name: "house-style", Premise: "You are about to write prose.", Notes: "new notes",
	})
	require.NoError(t, err)

	after := snapshotTree(t, fsys, dir)
	changed := assertOnlyFileChanged(t, before, after, "house-style")
	assert.Contains(t, changed, "description: You are about to write prose.")
	assert.Contains(t, changed, "notes: new notes")
	assert.True(t, strings.HasSuffix(changed, "HOUSE-BODY-MARKER\n"), "the body is untouched:\n%s", changed)

	isTree, err := bundles.IsTreeFormBundle(context.Background(), fsys, filepath.Join(dir, bundles.DirectoryFormManifest))
	require.NoError(t, err)
	assert.True(t, isTree, "an item edit must not turn a tree into a document")

	got := read().Bundle.Fragments
	assert.Equal(t, "You are about to write prose.", got["house-style"].Premise)
	assert.Equal(t, "new notes", got["house-style"].Notes)
	assert.Equal(t, "HOUSE-BODY-MARKER\n", got["house-style"].Content)
	assert.Equal(t, []string{"keep"}, got["house-style"].Tags)
	assert.Equal(t, "You are about to do the other thing.", got["other"].Premise)
}

func TestSetItemContent_TreeBundle_WritesOnlyThatFragmentsFile(t *testing.T) {
	fsys, cfg, dir, read := vaultTree(t)
	before := snapshotTree(t, fsys, dir)

	_, err := SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "vault", Kind: ItemKindFragment, Name: "other", Content: "EDITED-BODY\n",
	})
	require.NoError(t, err)

	changed := assertOnlyFileChanged(t, before, snapshotTree(t, fsys, dir), "other")
	assert.Contains(t, changed, "description: You are about to do the other thing.")
	got := read().Bundle.Fragments
	assert.Equal(t, "EDITED-BODY\n", got["other"].Content)
	assert.Equal(t, "You are about to do the other thing.", got["other"].Premise)
	assert.Equal(t, "HOUSE-BODY-MARKER\n", got["house-style"].Content)
}

func TestSetItemContent_TreeBundle_ClearsTheStaleDistilledFile(t *testing.T) {
	fsys, cfg, appPath := treeAuthoringFixture(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "vault", Tree: true, FS: fsys,
		Fragments: map[string]BundleFragmentInput{"f": {Content: "v1\n"}},
		Distiller: &recordingDistiller{returnValue: "DISTILLED-V1", returnModel: "mock"},
	})
	require.NoError(t, err)
	dir := filepath.Join(paths.LocalBundlesPathFor(appPath, paths.LayoutV2), "vault")
	before := snapshotTree(t, fsys, dir)
	require.Contains(t, strings.Join(values(before), "\n"), "DISTILLED-V1", "fixture: the distilled form is on disk")

	_, err = SetItemContent(context.Background(), cfg, SetItemContentRequest{Bundle: "vault", Kind: ItemKindFragment, Name: "f", Content: "v2\n"})
	require.NoError(t, err)

	after := snapshotTree(t, fsys, dir)
	assert.NotContains(t, strings.Join(values(after), "\n"), "DISTILLED-V1", "a distilled form of superseded content must not survive on disk")
	got := readBackTree(t, fsys, appPath, "vault").Bundle.Fragments["f"]
	assert.Equal(t, "v2\n", got.Content)
	assert.Empty(t, got.Distilled)
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestUpdateBundle_TreeBundle_EnvelopeEditTouchesOnlyTheEnvelope(t *testing.T) {
	fsys, cfg, dir, read := vaultTree(t)
	before := snapshotTree(t, fsys, dir)
	desc := "new description"
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{Name: "vault", SetDescription: &desc})
	require.NoError(t, err)

	changed := assertOnlyFileChanged(t, before, snapshotTree(t, fsys, dir), bundles.DirectoryFormManifest)
	assert.Contains(t, changed, "description: new description")
	assert.NotContains(t, changed, "fragments:", "the envelope never declares items inline")
	assert.Equal(t, "new description", read().Bundle.Description)
}

func TestUpdateBundle_TreeBundle_AddsAndRemovesItemFiles(t *testing.T) {
	fsys, cfg, dir, read := vaultTree(t)
	before := snapshotTree(t, fsys, dir)
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:            "vault",
		AddFragments:    map[string]BundleFragmentInput{"fresh": {Content: "FRESH-BODY\n", NoDistill: true}},
		RemoveFragments: []string{"other"},
	})
	require.NoError(t, err)
	after := snapshotTree(t, fsys, dir)
	for k, v := range before {
		if strings.Contains(k, "house-style") || k == bundles.DirectoryFormManifest {
			assert.Equal(t, v, after[k], "%s must be byte-identical", k)
		}
	}
	got := read().Bundle.Fragments
	assert.Contains(t, got, "fresh")
	assert.NotContains(t, got, "other")
	assert.Equal(t, "HOUSE-BODY-MARKER\n", got["house-style"].Content)
}

func TestSetFragmentPremise_TreeBundleWithASkill(t *testing.T) {
	fsys, cfg, dir, read := vaultTree(t)
	_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "vault", Name: "reviewer", Description: "Reviews things.", FS: fsys})
	require.NoError(t, err)
	before := snapshotTree(t, fsys, dir)

	_, err = SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{Bundle: "vault", Name: "house-style", Premise: "P"})
	require.NoError(t, err)
	assertOnlyFileChanged(t, before, snapshotTree(t, fsys, dir), "house-style")
	got := read().Bundle
	assert.Equal(t, "P", got.Fragments["house-style"].Premise)
	assert.Contains(t, got.Skills, "reviewer")
}
