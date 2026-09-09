package backends

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
// ".../backends.test"), which would make every managed-detection/removal
// test in this package see an unrecognized command. See
// selfexec.SetPathForTesting.
//
// It also redirects HOME. Writing .mcp.json goes through the §9.7 record
// store, which paths.HomeRecordsDir roots at the REAL ~/.ctxloom/records — so
// every test here that applies hooks against an on-disk temp dir deposited a
// durable record in the developer's own home, naming that temp path. This
// package was the largest single source of them.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		restore := selfexec.SetPathForTesting("ctxloom")
		defer restore()

		home, err := os.MkdirTemp("", "backends-test-home")
		if err != nil {
			panic(err)
		}
		defer func() { _ = os.RemoveAll(home) }()
		os.Setenv("HOME", home) //nolint:forbidigo // no *testing.T in TestMain

		return m.Run()
	}())
}
