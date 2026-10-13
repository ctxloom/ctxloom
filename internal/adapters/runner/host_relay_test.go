package runner_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// reportingStatic is the static writer that skips one item the way a
// command or skill writer does: it reports the skip on the loadout's sink,
// then delivers.
type reportingStatic struct {
	delivery.Static
	skip report.Finding
}

func (s reportingStatic) Deliver(ctx context.Context, lo delivery.Loadout, root engine.Base, target delivery.Target) (delivery.Delivered, error) {
	report.To(lo.Report).Report(s.skip)
	return s.Static.Deliver(ctx, lo, root, target)
}

// The runner's Host returns what executing the launch reported, for the
// StartRun answer to relay — and still hands every finding to its own sink
// (the runner's stderr, which a failed run surfaces).
//
// MUTATION: execute over h.Deps unteed → the relayed assertion goes red.
func TestHost_ExecuteRelaysWhatTheLaunchReported(t *testing.T) {
	s := newSharedProjectMCP(t)
	l := s.resolve(t, "shared", "run-relay")
	skip := report.Finding{Text: "command /deploy skipped: its name collides with a built-in", Remedy: "rename the command"}
	var own report.Collector
	deps := s.deps(t, &recordingDriver{})
	deps.Static = reportingStatic{Static: deps.Static, skip: skip}
	deps.Reporter = &own
	h := &runner.Host{Deps: deps}
	t.Cleanup(func() { _ = h.Teardown(context.Background()) })

	relayed, err := h.Execute(context.Background(), coordgrpc.EncodeLaunch(l))
	require.NoError(t, err)
	assert.Equal(t, report.Findings{skip}, relayed, "what the launch reported is relayed")
	assert.Equal(t, []string{skip.Text}, own.All().Texts(), "and the runner's own sink still has it")
}

// The runner warns, before the engine spawns, of each file in the project
// that holds context an earlier `ctxloom materialize` wrote (R5; materialize
// tests 34-36) — on every launch path, since every launch is a runner's —
// and the warning reaches the originator on the StartRun answer like any
// other finding the launch raised (task #19). Wording and remedy are R5's.
//
// MUTATION: drop the R5 check from Execute → red.
func TestHost_ExecuteWarnsOfContextAMaterializeLeftInTheProject(t *testing.T) {
	s := newSharedProjectMCP(t)
	l := s.resolve(t, "shared", "run-r5")
	atRest := filepath.Join(s.env.project, "docs", "AGENTS.md")
	b := safefs.NewBatch(afero.NewOsFs(), func(_ string, fn func() error) error { return fn() })
	require.NoError(t, s.rec.In(b).Stage(atRest, delivery.ProjectWriterFor("mock").Of(present.Context),
		[]present.Claim{{Pointer: present.AppendedSection, Value: []byte("ctx")}}))
	_, err := b.Commit()
	require.NoError(t, err)

	var own report.Collector
	deps := s.deps(t, &recordingDriver{})
	deps.Reporter = &own
	h := &runner.Host{Deps: deps}
	t.Cleanup(func() { _ = h.Teardown(context.Background()) })

	relayed, err := h.Execute(context.Background(), coordgrpc.EncodeLaunch(l))
	require.NoError(t, err)
	require.Len(t, relayed, 1, "one warning per file: %v", relayed.Texts())
	assert.False(t, relayed[0].Fatal(), "a warning, not a refusal")
	assert.Contains(t, relayed[0].Text, atRest)
	assert.Contains(t, relayed[0].Text, "holds context an earlier `ctxloom materialize` wrote into this project")
	assert.Equal(t, "ctxloom materialize --release --surface context --target "+s.env.project, relayed[0].Remedy)
}
