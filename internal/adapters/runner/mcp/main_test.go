package mcp

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// TestMain composes the shipped engines into the backend registry (the
// coordinators these tests stand up resolve engines by name) and closes
// config.findAppDir's walk-up from the working directory for every test in
// this binary — a temp HOME alone does not: see testsupport.SandboxedMain.
func TestMain(m *testing.M) {
	enginefixture.MustComposeShipped()
	os.Exit(testsupport.SandboxedMain(m))
}
