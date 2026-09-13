package coord

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain closes config.findAppDir's walk-up from the working directory for
// every test in this binary, not only the ones that remember to isolate. A
// temp HOME alone does not: see testsupport.SandboxedMain.
// It also composes the shipped engines into the backend registry: registration
// is explicit (no init), so a test binary that reads the registry must compose
// it the same way the CLI does. Without this the registry is empty here and an
// engine capability lookup finds nothing, so a refusal that depends on a
// declared capability silently does not fire.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(testsupport.SandboxedMain(m))
}
