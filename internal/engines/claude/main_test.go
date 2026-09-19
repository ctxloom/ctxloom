package claude

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain is the process-wide sandbox for this package. Writing .mcp.json goes
// through the §9.7 record store, which paths.HomeRecordsDir roots at the REAL
// ~/.ctxloom/records — so every test here that calls WriteSettings against
// an on-disk temp dir once deposited a record in the developer's own home.
// Redirecting HOME alone left the cwd walk-up open; the sandbox closes both.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
