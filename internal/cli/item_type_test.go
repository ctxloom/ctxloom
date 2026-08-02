package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// Switches over ItemType used to fall through to `return nil, nil` / `return
// "", "", nil` — success with zero payload — instead of erroring on an
// unrecognized kind. Unreachable with today's two constants, but it is the wrong
// shape to leave for the third. Both surviving sites are pinned here: the CLI's
// own listing enumerator, and the read path the item flow collapsed onto
// (itemDisplayContent was folded into operations.GetItemContent, whose kind
// validation enforces the same invariant one layer earlier).
func TestUnknownItemTypeIsAFailureNotAnEmptyItem(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

	rows, err := listItemRows(cfg, ItemType("sculpture"))
	assert.Error(t, err, "a listing that cannot tell what it was asked to list must say so")
	assert.Nil(t, rows, "never an empty list that reads as \"there are none\"")

	_, err = operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: "demo", Kind: ItemType("sculpture"), Name: "whatever",
	})
	assert.Error(t, err, "an unrecognized item kind must not read as an item with empty content")
}

// TestItemType_IsTheSameVocabularyAsOperations pins that cli.ItemType
// was a second declaration of operations.ItemKind — the same two string literals,
// cross-converted at four call sites, with nothing but a convention keeping them
// equal. Two enums over one vocabulary that must agree and get no compiler help
// is the defect; the strings were VERBATIM identical, so no parity assertion here
// could be red (template §4, the identical-implementations case) and this test's
// job is to pin the shared vocabulary so the collapse is provably
// behaviour-preserving.
//
// Both halves matter: the string VALUES are what land in a bundle ref
// (`bundle#fragments/name`) and in operations' own kind validation, and the SET is
// what makes an unrecognized kind an error rather than an empty item.
func TestItemType_IsTheSameVocabularyAsOperations(t *testing.T) {
	assert.Equal(t, string(operations.ItemKindFragment), string(ItemTypeFragment))
	assert.Equal(t, string(operations.ItemKindCommand), string(ItemTypeCommand))
	assert.Equal(t, "fragment", string(ItemTypeFragment), "the value rides in refs and in operations' kind check")
	assert.Equal(t, "command", string(ItemTypeCommand))

	assert.Equal(t, "fragments/", itemRefPrefix(ItemTypeFragment))
	assert.Equal(t, "commands/", itemRefPrefix(ItemTypeCommand))

	// Round-trip through the operations boundary: what the CLI names and what
	// the core validates are the same value, so no conversion can lose anything.
	for _, kind := range []operations.ItemKind{ItemTypeFragment, ItemTypeCommand} {
		_, err := operations.GetItemContent(context.Background(),
			config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}}),
			operations.GetItemRequest{Bundle: "nosuch", Kind: kind, Name: "x"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "invalid item kind",
			"%q must be a kind the operations core accepts", kind)
	}
}

// TestShowItem_MissingItemStillListsWhatExists pins the UX that the
// collapse had to carry across rather than drop: itemDisplayContent's not-found
// error named every item that DOES exist, and folding it into
// operations.GetItemContent must keep that — a name that isn't there is a typo,
// and the answer to a typo is the list.
func TestShowItem_MissingItemStillListsWhatExists(t *testing.T) {
	root := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join(root, ".ctxloom")}})
	seedLocalFragment(t, cfg, "demo", "real-one", "body")
	t.Chdir(root) // GetConfig() (config.Load) resolves <root>/.ctxloom

	cmd, _ := testCmd()
	err := showItem(cmd, "demo#fragments/ghost", ItemTypeFragment, false, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, operations.ErrItemNotFound, "the sentinel must survive for errors.Is callers")
	assert.Contains(t, err.Error(), "real-one", "the error must name the fragments that do exist")
}
