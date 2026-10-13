package main

import (
	"os"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// mainArgvEnv turns this test binary into ctxloom. When it is set, TestMain
// runs the REAL main() with the variable's value as argv instead of running
// tests, so a test observes main's own exit status and stderr: the process
// tail an in-process test cannot see, because main ends in os.Exit.
const mainArgvEnv = "CTXLOOM_TEST_MAIN_ARGV"

// TestMain sandboxes this package's tests the way internal/adapters/cli's
// are: a test here drives the real root with the real composition
// (TestLoadoutCommand_EmitsTheV2Envelope), which is the same surface that
// could reach a developer's real ~/.ctxloom, and the sandbox also stamps the
// test binary so the root's version gate does not refuse the run.
//
// The re-exec branch runs BEFORE the sandbox: that child inherits the
// parent's sandbox through the environment and working directory it was
// launched with.
func TestMain(m *testing.M) {
	if argv, ok := os.LookupEnv(mainArgvEnv); ok {
		os.Args = append([]string{"ctxloom"}, strings.Fields(argv)...)
		// The stamp a real build's ldflags give it, or every command past
		// argument validation refuses over the missing stamp.
		testsupport.StampTestBinary()
		main()
		// main always exits with Run's status; this is never reached.
		os.Exit(0)
	}
	os.Exit(testsupport.SandboxedMain(m))
}
