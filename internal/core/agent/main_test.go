package agent_test

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain closes config.findAppDir's walk-up from the working directory for
// every test in this binary, not only the ones that remember to isolate. A
// temp HOME alone does not: see testsupport.SandboxedMain.
//
// It is an EXTERNAL test file (package agent_test) because it composes the
// shipped engines for the external tests that read the registry, and an
// internal test file cannot import a package that imports this one.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(testsupport.SandboxedMain(m))
}
