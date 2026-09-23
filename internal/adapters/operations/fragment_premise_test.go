package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

const seededPremise = "You are about to remove a worktree."

// seedPremisedFragment gives bundle "b" a fragment "f" carrying every field an
// unrelated edit could wipe: tags, notes, installation, a distilled form and
// a premise.
func seedPremisedFragment(t *testing.T) *config.Config {
	t.Helper()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "b",
		SetFragments: map[string]BundleFragmentInput{
			"f": {Content: "body v1", Tags: []string{"keep"}, Notes: "origin notes", Installation: "setup", Premise: seededPremise},
		},
		Distiller: &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"},
	})
	require.NoError(t, err)
	got := loadFragment(t, cfg, "f")
	require.Equal(t, seededPremise, got.Premise, "fixture: SetFragments must carry the premise it was given")
	require.Equal(t, "DISTILLED", got.Distilled, "fixture: the fragment must start distilled")
	return cfg
}

func loadFragment(t *testing.T, cfg *config.Config, name string) bundles.BundleFragment {
	t.Helper()
	b, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	f, ok := b.Fragments[name]
	require.True(t, ok, "fragment %q missing", name)
	return f
}

func TestSetFragmentPremise_WritesPremiseAndNotesWithoutRedistilling(t *testing.T) {
	cfg := seedPremisedFragment(t)

	res, err := SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{
		Bundle: "b", Name: "f", Premise: "You are about to delete a branch.", Notes: "why it exists",
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", res.Status)
	assert.Equal(t, "b", res.Bundle)
	assert.Equal(t, "f", res.Name)
	assert.NotEmpty(t, res.Path)

	got := loadFragment(t, cfg, "f")
	assert.Equal(t, "You are about to delete a branch.", got.Premise)
	assert.Equal(t, "why it exists", got.Notes)
	assert.Equal(t, "body v1", got.Content, "a premise edit must not touch the body")
	assert.Equal(t, "DISTILLED", got.Distilled, "a premise edit must not clear the distilled form: nothing is re-distilled")
	assert.Equal(t, "mock", got.DistilledBy)
	assert.Equal(t, []string{"keep"}, got.Tags)
	assert.Equal(t, "setup", got.Installation)
}

func TestSetFragmentPremise_EmptyPremiseMeansAlwaysLoad(t *testing.T) {
	cfg := seedPremisedFragment(t)
	_, err := SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{
		Bundle: "b", Name: "f", Premise: "", Notes: "origin notes",
	})
	require.NoError(t, err)
	assert.Empty(t, loadFragment(t, cfg, "f").Premise)
}

func TestSetFragmentPremise_MissingFragmentIsNotFound(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{Bundle: "b", Name: "ghost", Premise: "p"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

func TestGetItemContent_CarriesPremiseAndNotes(t *testing.T) {
	cfg := seedPremisedFragment(t)
	got, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f"})
	require.NoError(t, err)
	assert.Equal(t, seededPremise, got.Premise)
	assert.Equal(t, "origin notes", got.Notes)
}

func TestCreateBundle_CarriesFragmentPremise(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "b",
		Fragments: map[string]BundleFragmentInput{"f": {Content: "body", Premise: seededPremise, NoDistill: true}},
	})
	require.NoError(t, err)
	assert.Equal(t, seededPremise, loadFragment(t, cfg, "f").Premise)
}

// The premise-preservation trap: every caller that builds a
// BundleFragmentInput for an EXISTING fragment must carry its premise, or an
// unrelated edit silently makes a conditional fragment unconditional.

func TestSetItemContent_PreservesPremise(t *testing.T) {
	cfg := seedPremisedFragment(t)
	_, err := SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "body v2",
	})
	require.NoError(t, err)
	got := loadFragment(t, cfg, "f")
	assert.Equal(t, "body v2", got.Content)
	assert.Equal(t, seededPremise, got.Premise, "a content edit must not wipe the premise")
}

func TestSetFragmentPremise_PreservesNoDistill(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:         "b",
		SetFragments: map[string]BundleFragmentInput{"f": {Content: "body", NoDistill: true}},
	})
	require.NoError(t, err)
	_, err = SetFragmentPremise(context.Background(), cfg, SetFragmentPremiseRequest{Bundle: "b", Name: "f", Premise: "p"})
	require.NoError(t, err)
	assert.True(t, loadFragment(t, cfg, "f").NoDistill)
}

func TestUpdateBundle_AddFragmentOfExistingNamePreservesPremise(t *testing.T) {
	cfg := seedPremisedFragment(t)
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:         "b",
		AddFragments: map[string]BundleFragmentInput{"f": {Content: "placeholder"}},
		AddTags:      []string{"unrelated"},
	})
	require.NoError(t, err)
	got := loadFragment(t, cfg, "f")
	assert.Equal(t, seededPremise, got.Premise, "add-only must not clobber an existing fragment's premise")
	assert.Equal(t, "body v1", got.Content)
}
