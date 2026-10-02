package companions

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes the package binary's HOME and cwd: a probe writes the
// home-scoped last-known loadout record, and no test may reach the
// developer's real ~/.ctxloom.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
