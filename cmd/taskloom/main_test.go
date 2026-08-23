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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// executeFailingUnderFormat drives the REAL rootCmd — the tree the binary ships
// — through a command that fails, with --format set to format, and returns
// every byte the invocation put on the error stream: cobra's own tail and
// main's tail both.
//
// It has to run the whole Execute path rather than call the emitter directly
// because the defect this file pins lives in the seam between the two. cobra
// prints a terminal error itself unless SilenceErrors is set, so a binary that
// adds a structured tail without silencing cobra emits BOTH — a stream that is
// worse than either, and one that no test of the emitter alone can see.
func executeFailingUnderFormat(t *testing.T, format string) string {
	t.Helper()
	fail := &cobra.Command{
		Use:  "zz-fail-under-test",
		RunE: func(*cobra.Command, []string) error { return errors.New("boom") },
	}
	rootCmd.AddCommand(fail)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"zz-fail-under-test", "--format", format})

	// rootCmd is package-global and shared with every other test in this
	// package, so this helper has to defend both directions — see
	// resetGlobalFormatFlags's own doc for why both an inbound and an
	// outbound reset are needed here.
	resetGlobalFormatFlags()

	t.Cleanup(func() {
		rootCmd.RemoveCommand(fail)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		resetGlobalFormatFlags()
		clidiag.SetStructured(false)
	})

	err := rootCmd.Execute()
	require.Error(t, err, "the fixture command must fail; with no error there is no error path to pin")
	reportExecuteError(&buf, err)
	return buf.String()
}

// resetGlobalFormatFlags restores rootCmd's persistent --format and --json
// flags to their zero state (default value, Changed=false).
//
// rootCmd is package-global and shared by every test in this package, so any
// test that drives it through Execute (or otherwise sets these flags
// directly) has to defend both directions:
//
// INBOUND: cliemit.Resolve honours a Changed --json flag as --format json
// BEFORE it ever reads --format. Go runs this package's tests in source-file
// order, and loadout_test.go sorts ahead of this file, so a test there that
// leaves --json Changed (runLoadout's callers) leaks into whichever test
// here runs next — resetting only on the way out is not enough when the leak
// comes from another file.
//
// OUTBOUND: put back everything a test touched, each parsed flag's Changed
// bit included, or the next test — in this file or any other — inherits a
// format nobody set. loadout_test.go's own callers reset both directions for
// the same reason; this function is the one place both files call so the
// reset logic is defined once.
func resetGlobalFormatFlags() {
	for _, name := range []string{"format", "json"} {
		f := rootCmd.PersistentFlags().Lookup(name)
		if f == nil {
			continue
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
}

// TestExecuteError_IsParseableUnderStructuredFormats is the silent-format-lie
// pin. A caller that asked for --format json is reading a JSON stream; if the
// command then fails, the bytes it gets must still be JSON. Emitting cobra's
// human "Error: ..." line there hands that caller a stream which is valid right
// up until the one event it most needs to handle, and then is not.
func TestExecuteError_IsParseableUnderStructuredFormats(t *testing.T) {
	got := executeFailingUnderFormat(t, "json")

	var envelope struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &envelope),
		"a --format json failure must be parseable JSON, got:\n%s", got)
	assert.Equal(t, "boom", envelope.Error, "the envelope must carry the original failure")
}

// TestExecuteError_HumanTextIsUnchanged is the other half: the fix must not be
// visible to anyone who did not ask for a machine format. "Error: <msg>" on
// stderr is exactly what cobra printed before this binary took the tail over,
// so this is a byte-for-byte preservation pin, not a description of a new
// behaviour.
func TestExecuteError_HumanTextIsUnchanged(t *testing.T) {
	assert.Equal(t, "Error: boom\n", executeFailingUnderFormat(t, "text"))
}

// TestExecuteError_MarkdownUsesTheMarkdownErrorLine records the one output that
// this change does move. Under --format markdown cobra used to print the plain
// text line, because cobra has never known what --format is; routing the tail
// through the shared filter gives markdown its own rendering, the same one
// cmd/ctxloom has emitted since the filter's error half landed.
func TestExecuteError_MarkdownUsesTheMarkdownErrorLine(t *testing.T) {
	assert.Equal(t, "**Error:** boom\n", executeFailingUnderFormat(t, "markdown"))
}

// ---------------------------------------------------------------------------
// Exit-status preservation
// ---------------------------------------------------------------------------

// exitPinArgvEnv turns this test binary into taskloom. When it is set, TestMain
// runs the REAL main() with the variable's value as argv instead of running
// tests, so the pin below observes main's own os.Exit — the one thing an
// in-process test of reportExecuteError structurally cannot see, because
// os.Exit would take the test binary down with it.
const exitPinArgvEnv = "TASKLOOM_EXIT_PIN_ARGV"

func TestMain(m *testing.M) {
	if argv, ok := os.LookupEnv(exitPinArgvEnv); ok {
		os.Args = append([]string{"taskloom"}, strings.Fields(argv)...)
		main()
		// Reached only when Execute returned nil, which is exactly when the
		// real binary falls off the end of main and the process exits 0.
		// Mirroring that keeps a success indistinguishable from the real thing,
		// so a pin expecting 1 fails loudly instead of hanging.
		os.Exit(0)
	}
	// SandboxedMain, not m.Run: it installs a temp HOME and a temp working
	// directory for the whole binary, which is what closes config.findAppDir's
	// walk-up from the working directory into this checkout's .ctxloom. The
	// branch above deliberately runs BEFORE it — that child inherits the
	// parent's sandbox through the environment and cwd it was launched with.
	os.Exit(testsupport.SandboxedMain(m))
}

// runMainForExitStatus re-executes this test binary as taskloom and reports the
// process exit status together with everything it wrote to stderr.
func runMainForExitStatus(t *testing.T, argv string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err, "locating the test binary to re-exec")

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), exitPinArgvEnv+"="+argv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	runErr := cmd.Run()
	if runErr == nil {
		return 0, stderr.String()
	}
	var ee *exec.ExitError
	require.ErrorAs(t, runErr, &ee, "re-executing the test binary as taskloom")
	return ee.ExitCode(), stderr.String()
}

// TestExecuteError_ExitStatusIsUnchanged is the preservation pin for the one
// thing every caller of a CLI depends on and no output test covers: routing the
// terminal error through the --format filter must not change what the process
// EXITS with. A shell `if ! taskloom ...`, a CI step, and a `set -e` script all
// read the status and never the stream, so a tail that reported beautifully and
// exited 0 would be a silent regression of the most load-bearing contract this
// binary has — and taskloom is scripted (see the CLI-over-MCP note in the
// project's own notes), so the status is the interface.
//
// `show` takes cobra.ExactArgs(1), so omitting the argument is a real argument-
// validation failure returned by Execute — the same path a failing RunE takes —
// and it reaches that failure without opening the task store, so the pin cannot
// be perturbed by the host it runs on and cannot write to a real project log.
func TestExecuteError_ExitStatusIsUnchanged(t *testing.T) {
	for _, format := range []string{"text", "markdown", "json", "yaml", "toml"} {
		t.Run(format, func(t *testing.T) {
			status, stderr := runMainForExitStatus(t, "show --format "+format)
			assert.Equal(t, 1, status,
				"--format must not change what the process exits with; stderr:\n%s", stderr)
			assert.NotEmpty(t, stderr,
				"exited nonzero but said nothing: a silent failure is the one outcome worse than a badly formatted one")
		})
	}
}

// TestExecuteError_StructuredStreamIsParseableInTheRealProcess re-checks the
// json envelope in the actual process rather than against an in-memory buffer.
// The in-process test calls reportExecuteError directly; only this one proves
// that main WIRES it, and that cobra's own "Error:" line really is silenced —
// if SilenceErrors were dropped, both lines would land here and the stream
// would stop being parseable even though the in-process test still passed.
func TestExecuteError_StructuredStreamIsParseableInTheRealProcess(t *testing.T) {
	status, stderr := runMainForExitStatus(t, "show --format json")
	require.Equal(t, 1, status)

	var envelope struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stderr), &envelope),
		"the real process must put parseable JSON on stderr under --format json, got:\n%s", stderr)
	assert.NotEmpty(t, envelope.Error,
		"the envelope parsed but carries no error text, so the failure reached the caller as an empty success-shaped object")
}

// TestExecuteError_HumanStreamIsUnchangedInTheRealProcess is the same check for
// the default format: a terminal user must see the byte-identical "Error: ..."
// line cobra printed before this binary took the tail over.
func TestExecuteError_HumanStreamIsUnchangedInTheRealProcess(t *testing.T) {
	status, stderr := runMainForExitStatus(t, "show")
	require.Equal(t, 1, status)
	assert.True(t, strings.HasPrefix(stderr, "Error: "),
		"stderr %q must still open with %q", stderr, "Error: ")
	assert.NotContains(t, stderr, "{", "the default format leaked a structured envelope")
}
