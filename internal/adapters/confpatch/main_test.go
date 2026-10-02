package confpatch

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes HOME and the working directory for every test in this
// binary: locked writes resolve their lock under the home lock directory
// (paths.HomePathFor), which refuses a real home from a test binary.
func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }
