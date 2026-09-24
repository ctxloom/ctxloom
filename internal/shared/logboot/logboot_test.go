package logboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// testProg is the program name these tests install a logger for — not a real
// binary's, so a test that mis-roots HOME cannot pass against a live log.
const testProg = "logboot-test"

// installAt points HOME at a temp dir, installs the process logger for
// testProg, and returns the log path under that HOME plus the flush Install
// handed back. The globals are restored when the test ends so a later test
// never logs into this one's temp dir.
func installAt(t *testing.T, verbose bool) (logPath string, flush func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() { zap.ReplaceGlobals(zap.NewNop()) })
	return filepath.Join(home, ".ctxloom", "logs", testProg+".log"), Install(testProg, verbose)
}

// captureStderr swaps os.Stderr for a pipe and returns what was written to it
// between the swap and the returned stop func. Install reads os.Stderr when it
// BUILDS the logger, so the swap has to be in place before Install runs.
func captureStderr(t *testing.T) (stop func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stderr
	os.Stderr = w

	return func() string {
		os.Stderr = real
		w.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		r.Close()
		return b.String()
	}
}

// The installed logger's sink is <prog>.log, and stderr gets NOTHING. A
// ctxloom-family process is very often a hook, whose stderr the calling engine
// renders as an error (SessionStart) or paints onto the terminal outside the
// alt-screen (statusline), destroying scrollback. A regression here is
// invisible in normal CLI use and corrupts every session.
func TestInstall_LogsToTheProgramsFileAndNotToStderr(t *testing.T) {
	stop := captureStderr(t)
	logPath, flush := installAt(t, false)

	zap.L().Warn("pin_sink_warning")
	flush()

	if stderr := stop(); stderr != "" {
		t.Errorf("the process logger wrote to stderr: %q", stderr)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	if !strings.Contains(string(got), "pin_sink_warning") {
		t.Errorf("log file = %q, want it to carry the warning", got)
	}
}

// Below the configured level, nothing is written anywhere — and because the
// sink opens lazily, not even the file exists. Pins the production recipe at
// warn: a logger that recorded every debug line would turn a per-message hook
// into a disk filler.
func TestInstall_NonVerboseDropsDebugAndCreatesNoFile(t *testing.T) {
	logPath, flush := installAt(t, false)

	zap.L().Debug("pin_debug_entry")
	zap.L().Info("pin_info_entry")
	flush()

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		got, _ := os.ReadFile(logPath)
		t.Errorf("a run that logged nothing at warn or above left %s behind (contents %q)", logPath, got)
	}
}

// CTXLOOM_VERBOSE tees to stderr ON TOP OF the file. An operator who sets it is
// asking for terminal output, which is a different thing from a hook emitting
// it unbidden — but the file must still get the entry, or turning verbose on
// would silently stop populating the log everything else reads.
func TestInstall_VerboseTeesToStderrAndStillWritesTheFile(t *testing.T) {
	stop := captureStderr(t)
	logPath, flush := installAt(t, true)

	zap.L().Warn("pin_verbose_warning")
	flush()

	if stderr := stop(); !strings.Contains(stderr, "pin_verbose_warning") {
		t.Errorf("verbose did not tee to stderr; stderr = %q", stderr)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	if !strings.Contains(string(got), "pin_verbose_warning") {
		t.Errorf("verbose stopped writing the log file; contents = %q", got)
	}
}
