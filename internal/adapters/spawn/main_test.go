package spawn

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestMain composes the shipped engines into the backend registry the way the
// CLI does — registration is explicit, and a spawner test that resolves a
// binding's engine reads the registry — and closes config.findAppDir's walk-up
// from the working directory for every test in this binary.
func TestMain(m *testing.M) {
	enginefixture.MustComposeShipped()
	os.Exit(testsupport.SandboxedMain(m))
}
