package composite_test

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain composes the shipped engines for the golden (it renders per
// registered engine) and closes config's walk-up from the working directory
// (testsupport.SandboxedMain). External package: the golden drives the
// adapters that import composite.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(testsupport.SandboxedMain(m))
}
