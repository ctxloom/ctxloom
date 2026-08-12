package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestPlanCandidates_ReportsAnUnreadableSessionDir pins the discovery half of
// the plan-stamping fix. planCandidates returned a bare nil for every
// failure, so an unreadable session dir was indistinguishable from a session
// that authored no plans. agent_report then answered journaled:true with an
// empty artifact list — a success receipt for delivering nothing, this
// project's characteristic silent no-op, and the one report where the agent
// has no other way to notice.
func TestPlanCandidates_ReportsAnUnreadableSessionDir(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "witty-plain-otter"

	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	// A regular FILE where the session directory belongs: ReadDir fails with
	// ENOTDIR, standing in for any non-ENOENT read fault (permissions, a
	// network-mount hiccup, a half-created dir).
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o644))

	p := &artifactStamper{harp: harp}
	got, err := p.planCandidates()
	assert.Empty(t, got)
	require.Error(t, err, "an unreadable session dir must not read as 'no plans to stamp'")
	assert.Contains(t, err.Error(), dir, "the failure must name the directory")
}

// TestPlanCandidates_QuietForALegitimateAbsence is the other arm: the two
// genuine "nothing to stamp" cases must stay silent, or every report from a
// session that has not written a plan yet would carry a spurious fault.
func TestPlanCandidates_QuietForALegitimateAbsence(t *testing.T) {
	testsupport.Isolate(t)

	t.Run("no harp at all", func(t *testing.T) {
		got, err := (&artifactStamper{}).planCandidates()
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("session dir not created yet", func(t *testing.T) {
		got, err := (&artifactStamper{harp: "fuzzy-blank-heron"}).planCandidates()
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// TestReportResult_NamesUnstampedPlans pins the answering half of the same
// fix. The advertised structured shape (journaled + artifact_ids, the
// generated output schema) is unchanged — an unstamped plan is simply absent
// from artifact_ids, which is exactly why the caller cannot infer it. The
// failures therefore have to be SAID, on the text channel coordinationResult
// already uses for human-readable status.
func TestReportResult_NamesUnstampedPlans(t *testing.T) {
	artifacts := []*agentcoordpb.ArtifactProduced{{ArtifactId: "plan/kept"}}
	res := reportResult(artifacts, []string{"/sessions/h/lost.plan.md: read: permission denied"})
	require.NotNil(t, res)

	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "the advertised structured shape must be unchanged")
	assert.Equal(t, true, structured["journaled"])
	assert.Equal(t, []any{"plan/kept"}, structured["artifact_ids"])

	require.NotEmpty(t, res.Content, "a report that failed to stamp plans must say so")
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "lost.plan.md")
	assert.Contains(t, text.Text, "permission denied")
	assert.Contains(t, text.Text, "NOT stamped")
}

// TestReportResult_SilentWhenEverythingStamped: a clean report must not grow
// text content it never had, or the signal above becomes noise.
func TestReportResult_SilentWhenEverythingStamped(t *testing.T) {
	res := reportResult([]*agentcoordpb.ArtifactProduced{{ArtifactId: "plan/kept"}}, nil)
	require.NotNil(t, res)
	assert.Empty(t, res.Content, "a fully-successful report must carry no failure text")
	structured, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"plan/kept"}, structured["artifact_ids"])
}
