package runtime

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does. The sandbox
// keeps delivery's session-home lock and app-dir walk-up out of the
// developer's real home (testsupport.SandboxedMain).
func TestMain(m *testing.M) {
	engines.MustCompose()
	os.Exit(testsupport.SandboxedMain(m))
}
