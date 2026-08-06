package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for a pipe and returns what was written to it
// between the swap and the returned stop func. loggerConstructor reads
// os.Stderr when it BUILDS the logger, so the swap has to be in place before
// the constructor runs.
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

// logHome points HOME at a temp dir and returns the log path under it.
func logHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".ctxloom", "logs", "ctxloom.log")
}

// The process logger's sink is the log file, and stderr gets NOTHING. This is
// the whole point of the sink: a ctxloom process is very often a hook, whose
// stderr the calling engine renders as an error (SessionStart) or paints onto
// the terminal outside the alt-screen (statusline), destroying scrollback. A
// regression here is invisible in normal CLI use and corrupts every session.
func TestLoggerConstructor_LogsToTheFileAndNotToStderr(t *testing.T) {
	logPath := logHome(t)
	stop := captureStderr(t)

	lg, err := loggerConstructor(false)()
	if err != nil {
		t.Fatalf("loggerConstructor: %v", err)
	}
	lg.Warn("pin_sink_warning")
	_ = lg.Sync()

	stderr := stop()
	if stderr != "" {
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

// Below the configured level, nothing is written anywhere. Pins the production
// recipe at warn: a logger that recorded every debug line would turn the
// statusline hook — which runs once per assistant message — into a disk filler.
func TestLoggerConstructor_NonVerboseDropsDebug(t *testing.T) {
	logPath := logHome(t)

	lg, err := loggerConstructor(false)()
	if err != nil {
		t.Fatalf("loggerConstructor: %v", err)
	}
	lg.Debug("pin_debug_entry")
	_ = lg.Sync()

	got, _ := os.ReadFile(logPath)
	if strings.Contains(string(got), "pin_debug_entry") {
		t.Error("a debug entry was recorded by the non-verbose logger")
	}
}

// CTXLOOM_VERBOSE tees to stderr ON TOP OF the file. An operator who sets it is
// asking for terminal output, which is a different thing from a hook emitting
// it unbidden — but the file must still get the entry, or turning verbose on
// would silently stop populating the log everything else reads.
func TestLoggerConstructor_VerboseTeesToStderrAndStillWritesTheFile(t *testing.T) {
	logPath := logHome(t)
	stop := captureStderr(t)

	lg, err := loggerConstructor(true)()
	if err != nil {
		t.Fatalf("loggerConstructor: %v", err)
	}
	lg.Warn("pin_verbose_warning")
	_ = lg.Sync()

	stderr := stop()
	if !strings.Contains(stderr, "pin_verbose_warning") {
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
