package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestStartupFindings_FlagOptsOut: --no-startup-findings leads the launch
// with nothing, findings or not — it returns before the package is even
// opened, so a launch it could not open comes back untouched.
func TestStartupFindings_FlagOptsOut(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	strictness.Record(report.KindConfig, "", "a finding the flag must withhold")
	runNoStartupFindings = true
	t.Cleanup(func() { runNoStartupFindings = false })
	l := launch.Launch{Engine: "mock"}

	got, err := (&runState{}).withStartupFindings(launch.Deps{}, l)

	require.NoError(t, err)
	assert.Equal(t, l, got)
}
