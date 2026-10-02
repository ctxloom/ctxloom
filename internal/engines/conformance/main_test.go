//go:build conformance

package conformance

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes the process: the suite drives the real static writer,
// which takes file locks under the home.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
