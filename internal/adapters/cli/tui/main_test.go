package tui

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestMain closes config.findAppDir's walk-up from the working directory for
// every test in this binary, not only the ones that remember to isolate. A
// temp HOME alone does not: see testsupport.SandboxedMain.
// It also composes the shipped engines into the backend registry, as the CLI
// does: the livetap tests resolve a harp's feed through the registry, which
// is empty in a test binary that does not compose it.
func TestMain(m *testing.M) {
	enginefixture.MustComposeShipped()
	os.Exit(testsupport.SandboxedMain(m))
}
