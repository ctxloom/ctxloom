package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// Two not-found vocabularies answer for bundle items, and they do not overlap.
//
// internal/errs declares a sentinel PER KIND (ErrFragmentNotFound,
// ErrCommandNotFound, ErrSkillNotFound), raised by the loader when a reference
// fails to RESOLVE. This package declares one KIND-GENERIC pair (ErrItemNotFound
// / ErrItemExists), raised when an EDIT names an item that is absent or already
// present. A caller asking "does this item exist?" therefore gets a different
// sentinel depending on which layer answered, and errors.Is against one is
// simply false for the other.
//
// This test states the divergence rather than resolving it. Which vocabulary
// should win is a public-contract decision across three packages — the CLI's
// item-CRUD error branches match this package's pair today, while the per-kind
// sentinels have far weaker matching — and it is escalated, not settled here.
// Until it is settled the boundary must at least be explicit: a collapse in
// either direction fails this test rather than silently changing what a
// frontend's not-found branch catches.
func TestItemSentinels_DoNotOverlapWithTheResolutionTaxonomy(t *testing.T) {
	cfg := newItemTestBundle(t)

	// The fixture must be a real, loadable bundle, or "not found" below would
	// describe a missing bundle rather than a missing item.
	_, err := AddItem(context.Background(), cfg, AddItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "present", Content: "body"})
	require.NoError(t, err)

	_, err = GetItemContent(context.Background(), cfg, GetItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "ghost"})
	require.Error(t, err)

	assert.ErrorIs(t, err, ErrItemNotFound,
		"the edit layer answers with its own kind-generic sentinel")
	assert.NotErrorIs(t, err, errs.ErrFragmentNotFound,
		"and NOT with the resolution layer's per-kind one, though both mean the fragment is absent")

	// The reverse gap: the edit layer's "already exists" has no counterpart in
	// the resolution taxonomy at all, so a collapse cannot be a pure rename.
	_, err = AddItem(context.Background(), cfg, AddItemRequest{
		Bundle: "b", Kind: ItemKindFragment, Name: "present", Content: "other"})
	require.ErrorIs(t, err, ErrItemExists)
}
