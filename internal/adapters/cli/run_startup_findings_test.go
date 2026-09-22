package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// noCompanions pins the companion probe to "nothing discovered" so a test
// about the OTHER rows is not perturbed by whatever companion binaries happen
// to sit on the developer's PATH; it returns the generation so pinned.
func noCompanions(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	return withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{}, nil
	})
}

// cleanProject is a project with nothing to report: marker present, config
// valid, every local-only path scaffolded, no companions discovered.
func cleanProject(t *testing.T) *config.Config {
	t.Helper()
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)
	return noCompanions(t, cfg)
}

// TestStartupFindings_IsTheLaunchsLead asserts on the block the engine
// receives — the lead the launch's package carries — never on stderr: a
// finding the launch recorded rides into the started agent's context as one
// named block after the assembled context.
func TestStartupFindings_IsTheLaunchsLead(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	st := &runState{cfg: cleanProject(t)}
	strictness.Record(strictness.ClassIsolation, "", "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")

	lead := st.startupFindings()

	require.Len(t, lead, 1, "one block, after the assembled context")
	assert.Equal(t, startupFindingsFragmentName, lead[0].Name)
	assert.Contains(t, lead[0].Body, "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")
	assert.Contains(t, lead[0].Body, operations.StartupFindingsMarker)
	assert.True(t, strings.HasPrefix(lead[0].Body, "ctxloom doctor\n"),
		"rendered by doctor's own renderer, so the agent reads the same surface a human would")
}

// TestStartupFindings_FlagOptsOut: --no-startup-findings composes no lead,
// findings or not.
func TestStartupFindings_FlagOptsOut(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	st := &runState{cfg: cleanProject(t)}
	strictness.Record(strictness.ClassConfig, "", "a finding the flag must withhold")
	runNoStartupFindings = true
	t.Cleanup(func() { runNoStartupFindings = false })

	assert.Empty(t, st.startupFindings())
}

// TestStartupFindings_NothingToDeliverAddsNothing: a clean launch composes
// no block at all — not an empty one, not a header with no rows.
func TestStartupFindings_NothingToDeliverAddsNothing(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	st := &runState{cfg: cleanProject(t)}
	assert.Empty(t, st.startupFindings())
}
