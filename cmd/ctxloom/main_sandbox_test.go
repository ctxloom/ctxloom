package main

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes this package's tests the way internal/adapters/cli's
// are: a test here drives the real root with the real composition
// (TestLoadoutCommand_EmitsTheV2Envelope), which is the same surface that
// could reach a developer's real ~/.ctxloom, and the sandbox also stamps the
// test binary so the root's version gate does not refuse the run.
func TestMain(m *testing.M) {
	os.Exit(testsupport.SandboxedMain(m))
}
