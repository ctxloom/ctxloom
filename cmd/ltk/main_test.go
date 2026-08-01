package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// executeFailingUnderFormat drives the REAL command tree — newRootCmd, the one
// the binary runs and the doc generator walks — through a command that fails,
// with --format set to format, and returns every byte the invocation put on the
// error stream, main's own tail included.
//
// The tail is the point. ltk already silences cobra's error print, so the only
// thing that ever describes a failure to the caller is reportExecuteError, and
// the only way to know it honours --format is to run it behind a real Execute
// with a real parsed flag.
func executeFailingUnderFormat(t *testing.T, format string) string {
	t.Helper()
	root := newRootCmd()
	root.AddCommand(&cobra.Command{
		Use:  "zz-fail-under-test",
		RunE: func(*cobra.Command, []string) error { return errors.New("boom") },
	})

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"zz-fail-under-test", "--format", format})

	err := root.Execute()
	if err == nil {
		t.Fatal("the fixture command must fail; with no error there is no error path to pin")
	}
	reportExecuteError(&buf, root, err)
	return buf.String()
}

// TestExecuteError_IsParseableUnderStructuredFormats is the silent-format-lie
// pin. A caller that asked for --format json is reading a JSON stream; when the
// command fails, what it gets must still be JSON. ltk's own "ltk: <err>" line
// is a human sentence, so a script parsing the stream sees valid output right
// up to the one event it most needs to handle, and then does not.
func TestExecuteError_IsParseableUnderStructuredFormats(t *testing.T) {
	got := executeFailingUnderFormat(t, "json")

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatalf("a --format json failure must be parseable JSON (%v), got:\n%s", err, got)
	}
	if envelope.Error != "boom" {
		t.Errorf("envelope carries %q, want the original failure %q", envelope.Error, "boom")
	}
}

// TestExecuteError_HumanLineIsUnchanged pins the other half. ltk has always
// prefixed its terminal error with its own name rather than the family's
// "Error:", and a script grepping for "ltk:" on stderr is a script this change
// must not break: nobody who did not ask for a machine format should be able to
// tell the tail was rewritten. Byte-for-byte, both human formats.
func TestExecuteError_HumanLineIsUnchanged(t *testing.T) {
	for _, format := range []string{"text", "markdown"} {
		if got := executeFailingUnderFormat(t, format); got != "ltk: boom\n" {
			t.Errorf("--format %s got %q, want %q", format, got, "ltk: boom\n")
		}
	}
}

// ---------------------------------------------------------------------------
// Exit-status preservation
// ---------------------------------------------------------------------------

// exitPinArgvEnv turns this test binary into ltk. When it is set, TestMain runs
// the REAL main() with the variable's value as argv instead of running tests, so
// the pin below observes main's own os.Exit — the one thing an in-process test
// of reportExecuteError structurally cannot see, because os.Exit would take the
// test binary down with it.
const exitPinArgvEnv = "LTK_EXIT_PIN_ARGV"

func TestMain(m *testing.M) {
	if argv, ok := os.LookupEnv(exitPinArgvEnv); ok {
		os.Args = append([]string{progName}, strings.Fields(argv)...)
		main()
		// Reached only when Execute returned nil, which is exactly when the
		// real binary falls off the end of main and the process exits 0.
		// Mirroring that keeps a success indistinguishable from the real thing,
		// so a pin expecting 1 fails loudly instead of hanging.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runMainForExitStatus re-executes this test binary as ltk and reports the
// process exit status together with everything it wrote to stderr.
func runMainForExitStatus(t *testing.T, argv string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary to re-exec: %v", err)
	}

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), exitPinArgvEnv+"="+argv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	switch err := cmd.Run(); {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, new(*exec.ExitError)):
		var ee *exec.ExitError
		errors.As(err, &ee)
		return ee.ExitCode(), stderr.String()
	default:
		t.Fatalf("re-executing the test binary as %s: %v", progName, err)
		return -1, ""
	}
}

// TestExecuteError_ExitStatusIsUnchanged is the preservation pin for the one
// thing every caller of a CLI depends on and no output test covers: routing the
// terminal error through the --format filter must not change what the process
// EXITS with. A shell `if ! ltk ...`, a CI step, and a `set -e` script all read
// the status and never the stream, so a tail that reported beautifully and
// exited 0 would be a silent regression of the most load-bearing contract ltk
// has.
//
// `check` takes cobra.NoArgs, so the extra argument is a real argument-
// validation failure returned by Execute — the same path a failing RunE takes —
// and it reaches that failure without touching the filesystem or any config, so
// the pin cannot be perturbed by the host it runs on.
func TestExecuteError_ExitStatusIsUnchanged(t *testing.T) {
	for _, format := range []string{"text", "markdown", "json", "yaml", "toml"} {
		t.Run(format, func(t *testing.T) {
			status, stderr := runMainForExitStatus(t, "check zz-extra-arg --format "+format)
			if status != 1 {
				t.Errorf("exit status %d, want 1; --format must not change what the process exits with\nstderr:\n%s", status, stderr)
			}
			if stderr == "" {
				t.Error("exited nonzero but said nothing on stderr: a silent failure is the one outcome worse than a badly formatted one")
			}
		})
	}
}

// TestExecuteError_StructuredStreamIsParseableInTheRealProcess re-checks the
// json envelope in the actual process rather than against an in-memory buffer.
// The in-process test calls reportExecuteError directly; only this one proves
// that main WIRES it — that the bytes a caller redirecting ltk's stderr
// receives are the parseable ones.
func TestExecuteError_StructuredStreamIsParseableInTheRealProcess(t *testing.T) {
	status, stderr := runMainForExitStatus(t, "check zz-extra-arg --format json")
	if status != 1 {
		t.Fatalf("exit status %d, want 1", status)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("the real process must put parseable JSON on stderr under --format json (%v), got:\n%s", err, stderr)
	}
	if envelope.Error == "" {
		t.Error("the envelope parsed but carries no error text, so the failure reached the caller as an empty success-shaped object")
	}
}

// TestExecuteError_HumanStreamIsUnchangedInTheRealProcess is the same check for
// the default format: a terminal user must see the byte-identical "ltk: <err>"
// line this binary has always printed.
func TestExecuteError_HumanStreamIsUnchangedInTheRealProcess(t *testing.T) {
	status, stderr := runMainForExitStatus(t, "check zz-extra-arg")
	if status != 1 {
		t.Fatalf("exit status %d, want 1", status)
	}
	if !strings.HasPrefix(stderr, progName+": ") {
		t.Errorf("stderr %q must still open with %q", stderr, progName+": ")
	}
	if strings.Contains(stderr, "{") {
		t.Errorf("the default format leaked a structured envelope: %q", stderr)
	}
}
