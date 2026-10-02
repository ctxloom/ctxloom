package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func TestApp_TheSigCheckSwitchReachesTheGenerationsTrust(t *testing.T) {
	for _, tc := range []struct {
		name string
		sw   Switches
		want bool
	}{
		{"enforced by default", Switches{}, false},
		{"waived by the switch", Switches{SigCheckDisabled: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := NewApp(fixtureSources{cfg: config.NewFixture(config.Fixture{})}, tc.sw, nil, strictness.Mode{Prog: "ctxloom"}, Handed{Open: config.Open})
			snap, err := app.Snapshot(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, snap.Trust.SignatureCheckDisabled())
		})
	}
}
