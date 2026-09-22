package backends

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestMain composes the shipped engines once per test binary and sandboxes
// the process: claude's record-backed settings write goes through the
// record store rooted at the REAL ~/.ctxloom/records, and the store refuses
// to run from a test binary outside a temp root.
func TestMain(m *testing.M) {
	enginefixture.MustComposeShipped()
	os.Exit(testsupport.SandboxedMain(m))
}
