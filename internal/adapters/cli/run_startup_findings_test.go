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
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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

// attachFixture is a runState at the point the trunk attaches findings: the
// RunStart already built with the assembled context as its one fragment.
func attachFixture(cfg *config.Config) *runState {
	return &runState{
		cfg: cfg,
		req: &pb.RunStart{Fragments: []*pb.Fragment{{Content: "ASSEMBLED-CONTEXT"}}},
	}
}

// TestAttachStartupFindings_DeliversIntoTheRequest asserts on the bytes the
// engine receives — the RunStart's fragments — never on stderr: a finding
// the launch recorded rides into the started agent's context as a fragment
// alongside the assembled context.
func TestAttachStartupFindings_DeliversIntoTheRequest(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	st := attachFixture(cleanProject(t))
	strictness.Record(strictness.ClassIsolation, "", "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")

	st.attachStartupFindings()

	require.Len(t, st.req.Fragments, 2, "one fragment appended after the assembled context")
	assert.Equal(t, "ASSEMBLED-CONTEXT", st.req.Fragments[0].Content, "the assembled context is untouched")
	delivered := st.req.Fragments[1]
	assert.Equal(t, startupFindingsFragmentName, delivered.Name)
	assert.Contains(t, delivered.Content, "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")
	assert.Contains(t, delivered.Content, operations.StartupFindingsMarker)
	assert.True(t, strings.HasPrefix(delivered.Content, "ctxloom doctor\n"),
		"rendered by doctor's own renderer, so the agent reads the same surface a human would")
}

// TestAttachStartupFindings_FlagOptsOut: --no-startup-findings leaves the
// request exactly as built, findings or not.
func TestAttachStartupFindings_FlagOptsOut(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	st := attachFixture(cleanProject(t))
	strictness.Record(strictness.ClassConfig, "", "a finding the flag must withhold")
	runNoStartupFindings = true
	t.Cleanup(func() { runNoStartupFindings = false })

	st.attachStartupFindings()

	require.Len(t, st.req.Fragments, 1)
	assert.Equal(t, "ASSEMBLED-CONTEXT", st.req.Fragments[0].Content)
}

// TestAttachStartupFindings_NothingToDeliverAddsNothing: a clean launch adds
// no fragment at all — not an empty one, not a header with no rows.
func TestAttachStartupFindings_NothingToDeliverAddsNothing(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	st := attachFixture(cleanProject(t))

	st.attachStartupFindings()

	require.Len(t, st.req.Fragments, 1)
	assert.Equal(t, "ASSEMBLED-CONTEXT", st.req.Fragments[0].Content)
}
