package conformance_test

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes the process: PresentAll drives claude's real writers,
// whose MCP write goes through the record store paths.HomeRecordsDir roots
// at the REAL ~/.ctxloom/records, and the store refuses to run from a test
// binary outside a temp root.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
