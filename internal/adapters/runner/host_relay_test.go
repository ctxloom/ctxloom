package runner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
