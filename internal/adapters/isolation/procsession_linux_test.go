package isolation

import "testing"

// TestIsolateRunner_ForeignRunnerDiesWithItsHost pins that PR_SET_PDEATHSIG
// (setRunnerPdeathsig) takes down a runner that carries no watcher of its
// own — a foreign `sleep`. Linux-only because nothing else can: darwin/BSD
// have no kernel parent-death attribute, and their in-child watch
// (parentwatch) only runs inside our own binary; that path is
// TestIsolateRunner_RunnerDiesWithItsHost.
func TestIsolateRunner_ForeignRunnerDiesWithItsHost(t *testing.T) {
	assertRunnerDiesWithItsHost(t, "", "sleep", "100")
}
