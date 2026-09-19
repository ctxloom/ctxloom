package mcp

import (
	"github.com/ctxloom/ctxloom/internal/engines"
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain closes config.findAppDir's walk-up from the working directory for
// every test in this binary, not only the ones that remember to isolate. A
// temp HOME alone does not: see testsupport.SandboxedMain.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(testsupport.SandboxedMain(m))
}
