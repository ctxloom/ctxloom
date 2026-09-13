package transcript

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain gives the whole binary the process-wide sandbox: the Recorder
// tests resolve ctxloom's app directory, and a cwd walk-up from this package's
// source directory would otherwise reach whatever .ctxloom sits above the
// checkout. testsupport.Isolate only roots HOME; the sandbox closes the rest.
func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }
