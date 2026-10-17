package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// brokenParentCfg is hookedMaterializeCfg whose `reviewer` profile also
// names a parent that does not exist: assembly still composes the rest, and
// raises a fatal-class KindRef finding for the missing parent.
func brokenParentCfg(t *testing.T, mark string) *config.Config {
	t.Helper()
	cfg := materializeCfg(t, mark, "")
	appPaths := cfg.ToFixture().AppPaths
	require.NotEmpty(t, appPaths)
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(bundletree.ProjectProfilesDir(t, appPaths[0]), "reviewer.yaml"),
		"parents:\n  - ctxloom+local:project#profiles/absent-parent\n"+
			"select_tags:\n  - security\n"+
			"hooks:\n  unified:\n    session_start:\n      - type: command\n        command: echo team-guardrail\n", 0o644)
	// The production composition's sink: the loader's findings reach the
	// strictness ledger, as App wires them.
	cfg.SetReporter(strictness.Sink("ctxloom"))
	return cfg
}

// recordFiles is every file under the ownership-record directory: the
// records and the locks taken to write them.
func recordFiles(t *testing.T) []string {
	t.Helper()
	dir, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	return deliverytest.RelativeFiles(afero.NewOsFs(), dir)
}

// TestMaterialize_AFatalAssemblyFindingWritesNothing: in strict mode a
// fatal-class finding raised while ANY engine's package is assembled refuses
// the whole delivery before anything is written — no engine file for either
// engine (the first engine assembled cleanly before the second was even
// looked at), no target directory created, no ownership record or lock —
// and the refusal carries the finding.
func TestMaterialize_AFatalAssemblyFindingWritesNothing(t *testing.T) {
	cfg := brokenParentCfg(t, "GATED-MARK")
	target := filepath.Join(t.TempDir(), "not-yet")
	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })

	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Target: target, Explicit: true, Engines: []string{"mock", "claude-code"},
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, ErrMaterializeRefused), "got %v", err)
	var refusal MaterializeRefusal
	require.True(t, errors.As(err, &refusal))
	require.NotEmpty(t, refusal.Findings)
	assert.Equal(t, report.KindRef, refusal.Findings[0].Kind)
	assert.Contains(t, refusal.Findings[0].Text, "absent-parent")
	assert.Contains(t, err.Error(), "absent-parent", "the refusal names what it refused over")

	assert.NoDirExists(t, target, "the target is not even created")
	assert.Empty(t, recordFiles(t), "no ownership record, no lock")
	assert.Equal(t, refusal.Findings, strictness.Since(mark), "the findings stay on the ledger for the caller's own gate")
}

// TestMaterialize_DegradedStillWritesAndWarns: under --degraded the same
// finding is waived — the delivery writes as it always did, and the finding
// is still recorded (degraded suppresses fatality, not recording).
func TestMaterialize_DegradedStillWritesAndWarns(t *testing.T) {
	cfg := brokenParentCfg(t, "DEGRADED-MARK")
	target := t.TempDir()
	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })

	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Target: target, Explicit: true, Engines: []string{"mock"},
		Strictness: strictness.Mode{Prog: "ctxloom", Degraded: true},
	})
	require.NoError(t, err)
	assert.Equal(t, MaterializeApplied, res.Status)
	assert.Contains(t, fileIn(t, target, mock.ContextFileName), "DEGRADED-MARK")
	assert.NotEmpty(t, recordFiles(t))
	found := strictness.Since(mark)
	require.NotEmpty(t, found, "degraded suppresses fatality, not recording")
	assert.Contains(t, found[0].Text, "absent-parent")
}

// TestMaterialize_ADryRunIsNotGatedHere: a dry run writes nothing either
// way, so it reports its plan and leaves the finding to the caller's gate.
func TestMaterialize_ADryRunIsNotGatedHere(t *testing.T) {
	cfg := brokenParentCfg(t, "DRY-MARK")
	target := t.TempDir()
	res, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Target: target, Explicit: true, Engines: []string{"mock"}, DryRun: true,
	})
	require.NoError(t, err)
	assert.Equal(t, MaterializePlanned, res.Status)
}

// TestRunSyncPostSteps_AFatalFindingRefusesTheConsentedRefresh (ruling 2):
// the post-sync refresh under --yes goes through the same gate: a fatal
// finding refuses it before anything is written, and the sync's result
// carries the findings for the command to exit on.
func TestRunSyncPostSteps_AFatalFindingRefusesTheConsentedRefresh(t *testing.T) {
	cfg := brokenParentCfg(t, "SYNC-GATED-MARK")
	root := t.TempDir()
	t.Setenv("CTXLOOM_ROOT", root)
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = Materialize

	result := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), cfg, strictness.Mode{Prog: "ctxloom"},
		SyncDependenciesRequest{ApplyHooks: true, WriteProject: true}, result, afero.NewOsFs())

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is written into the project directory")
	assert.Empty(t, recordFiles(t), "nothing is claimed")
	require.NotEmpty(t, result.RefreshFindings, "the refusal reaches the command")
	assert.Contains(t, result.RefreshFindings[0].Text, "absent-parent")

	// --degraded: the refresh writes as before, and nothing is left to exit on.
	degraded := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), cfg, strictness.Mode{Prog: "ctxloom", Degraded: true},
		SyncDependenciesRequest{ApplyHooks: true, WriteProject: true}, degraded, afero.NewOsFs())
	assert.Contains(t, fileIn(t, root, ".claude/settings.json"), "team-guardrail")
	assert.Empty(t, degraded.RefreshFindings)
}

// TestRunSyncPostSteps_AnUnconsentedRefreshIsNotGated: without --yes the
// refresh only plans; the pull itself succeeded, so nothing is left to exit
// on (the warning still streams).
func TestRunSyncPostSteps_AnUnconsentedRefreshIsNotGated(t *testing.T) {
	cfg := brokenParentCfg(t, "SYNC-PLAN-MARK")
	t.Setenv("CTXLOOM_ROOT", t.TempDir())
	orig := syncHooksStep
	t.Cleanup(func() { syncHooksStep = orig })
	syncHooksStep = Materialize
	result := &SyncDependenciesResult{Total: 1}
	runSyncPostSteps(context.Background(), engines.Registry(), cfg, strictness.Mode{Prog: "ctxloom"},
		SyncDependenciesRequest{ApplyHooks: true}, result, afero.NewOsFs())
	assert.Empty(t, result.RefreshFindings)
}

// the seam is still the real Materialize's shape.
var _ func(context.Context, engine.Registry, *config.Config, MaterializeRequest) (*MaterializeResult, error) = Materialize
