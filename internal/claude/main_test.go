package claude

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMain pins the self-exec command (agent.CtxloomCommand → selfexec.Path)
// to the literal "ctxloom" for this package's whole test run. In production
// the running binary IS named ctxloom, so a materialized command's exec
// token always matches agent.IsManaged's hardcoded "ctxloom" bin; under
// `go test`, Path's natural answer is this test binary's own path (e.g.
// ".../claude.test"), which would make every managed-detection/removal test
// in this package see an unrecognized command. See selfexec.SetPathForTesting.
//
// The rest of the run is the process-wide sandbox. Writing .mcp.json goes
// through the §9.7 record store, which paths.HomeRecordsDir roots at the REAL
// ~/.ctxloom/records — so every test here that calls WriteSettings against
// an on-disk temp dir once deposited a record in the developer's own home.
// Redirecting HOME alone left the cwd walk-up open; the sandbox closes both.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		restore := selfexec.SetPathForTesting("ctxloom")
		defer restore()
		return testsupport.SandboxedMain(m)
	}())
}
