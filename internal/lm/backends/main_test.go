package backends

import (
	"os"
	"testing"

	claudeengine "github.com/ctxloom/ctxloom/internal/claude/engine"
	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain pins the self-exec command (agent.CtxloomCommand → selfexec.Path)
// to the literal "ctxloom" for this package's whole test run. In production
// the running binary IS named ctxloom, so a materialized command's exec
// token always matches agent.IsManaged's hardcoded "ctxloom" bin; under
// `go test`, Path's natural answer is this test binary's own path (e.g.
// ".../backends.test"), which would make every managed-detection/removal
// test in this package see an unrecognized command. See
// selfexec.SetPathForTesting.
//
// It also sandboxes HOME and the working directory (testsupport.SandboxedMain).
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
		restore := selfexec.SetPathForTesting("ctxloom")
		defer restore()
		if err := Register(append(MockDescriptors(), claudeengine.Descriptor())...); err != nil {
			panic(err)
		}

		return testsupport.SandboxedMain(m)
	}())
}
