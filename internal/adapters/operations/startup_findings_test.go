package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestStartupFindings_RecordedFindingsBecomeRows: every finding the
// launch itself recorded (a config warning, a degraded isolation axis, a sync
// failure) is a row in the report, carrying the finding's own message and
// its fix-it — the wording the human already saw on stderr, not a paraphrase.
func TestStartupFindings_RecordedFindingsBecomeRows(t *testing.T) {
	cfg := cleanProject(t)
	recorded := []strictness.Finding{
		{Class: strictness.ClassConfig, Message: "unknown key `runtme` in config.yaml: ctxloom does not know it, so it is IGNORED", FixIt: "did you mean `runtime`?"},
		{Class: strictness.ClassIsolation, Message: "container runtime requested but no runtime is reachable; running on the host"},
	}

	report := StartupFindings(&App{}, cfg, isolatedHome(t), recorded)

	require.Len(t, report.Checks, 2)
	for i, f := range recorded {
		row := report.Checks[i]
		assert.Equal(t, StartupFindingsMarker, row.Marker)
		assert.Equal(t, DoctorWarn, row.Status, "a finding the launch proceeded past is a warn, never info")
		assert.Contains(t, row.Detail, f.Message, "the finding's own wording must survive")
		assert.Contains(t, row.Detail, "["+string(f.Class)+"]", "the class tag is how the human's abort listing reads; keep one language")
	}
	assert.Contains(t, report.Checks[0].Detail, "fix: did you mean `runtime`?")
	assert.NotContains(t, report.Checks[1].Detail, "fix:", "a finding with no fix-it must not grow an empty one")
}

// TestStartupFindings_CleanProjectYieldsNothing is the "nothing to
// say" contract: a project with nothing wrong produces an EMPTY report, so
// the launch attaches nothing rather than a list of green rows.
func TestStartupFindings_CleanProjectYieldsNothing(t *testing.T) {
	cfg := cleanProject(t)
	report := StartupFindings(&App{}, cfg, isolatedHome(t), nil)
	assert.Empty(t, report.Checks)
}

// TestStartupFindings_AbsentLocalStateIsAFinding: a fresh init has none
// of the must-exist local-only paths yet (the task-log project-id marker
// among them), and the agent about to work in it is told so in doctor's own
// words.
func TestStartupFindings_AbsentLocalStateIsAFinding(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg = noCompanions(t, cfg)

	report := StartupFindings(&App{}, cfg, isolatedHome(t), nil)

	require.Len(t, report.Checks, 1)
	want := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, want, report.Checks[0], "the row IS doctor's row — same marker, status and detail")
}

// TestStartupFindings_WithheldCompanionIsAFinding: the companions
// check reports ok even when a companion was found and NOT RUN, or is not
// installed — doctor treats add-ons as never a failure. For the agent those
// are the decisions that matter: the tool it expects is absent. So the row
// is selected on WHAT WAS DECIDED, not on the status.
func TestStartupFindings_WithheldCompanionIsAFinding(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{
			Loadouts: []bundles.CompanionLoadout{
				{Bin: "ltk", Path: "/opt/bin/ltk", Document: []byte("run:\n  version: \"1.0\"\n")},
			},
			Candidates: []bundles.CompanionCandidate{
				{Bin: "taskloom", Path: "/opt/bin/taskloom", Reason: bundles.CandidateUnconsented},
			},
		}, nil
	})

	report := StartupFindings(&App{}, cfg, isolatedHome(t), nil)

	require.Len(t, report.Checks, 1)
	assert.Equal(t, doctorCheckSetupCompanions(cfg, nil, false), report.Checks[0])
	assert.Contains(t, report.Checks[0].Detail, "NOT RUN: taskloom (/opt/bin/taskloom)")
}

// TestStartupFindings_CleanCompanionsAreNotAFinding: every discovered
// companion contributing its loadout is the intended state, and the intended
// state is not news.
func TestStartupFindings_CleanCompanionsAreNotAFinding(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{
			Loadouts: []bundles.CompanionLoadout{
				{Bin: "ltk", Path: "/opt/bin/ltk", Document: []byte("run:\n  version: \"1.0\"\n")},
			},
		}, nil
	})

	assert.Empty(t, StartupFindings(&App{}, cfg, isolatedHome(t), nil).Checks)
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
