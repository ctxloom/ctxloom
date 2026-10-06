package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestManagedConfigFor_CarriesTheShellTimeout: the shell timeout reaches the
// managed payload both launch paths build, and alone makes the settings
// surface present — a run with no statusline and no deny list still gets
// its engine's shell timeouts.
func TestManagedConfigFor_CarriesTheShellTimeout(t *testing.T) {
	st := engine.ShellTimeout{Default: 10 * time.Minute, Max: time.Hour}
	m := ManagedConfigFor(ManagedSurfaces{ShellTimeout: st}, engine.Exports{})
	require.Equal(t, st, m.ShellTimeout)
	require.True(t, m.Items().Settings)
	require.False(t, ManagedConfigFor(ManagedSurfaces{}, engine.Exports{}).Items().Settings, "nothing to say is no settings surface")
}
