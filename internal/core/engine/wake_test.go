package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

type wakeFunc func(ctx context.Context, nonce string) error

func (f wakeFunc) Fire(ctx context.Context, nonce string) error { return f(ctx, nonce) }

// A typed spec binds ONCE, at bind time, and fails there — never at fire —
// when the session has no pane to type into.
func TestTypedWakeSpec_RefusesASessionThatIsNotPaneHosted(t *testing.T) {
	spec := engine.TypedWakeSpec{Composer: func(string) bool { return true }}
	_, err := spec.Bind(context.Background(), engine.BoundSession{Harp: "h"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrWakeUnbound))
}

func TestTypedWakeSpec_RefusesASpecWithNoComposerProbe(t *testing.T) {
	bound := engine.BoundSession{Harp: "h", Typed: func(engine.ComposerProbe) (engine.Wake, error) {
		t.Fatal("a spec with no composer probe must not reach the pane")
		return nil, nil
	}}
	_, err := engine.TypedWakeSpec{}.Bind(context.Background(), bound)
	require.Error(t, err)
}

// The composer the runner's pane binding receives is the ENGINE's: the
// engine knows what its empty input line looks like, the runner does not.
func TestTypedWakeSpec_HandsTheEnginesComposerToThePaneBinding(t *testing.T) {
	var got engine.ComposerProbe
	want := wakeFunc(func(context.Context, string) error { return nil })
	bound := engine.BoundSession{Harp: "h", Typed: func(c engine.ComposerProbe) (engine.Wake, error) {
		got = c
		return want, nil
	}}
	spec := engine.TypedWakeSpec{Composer: func(line string) bool { return line == "EMPTY" }}
	w, err := spec.Bind(context.Background(), bound)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got("EMPTY"))
	assert.False(t, got("draft"))
	require.NoError(t, w.Fire(context.Background(), "n"))
}
