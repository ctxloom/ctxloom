package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// newItemTestBundle makes an empty bundle "b" for item tests.
func newItemTestBundle(t *testing.T) *config.Config {
	t.Helper()
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "b"})
	require.NoError(t, err)
	return cfg
}

func TestAddItem_AddOnlyAcrossKinds(t *testing.T) {
	for _, kind := range []ItemKind{ItemKindFragment, ItemKindCommand} {
		t.Run(string(kind), func(t *testing.T) {
			cfg := newItemTestBundle(t)

			res, err := AddItem(context.Background(), cfg, AddItemRequest{
				Bundle: "b", Kind: kind, Name: "x", Content: "hello",
			})
			require.NoError(t, err)
			assert.Equal(t, "created", res.Status)

			got, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: kind, Name: "x"})
			require.NoError(t, err)
			assert.Equal(t, "hello", got.Content)

			// Re-adding the same name must not overwrite — it returns ErrItemExists.
			_, err = AddItem(context.Background(), cfg, AddItemRequest{
				Bundle: "b", Kind: kind, Name: "x", Content: "CLOBBER",
			})
			require.ErrorIs(t, err, ErrItemExists)

			got, err = GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: kind, Name: "x"})
			require.NoError(t, err)
			assert.Equal(t, "hello", got.Content, "add-only must not clobber existing content")
		})
	}
}

func TestAddItem_DistillsWhenDistillerProvided(t *testing.T) {
	cfg := newItemTestBundle(t)
	d := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"}

	_, err := AddItem(context.Background(), cfg, AddItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw", Distiller: d,
	})
	require.NoError(t, err)
	require.Len(t, d.calls, 1)
	assert.Equal(t, "raw", d.calls[0].Content)

	got, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f"})
	require.NoError(t, err)
	assert.Equal(t, "DISTILLED", got.Distilled)
}

func TestAddItem_NoDistillerStoresRaw(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw",
	})
	require.NoError(t, err)
	got, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f"})
	require.NoError(t, err)
	assert.Empty(t, got.Distilled, "nil distiller stores raw, no distilled form")
}

func TestDeleteItem(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindCommand, Name: "p", Content: "c"})
	require.NoError(t, err)

	_, err = DeleteItem(context.Background(), cfg, DeleteItemRequest{Bundle: "b", Kind: ItemKindCommand, Name: "p"})
	require.NoError(t, err)

	_, err = GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindCommand, Name: "p"})
	require.ErrorIs(t, err, ErrItemNotFound)

	// Deleting an absent item is ErrItemNotFound, not a silent success.
	_, err = DeleteItem(context.Background(), cfg, DeleteItemRequest{Bundle: "b", Kind: ItemKindCommand, Name: "p"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

func TestGetItemContent_NotFound(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "ghost"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

// TestSetItemContent_PreservesFieldsAndRedistills pins the edit path: changing
// content must keep the item's other fields (tags) and trigger re-distillation.
func TestSetItemContent_PreservesFieldsAndRedistills(t *testing.T) {
	cfg := newItemTestBundle(t)

	// Seed a fragment WITH tags via UpdateBundle (AddItem sets content only).
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "b",
		SetFragments: map[string]BundleFragmentInput{
			"f": {Content: "v1", Tags: []string{"keep"}},
		},
	})
	require.NoError(t, err)

	d := &recordingDistiller{returnValue: "DISTILLED-V2", returnModel: "mock"}
	res, err := SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "v2", Distiller: d,
	})
	require.NoError(t, err)
	assert.True(t, res.Distilled)
	require.Len(t, d.calls, 1)
	assert.Equal(t, "v2", d.calls[0].Content)

	// Re-load and confirm tags survived the content-only edit.
	bundle, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	assert.Equal(t, "v2", bundle.Fragments["f"].Content)
	assert.Equal(t, []string{"keep"}, bundle.Fragments["f"].Tags, "content edit must not wipe tags")
	assert.Equal(t, "DISTILLED-V2", bundle.Fragments["f"].Distilled)
}

func TestSetItemContent_NotFound(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "ghost", Content: "x",
	})
	require.ErrorIs(t, err, ErrItemNotFound)
}


// TestSetItemContent_EmptyContentRefuses pins U084-F02: SetItemContent used
// to accept Content: "" unconditionally, silently overwriting an authored
// fragment's content with zero bytes and reporting status: "updated" — this
// is the frontend-agnostic operations layer (ADR-0026), so the floor belongs
// here, not only in the CLI's checkEditedContent pre-check.
func TestSetItemContent_EmptyContentRefuses(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "v1"})
	require.NoError(t, err)

	_, err = SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "",
	})
	require.ErrorIs(t, err, ErrItemContentEmpty)

	// The original content must survive untouched.
	got, err := GetItemContent(context.Background(), cfg, GetItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f"})
	require.NoError(t, err)
	assert.Equal(t, "v1", got.Content, "a refused empty-content edit must not touch the saved item")
}

// TestSetItemContent_WhitespaceOnlyContentRefuses is the companion case:
// checkEditedContent's own criterion is TrimSpace-empty, not byte-empty, so
// this operations-layer floor must match it exactly or a whitespace-only
// buffer would still slip through here even though the CLI path blocks it.
func TestSetItemContent_WhitespaceOnlyContentRefuses(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "v1"})
	require.NoError(t, err)

	_, err = SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "   \n\t  ",
	})
	require.ErrorIs(t, err, ErrItemContentEmpty)
}

// A distiller error during an edit must not be reported as a successful
// distill: applyFragmentEdits already cleared the stale distilled form, so the
// saved item has none and Distilled must be false.
func TestSetItemContent_DistillFailureReportsNotDistilled(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "v1"})
	require.NoError(t, err)

	d := &recordingDistiller{returnErr: assert.AnError}
	res, err := SetItemContent(context.Background(), cfg, SetItemContentRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "v2", Distiller: d,
	})
	require.NoError(t, err)
	assert.False(t, res.Distilled, "a failed distill must not report Distilled=true")

	bundle, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	assert.Empty(t, bundle.Fragments["f"].Distilled, "no distilled form should be saved when the distiller errored")
}

func TestDistillItem_DistillsStaleContent(t *testing.T) {
	cfg := newItemTestBundle(t)
	// Seed raw (no distiller) → its distilled form is stale.
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw"})
	require.NoError(t, err)

	d := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"}
	res, err := DistillItem(context.Background(), cfg, DistillItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Distiller: d,
	})
	require.NoError(t, err)
	assert.Equal(t, "distilled", res.Status)
	assert.Equal(t, "mock", res.ModelID)
	require.Len(t, d.calls, 1)
}

// A first-time distill that errors must report "skipped"/"distill_failed", not
// a fabricated "distilled" success with an empty model id.
func TestDistillItem_DistillFailureReportsSkipped(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw"})
	require.NoError(t, err)

	d := &recordingDistiller{returnErr: assert.AnError}
	res, err := DistillItem(context.Background(), cfg, DistillItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Distiller: d,
	})
	require.NoError(t, err)
	assert.Equal(t, "skipped", res.Status)
	assert.Equal(t, "distill_failed", res.Reason)
	assert.Empty(t, res.ModelID)
}

// A FAILED re-distill must not report the stale (previous) model id as a fresh
// success — the silent-lie case the failed-set check guards.
func TestDistillItem_RedistillFailureDoesNotReportStaleModel(t *testing.T) {
	cfg := newItemTestBundle(t)
	good := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"}
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw", Distiller: good})
	require.NoError(t, err)

	bad := &recordingDistiller{returnErr: assert.AnError}
	res, err := DistillItem(context.Background(), cfg, DistillItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "f", Force: true, Distiller: bad,
	})
	require.NoError(t, err)
	assert.Equal(t, "skipped", res.Status)
	assert.Equal(t, "distill_failed", res.Reason)
}

func TestDistillItem_SkipsWhenUnchanged(t *testing.T) {
	cfg := newItemTestBundle(t)
	d := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"}
	// Add WITH distiller → distilled and content hash recorded (fresh).
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw", Distiller: d})
	require.NoError(t, err)

	// Without force, a fresh item is skipped.
	res, err := DistillItem(context.Background(), cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Distiller: d})
	require.NoError(t, err)
	assert.Equal(t, "skipped", res.Status)
	assert.Equal(t, "unchanged", res.Reason)

	// Force redistills it.
	res, err = DistillItem(context.Background(), cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Force: true, Distiller: d})
	require.NoError(t, err)
	assert.Equal(t, "distilled", res.Status)
}

func TestDistillItem_SkipsNoDistillAndNoDistiller(t *testing.T) {
	cfg := newItemTestBundle(t)
	// no_distill item.
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:         "b",
		SetFragments: map[string]BundleFragmentInput{"nd": {Content: "x", NoDistill: true}},
	})
	require.NoError(t, err)
	res, err := DistillItem(context.Background(), cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "nd", Force: true, Distiller: &recordingDistiller{}})
	require.NoError(t, err)
	assert.Equal(t, "skipped", res.Status)
	assert.Equal(t, "no_distill", res.Reason)

	// Distillable item but no distiller supplied → skipped, not a silent save.
	_, err = AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Content: "raw"})
	require.NoError(t, err)
	res, err = DistillItem(context.Background(), cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Distiller: nil})
	require.NoError(t, err)
	assert.Equal(t, "skipped", res.Status)
	assert.Equal(t, "no_distiller", res.Reason)
}

func TestDistillItem_NotFound(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := DistillItem(context.Background(), cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "ghost", Distiller: &recordingDistiller{}})
	require.ErrorIs(t, err, ErrItemNotFound)
}

func TestBundleMCP_GetSet(t *testing.T) {
	cfg := newItemTestBundle(t)
	// Seed an MCP server.
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:          "b",
		SetMCPServers: map[string]BundleMCPInput{"srv": {Command: "old"}},
	})
	require.NoError(t, err)

	got, err := GetBundleMCP(context.Background(), cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "old", got.MCP.Command)

	_, err = SetBundleMCP(context.Background(), cfg, SetBundleMCPRequest{
		Bundle: "b", Name: "srv", MCP: BundleMCPInput{Command: "new", Args: []string{"-x"}},
	})
	require.NoError(t, err)

	got, err = GetBundleMCP(context.Background(), cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "new", got.MCP.Command)
	assert.Equal(t, []string{"-x"}, got.MCP.Args)
}

func TestGetBundleMCP_NotFound(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := GetBundleMCP(context.Background(), cfg, GetBundleMCPRequest{Bundle: "b", Name: "ghost"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

func TestDistillBundleFile(t *testing.T) {
	cfg := newItemTestBundle(t)
	// Two fragments: one distillable, one no_distill.
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "b",
		SetFragments: map[string]BundleFragmentInput{
			"live": {Content: "raw"},
			"keep": {Content: "x", NoDistill: true},
		},
	})
	require.NoError(t, err)
	path, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)

	d := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock"}
	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{Path: path.Path, Distiller: d})
	require.NoError(t, err)
	assert.True(t, res.Saved)

	byName := map[string]DistillBundleItem{}
	for _, it := range res.Items {
		byName[it.Name] = it
	}
	assert.Equal(t, DistillStatusDistilled, byName["live"].Status)
	assert.Equal(t, DistillStatusSkipped, byName["keep"].Status)
	assert.Equal(t, "no_distill", byName["keep"].Reason)
}

func TestDistillBundleFile_DryRunAndNoDistiller(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:         "b",
		SetFragments: map[string]BundleFragmentInput{"live": {Content: "raw"}},
	})
	require.NoError(t, err)
	b, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)

	dry, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{Path: b.Path, DryRun: true})
	require.NoError(t, err)
	assert.False(t, dry.Saved)
	require.Len(t, dry.Items, 1)
	assert.Equal(t, DistillStatusPlanned, dry.Items[0].Status)

	none, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{Path: b.Path, Distiller: nil})
	require.NoError(t, err)
	assert.False(t, none.Saved)
	require.Len(t, none.Items, 1)
	assert.Equal(t, "no_distiller", none.Items[0].Reason)
}

func TestItemOps_InvalidKind(t *testing.T) {
	cfg := newItemTestBundle(t)
	_, err := AddItem(context.Background(), cfg, AddItemRequest{Bundle: "b", Kind: "bogus", Name: "x", Content: "c"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid item kind")
}
