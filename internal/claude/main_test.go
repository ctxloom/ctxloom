package claude

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/selfexec"
)

// TestMain pins the self-exec command (agent.CtxloomCommand → selfexec.Path)
// to the literal "ctxloom" for this package's whole test run. In production
// the running binary IS named ctxloom, so a materialized command's exec
// token always matches agent.IsManaged's hardcoded "ctxloom" bin; under
// `go test`, Path's natural answer is this test binary's own path (e.g.
// ".../claude.test"), which would make every managed-detection/removal test
// in this package see an unrecognized command. See selfexec.SetPathForTesting.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		restore := selfexec.SetPathForTesting("ctxloom")
		defer restore()
		return m.Run()
	}())
}
