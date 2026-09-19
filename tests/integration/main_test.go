//go:build integration

package integration

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(m.Run())
}
