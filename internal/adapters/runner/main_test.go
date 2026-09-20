package runner_test

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain registers the shipped engines (the mock among them) and
// sandboxes HOME and the working directory: the writers the runner delivers
// through resolve the session's scratch under the home config dir.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(testsupport.SandboxedMain(m))
}
