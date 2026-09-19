package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestStartupFindingsReport_RecordedFindingsBecomeRows: every finding the
// launch itself recorded (a config warning, a degraded isolation axis, a sync
// failure) is a row in the report, carrying the finding's own message and
// its fix-it — the wording the human already saw on stderr, not a paraphrase.
func TestStartupFindingsReport_RecordedFindingsBecomeRows(t *testing.T) {
	cfg := cleanProject(t)
	recorded := []strictness.Finding{
		{Class: strictness.ClassConfig, Message: "unknown key `runtme` in config.yaml: ctxloom does not know it, so it is IGNORED", FixIt: "did you mean `runtime`?"},
		{Class: strictness.ClassIsolation, Message: "container runtime requested but no runtime is reachable; running on the host"},
	}

	report := startupFindingsReport(cfg, recorded)

	require.Len(t, report.Checks, 2)
	for i, f := range recorded {
		row := report.Checks[i]
		assert.Equal(t, startupFindingsMarker, row.Marker)
		assert.Equal(t, doctorWarn, row.Status, "a finding the launch proceeded past is a warn, never info")
		assert.Contains(t, row.Detail, f.Message, "the finding's own wording must survive")
		assert.Contains(t, row.Detail, "["+string(f.Class)+"]", "the class tag is how the human's abort listing reads; keep one language")
	}
	assert.Contains(t, report.Checks[0].Detail, "fix: did you mean `runtime`?")
	assert.NotContains(t, report.Checks[1].Detail, "fix:", "a finding with no fix-it must not grow an empty one")
}

// TestStartupFindingsReport_CleanProjectYieldsNothing is the "nothing to
// say" contract: a project with nothing wrong produces an EMPTY report, so
// the launch attaches nothing rather than a list of green rows.
func TestStartupFindingsReport_CleanProjectYieldsNothing(t *testing.T) {
	cfg := cleanProject(t)
	report := startupFindingsReport(cfg, nil)
	assert.Empty(t, report.Checks)
}

// TestStartupFindingsReport_AbsentLocalStateIsAFinding: a fresh init has none
// of the must-exist local-only paths yet (the task-log project-id marker
// among them), and the agent about to work in it is told so in doctor's own
// words.
func TestStartupFindingsReport_AbsentLocalStateIsAFinding(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg = noCompanions(t, cfg)

	report := startupFindingsReport(cfg, nil)

	require.Len(t, report.Checks, 1)
	want := doctorCheckLocalTierState(cfg)
	assert.Equal(t, want, report.Checks[0], "the row IS doctor's row — same marker, status and detail")
}

// TestStartupFindingsReport_WithheldCompanionIsAFinding: the companions
// check reports ok even when a companion was found and NOT RUN, or is not
// installed — doctor treats add-ons as never a failure. For the agent those
// are the decisions that matter: the tool it expects is absent. So the row
// is selected on WHAT WAS DECIDED, not on the status.
func TestStartupFindingsReport_WithheldCompanionIsAFinding(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{
			Loadouts: []bundles.CompanionLoadout{
				{Bin: "ltk", Path: "/opt/bin/ltk", Bundle: []byte("version: \"1.0\"\n")},
			},
			Candidates: []bundles.CompanionCandidate{
				{Bin: "taskloom", Path: "/opt/bin/taskloom", Reason: bundles.CandidateUnconsented},
			},
		}, nil
	})

	report := startupFindingsReport(cfg, nil)

	require.Len(t, report.Checks, 1)
	assert.Equal(t, doctorCheckSetupCompanions(cfg, nil), report.Checks[0])
	assert.Contains(t, report.Checks[0].Detail, "NOT RUN: taskloom (/opt/bin/taskloom)")
}

// TestStartupFindingsReport_CleanCompanionsAreNotAFinding: every discovered
// companion contributing its loadout is the intended state, and the intended
// state is not news.
func TestStartupFindingsReport_CleanCompanionsAreNotAFinding(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{
			Loadouts: []bundles.CompanionLoadout{
				{Bin: "ltk", Path: "/opt/bin/ltk", Bundle: []byte("version: \"1.0\"\n")},
			},
		}, nil
	})

	assert.Empty(t, startupFindingsReport(cfg, nil).Checks)
}

// TestCompanionDecisions_Withheld pins the discriminator the report selects
// on: any companion absent, unconsented or failed is withheld; a catalog of
// only contributing loadouts, or of nothing at all, is not.
func TestCompanionDecisions_Withheld(t *testing.T) {
	assert.False(t, companionDecisions{}.withheld())
	assert.False(t, companionDecisions{contributing: []string{"ltk"}}.withheld())
	assert.True(t, companionDecisions{absent: []string{"reprise"}}.withheld())
	assert.True(t, companionDecisions{notRun: []string{"taskloom (/opt/bin/taskloom)"}}.withheld())
	assert.True(t, companionDecisions{failed: []string{"wedged (/opt/bin/wedged)"}}.withheld())
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
	strictness.SetDegraded(true)
	t.Cleanup(func() { strictness.Reset(); strictness.SetDegraded(false) })
	st := attachFixture(cleanProject(t))
	strictness.Record(strictness.ClassIsolation, "", "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")

	st.attachStartupFindings()

	require.Len(t, st.req.Fragments, 2, "one fragment appended after the assembled context")
	assert.Equal(t, "ASSEMBLED-CONTEXT", st.req.Fragments[0].Content, "the assembled context is untouched")
	delivered := st.req.Fragments[1]
	assert.Equal(t, startupFindingsFragmentName, delivered.Name)
	assert.Contains(t, delivered.Content, "STARTUP-FINDING-REACHES-THE-AGENT: container degraded to host")
	assert.Contains(t, delivered.Content, startupFindingsMarker)
	assert.True(t, strings.HasPrefix(delivered.Content, "ctxloom doctor\n"),
		"rendered by doctor's own renderer, so the agent reads the same surface a human would")
}

// TestAttachStartupFindings_FlagOptsOut: --no-startup-findings leaves the
// request exactly as built, findings or not.
func TestAttachStartupFindings_FlagOptsOut(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(true)
	t.Cleanup(func() { strictness.Reset(); strictness.SetDegraded(false) })
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
