package main

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
)

// reexecEnv, when set to "1" in the test binary's environment, makes TestMain
// run the mock's real entry point (run) over os.Args instead of the test
// suite. It is how the pty test drives the mock as a SEPARATE PROCESS on a
// real pty — the only way to exercise SIGWINCH and the tty line discipline —
// without `go build`ing a second binary or adding a dependency: the test
// binary already links run, so it re-executes itself.
const reexecEnv = "MOCKENGINE_TEST_REEXEC"

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does. Under
// reexecEnv it IS the mock: same registry composition, then run.
func TestMain(m *testing.M) {
	engines.MustRegister()
	if os.Getenv(reexecEnv) == "1" {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}
