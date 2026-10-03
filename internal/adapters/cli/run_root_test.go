package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// hostRootFor stands the session's coordinator up the way `ctxloom run` does
// and returns the harp its root is named by.
func hostRootFor(t *testing.T, activeHarp, resume string) string {
	t.Helper()
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	prevBuild := theComposition.NewCoordinator
	theComposition.NewCoordinator = func(app *operations.App, opts coord.Options) (*coord.Coordinator, error) {
		opts.Spawner = coordharness.NopSpawner{}
		return prevBuild(app, opts)
	}
	t.Cleanup(func() { theComposition.NewCoordinator = prevBuild })
	prev := runResumeSession
	runResumeSession = resume
	t.Cleanup(func() { runResumeSession = prev })

	st := &runState{workDir: t.TempDir(), activeHarp: activeHarp}
	teardown := st.hostCoordinator()
	t.Cleanup(teardown)
	require.NotNil(t, st.sessionCoord, "precondition: the session hosts a coordinator")
	return filepath.Base(st.sessionCoord.StateDir())
}

// A fresh `ctxloom run` founds a root of its own, named by the harp it just
// minted: it never claims, adopts or is refused by another session's tree.
func TestHostCoordinator_AFreshRunFoundsItsOwnRoot(t *testing.T) {
	assert.Equal(t, "fresh-harp", hostRootFor(t, "fresh-harp", ""))
}

// `ctxloom run --session H` mints a NEW harp for itself, yet claims root H:
// the resumed session's tree, whose runs it adopts.
func TestHostCoordinator_ResumeClaimsTheResumedRoot(t *testing.T) {
	assert.Equal(t, "resumed-harp", hostRootFor(t, "new-harp", "resumed-harp"))
}
