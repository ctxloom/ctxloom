package backends

import (
	"os"
	"testing"

	claudeengine "github.com/ctxloom/ctxloom/internal/claude/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain sandboxes HOME and the working directory (testsupport.SandboxedMain).
// Writing .mcp.json goes through the §9.7 record store, which
// paths.HomeRecordsDir roots at the REAL ~/.ctxloom/records — so every test
// here that applies hooks against an on-disk temp dir deposited a durable
// record in the developer's own home, naming that temp path. This package was
// the largest single source of them. A temp HOME alone leaves
// config.findAppDir's walk-up from the working directory open; the sandbox
// closes both.
//
// It composes the shipped engines into this package's registry directly
// (this package cannot import the composition root, internal/lm/engines,
// which imports it): the same descriptors, registered once per test binary.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		if err := Register(append(MockDescriptors(), claudeengine.Descriptor())...); err != nil {
			panic(err)
		}

		return testsupport.SandboxedMain(m)
	}())
}
