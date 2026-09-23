package mock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The mock is the typed kind's conformance subject: it declares a TYPED wake,
// and its composer is its cooked-mode line buffer — no prompt glyph, no
// decorations — so the input line is empty exactly when nothing is on it.
func TestWake_MockDeclaresATypedWakeWhoseComposerIsItsLineBuffer(t *testing.T) {
	spec, ok := New().Wake().Get()
	require.True(t, ok, "the mock must provide a wake: it is the typed kind's conformance subject")
	typed, ok := spec.(engine.TypedWakeSpec)
	require.True(t, ok, "the mock's wake types into its pane, got %T", spec)

	var composer engine.ComposerProbe
	_, err := typed.Bind(context.Background(), engine.BoundSession{Harp: "h", Typed: func(c engine.ComposerProbe) (engine.Wake, error) {
		composer = c
		return nil, nil
	}})
	require.NoError(t, err)
	assert.True(t, composer(""), "nothing typed")
	assert.False(t, composer("hel"), "a draft")
	assert.False(t, composer(" "), "whitespace is still something a human typed")
}
