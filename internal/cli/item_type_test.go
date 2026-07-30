package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// U037-F07: two switches over ItemType fell through to `return nil, nil` /
// `return "", "", nil` — success with zero payload — instead of erroring on an
// unrecognized kind. Unreachable with today's two constants, but it is the
// wrong shape to leave for the third.
func TestItemDisplayContent_UnknownItemTypeErrors(t *testing.T) {
	_, _, err := itemDisplayContent(&bundles.Bundle{}, "whatever", ItemType("sculpture"))
	assert.Error(t, err, "an unrecognized item type must not read as an item with empty content")
}

// TestItemType_IsTheSameVocabularyAsOperations is the U037-F09 pin. cli.ItemType
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
