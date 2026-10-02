package engine

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does. It also
// sandboxes HOME and the working directory: engine settings writes lock under
// the home lock directory (paths.HomePathFor), which refuses a real home from
// a test binary.
func TestMain(m *testing.M) {
	enginefixture.MustComposeShipped()
	os.Exit(testsupport.SandboxedMain(m))
}
