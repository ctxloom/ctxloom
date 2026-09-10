package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// recordingContextWriter remembers every WriteContext it is given, so a test
// can assert on the CALLS rather than on a file's contents. The stripping
// behaviour under test was a second write carrying "" — invisible if you only
// look at the end state of a file that a later run rewrites anyway.
type recordingContextWriter struct{ writes []string }

func (w *recordingContextWriter) WriteContext(req ContextWriteRequest) (ContextReport, error) {
	w.writes = append(w.writes, req.Context)
	return ContextReport{}, nil
}

// A project surface SURVIVES the run that delivered it.
//
// The reversal used to re-write the context with "", which strips the managed
// section — so whether CLAUDE.md still existed after a session depended on
// whether the process exited cleanly. It never ran on SIGKILL, a crash, or a
// container stop, so leftover surfaces had to be tolerated anyway; keeping the
// teardown bought no safety and made the end state depend on how the process
// died.
//
// Asserted on the WRITE COUNT, deliberately. Asserting the file still exists
// would pass for the wrong reason — nothing in this test rewrites it — and
// would keep passing if the strip were reintroduced through a different route.
// The claim is that cleanup ISSUES NO WRITE.
func TestDeliverManagedContext_LeavesTheSurfaceInPlaceOnExit(t *testing.T) {
	testsupport.Isolate(t)

	w := &recordingContextWriter{}
	handle, err := DeliverManagedContext(w, t.TempDir(), "MANAGED-BODY")
	require.NoError(t, err)
	require.NotNil(t, handle, "delivery must hand back a handle to reverse")
	require.Equal(t, []string{"MANAGED-BODY"}, w.writes, "delivery writes the body once")

	require.NoError(t, handle.Cleanup(), "cleanup must not error")

	assert.Equal(t, []string{"MANAGED-BODY"}, w.writes,
		"cleanup issued a second write; a write carrying \"\" is what strips the managed section and removes the file. Startup reconciles the surface, and `ctxloom clean` / `ctxloom manage uninstall` are what remove it — not process exit")
}

// The shared no-op handle is what the project-surface deliveries return. It is
// pinned separately because its CONTRACT is the thing that must not drift: a
// future delivery that wants real teardown must be per-session SCRATCH, which
// nothing reconciles and which `clean` cannot see, and must not reach for this.
func TestSurfacePersistsAfterExit_ReversesNothing(t *testing.T) {
	require.NotNil(t, SurfacePersistsAfterExit)
	assert.NoError(t, SurfacePersistsAfterExit.Cleanup(),
		"the shared project-surface handle must be a successful no-op")
}
