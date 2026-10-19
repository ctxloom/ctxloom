//go:build integration

package integration

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does. The run sits
// inside testenv.GuardRealHome, which fails it if a test child left a trace in
// the developer's real home.
func TestMain(m *testing.M) {
	testenv.ScrubProcessEnv()
	enginefixture.MustComposeShipped()
	os.Exit(testenv.GuardRealHome(m.Run))
}
