package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Ready is false until the open has RETURNED, including while the opener is
// still running — the window in which a reader gating on Opened would
// re-enter Owner and deadlock.
func TestApp_ReadyOnlyOnceOpeningHasFinished(t *testing.T) {
	var a *App
	var duringOpened, duringReady bool
	a = NewApp(nil, Switches{}, nil, strictness.Mode{}, Handed{
		Open: func(context.Context, config.Sources, ...config.Option) (*config.Owner, error) {
			duringOpened, duringReady = a.Opened(), a.Ready()
			return nil, nil
		},
	})

	assert.False(t, a.Ready(), "nothing has opened yet")
	_, err := a.Owner(context.Background())
	require.NoError(t, err)

	assert.True(t, duringOpened, "Opened is true from the moment opening starts")
	assert.False(t, duringReady, "Ready stays false while the opener is on the stack")
	assert.True(t, a.Ready(), "Ready once the opener has returned")
}
