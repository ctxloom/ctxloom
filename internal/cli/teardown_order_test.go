package cli

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/stretchr/testify/require"
)

// orderWS records when its Cleanup ran, relative to a shared sequence.
type orderWS struct {
	seq  *[]string
	dir  string
	fail error
}

func (w *orderWS) Dir() string { return w.dir }
func (w *orderWS) Cleanup() error {
	*w.seq = append(*w.seq, "workspace")
	return w.fail
}

// The ORDER is the invariant: the transport is killed before the workspace it
// runs in is removed. As two separate defers these ran LIFO — workspace first —
// which deleted the scratch tree (config overlays, socket dir, credential mount
// sources) out from under a live transport, and
// isolation.containerWorkspace.Cleanup's own contract is "safe to call once
// after the run's client is killed".
func TestTeardownAll_KillsTransportBeforeRemovingWorkspace(t *testing.T) {
	var seq []string
	st := &runState{
		runnerHandle: &isolation.RunnerHandle{
			Name: "ctxloom-iso-order-probe",
			Kill: func() { seq = append(seq, "transport") },
		},
		ws: &orderWS{seq: &seq, dir: t.TempDir()},
	}

	st.teardownAll()

	require.Equal(t, []string{"transport", "workspace"}, seq,
		"the workspace must outlive the transport that is using it")
}

// teardownAll is deferred BEFORE prepareWorkspace runs, so an early return out
// of the startup gate reaches it with no workspace at all. That must not panic.
func TestTeardownAll_NoWorkspaceYetIsNotAPanic(t *testing.T) {
	var seq []string
	st := &runState{
		runnerHandle: &isolation.RunnerHandle{
			Name: "ctxloom-iso-order-probe",
			Kill: func() { seq = append(seq, "transport") },
		},
	}

	require.NotPanics(t, st.teardownAll,
		"registered before the workspace exists; a nil workspace is the normal early-return case")
	require.Equal(t, []string{"transport"}, seq,
		"the transport must still be torn down when there is no workspace")
}
