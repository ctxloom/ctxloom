package claude

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain is the process-wide sandbox for this package. Delivery takes file
// locks under the home, and a status read resolves the home records dir, so
// an unsandboxed test would reach the developer's own ~/.ctxloom. Redirecting
// HOME alone left the cwd walk-up open; the sandbox closes both.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
