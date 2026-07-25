package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// U037-F07: two switches over ItemType fell through to `return nil, nil` /
// `return "", "", nil` — success with zero payload — instead of erroring on an
// unrecognized kind. Unreachable with today's two constants, but it is the
// wrong shape to leave for the third.
func TestItemDisplayContent_UnknownItemTypeErrors(t *testing.T) {
	_, _, err := itemDisplayContent(&bundles.Bundle{}, "whatever", ItemType("sculpture"))
	assert.Error(t, err, "an unrecognized item type must not read as an item with empty content")
}
