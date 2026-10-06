package composite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestEngineItems_AShellTimeoutAloneIsSettings: a package whose only settings
// content is the shell timeout still offers the engine a settings item.
func TestEngineItems_AShellTimeoutAloneIsSettings(t *testing.T) {
	pkg := composite.Package{ShellTimeout: engine.ShellTimeout{Default: time.Minute, Max: time.Hour}}
	require.True(t, pkg.EngineItems("mock").Settings)
	require.False(t, composite.Package{}.EngineItems("mock").Settings)
}
