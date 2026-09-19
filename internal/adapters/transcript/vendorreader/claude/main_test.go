package claude

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain gives the whole binary the process-wide sandbox: every conversion
// here writes through a transcript.Recorder, which resolves ctxloom's app
// directory, and a cwd walk-up from this package's source directory would
// otherwise adopt whatever .ctxloom sits above the checkout.
// testsupport.Isolate only roots HOME; the sandbox is what closes the rest.
func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }
