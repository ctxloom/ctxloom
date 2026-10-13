package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestLaunchContextWindow_LabelThenEngineDefault: the window of a resolved
// launch is its label's own neutral `context_window`; absent, the declared
// default of the engine it runs (reached through the registry); absent
// both, 0. claude declares its default; the mock declares none.
func TestLaunchContextWindow_LabelThenEngineDefault(t *testing.T) {
	cl, err := claude.Build()
	require.NoError(t, err)
	reg, err := engine.NewRegistry(cl, mock.New())
	require.NoError(t, err)

	on := func(name engine.Name, body map[string]any) launch.Launch {
		return launch.Launch{Engine: name, Label: engine.LabelConfig{Label: "l", Body: body}}
	}
	require.Equal(t, 131072, LaunchContextWindow(reg, on(cl.Root().Name, map[string]any{"context_window": 131072})),
		"the label's own window wins over the engine's default")
	require.Equal(t, claude.DefaultContextWindow, LaunchContextWindow(reg, on(cl.Root().Name, map[string]any{"model": "haiku"})),
		"a claude label declaring none takes claude's declared default")
	require.Equal(t, 0, LaunchContextWindow(reg, on(mock.New().Root().Name, nil)),
		"an engine declaring no default leaves the window unknown")
	require.Equal(t, 0, LaunchContextWindow(reg, on("not-registered", nil)))
}

// TestLazyOneShot_ContextWindowStartsOnceAndReadsTheLaunch: asking the
// window starts the session if none has, reads the launch it resolved, and
// the following turn reuses that session rather than minting another. A
// start failure is the window's error.
func TestLazyOneShot_ContextWindowStartsOnceAndReadsTheLaunch(t *testing.T) {
	cl, err := claude.Build()
	require.NoError(t, err)
	reg, err := engine.NewRegistry(cl)
	require.NoError(t, err)

	starts := 0
	l := &LazyOneShot{engines: reg, start: func(context.Context) (*OneShotSession, error) {
		starts++
		return &OneShotSession{Launch: launch.Launch{Engine: cl.Root().Name, Label: engine.LabelConfig{Body: map[string]any{"context_window": 65536}}}}, nil
	}}
	got, err := l.ContextWindow(context.Background())
	require.NoError(t, err)
	require.Equal(t, 65536, got)
	_, err = l.ContextWindow(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, starts, "the session starts once")

	boom := errors.New("no engine")
	failing := &LazyOneShot{engines: reg, start: func(context.Context) (*OneShotSession, error) { return nil, boom }}
	_, err = failing.ContextWindow(context.Background())
	require.ErrorIs(t, err, boom)
}

// TestOneShotBuilder_LazyCarriesTheRegistry: the lazy one-shot a compaction
// uses reaches engine defaults through the launch facts' registry; without
// it a claude label declaring no window would silently read as unknown.
func TestOneShotBuilder_LazyCarriesTheRegistry(t *testing.T) {
	cl, err := claude.Build()
	require.NoError(t, err)
	reg, err := engine.NewRegistry(cl)
	require.NoError(t, err)
	lazy := DistillerOneShot(LaunchFacts{Engines: reg}, nil, nil).Lazy()
	_, ok := lazy.engines.Lookup(cl.Root().Name)
	require.True(t, ok, "Lazy must carry the facts' engine registry")
}
