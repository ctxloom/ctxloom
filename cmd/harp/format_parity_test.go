package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/formatparity"
)

// exitPinArgvEnv turns this test binary into harp: when it is set, TestMain
// runs the REAL main() with the variable's value as argv, so a test observes
// main's own stderr and exit status.
const exitPinArgvEnv = "HARP_EXIT_PIN_ARGV"

func TestMain(m *testing.M) {
	if argv, ok := os.LookupEnv(exitPinArgvEnv); ok {
		os.Args = append([]string{progName}, strings.Fields(argv)...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runMainForExitStatus re-executes this test binary as harp and reports the
// process exit status together with everything it wrote to stderr.
func runMainForExitStatus(t *testing.T, args ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary to re-exec: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), exitPinArgvEnv+"="+strings.Join(args, " "))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stderr.String()
	default:
		t.Fatalf("re-executing the test binary as %s: %v", progName, err)
		return -1, ""
	}
}

// harp's --format plumbing is the family's: the same help, completion,
// default and error tail as every other binary (formatparity.Check).
func TestFormatParity(t *testing.T) {
	formatparity.Check(t, formatparity.Binary{
		Prog: progName,
		Root: newRootCmd,
		Run:  runMainForExitStatus,
		// A generator asked for no names refuses before generating anything.
		FailingArgs: []string{"-n", "0"},
	})
}
