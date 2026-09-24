package logsink

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testProg is the program name these tests open logs for. Not "ctxloom": a
// name no real binary uses keeps a mis-rooted HOME from being mistaken for a
// passing test against the live log.
const testProg = "logsink-test"

// homeAt points HOME at a fresh temp dir and returns the log path that should
// result from it. t.Setenv restores the real HOME when the test ends.
func homeAt(t *testing.T) (home, logPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, filepath.Join(home, ".ctxloom", "logs", testProg+".log")
}

// Open must CREATE the logs directory. ~/.ctxloom/logs does not exist on a
// machine that has never run this build, so a sink that only worked where the
// directory already happened to exist would leave every fresh install with no
// log at all — precisely the case the log exists to cover.
func TestOpen_CreatesTheLogsDirectory(t *testing.T) {
	_, logPath := homeAt(t)

	f, err := Open(testProg)
	if err != nil {
		t.Fatalf("Open on a home with no .ctxloom: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString("entry\n"); err != nil {
		t.Fatalf("write to the opened log: %v", err)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read back %s: %v", logPath, err)
	}
	if string(got) != "entry\n" {
		t.Errorf("log contents = %q, want the entry that was written", got)
	}
}

// Opening an existing log must APPEND. Every ctxloom process opens this file
// independently — a statusline hook runs once per assistant message — so an
// open that truncated would leave the log holding only whatever the most
// recent process wrote, destroying the timeline the file exists to provide.
func TestOpen_AppendsRatherThanTruncating(t *testing.T) {
	_, logPath := homeAt(t)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("earlier\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Open(testProg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.WriteString("later\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "earlier\nlater\n" {
		t.Errorf("log contents = %q, want the earlier entry kept and the later one appended", got)
	}
}

// An oversized log is rolled aside, and the roll keeps the old bytes in .1
// rather than deleting them: the entries that filled the file are usually the
// ones worth reading.
func TestOpen_RollsAnOversizedLogAsideKeepingItsBytes(t *testing.T) {
	_, logPath := homeAt(t)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := append(bytes.Repeat([]byte("x"), MaxBytes), []byte("\nlast-old-entry\n")...)
	if err := os.WriteFile(logPath, old, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Open(testProg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.WriteString("fresh\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh\n" {
		t.Errorf("post-roll log = %q (%d bytes), want only the fresh entry", firstBytes(got), len(got))
	}
	rolled, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("rolled generation missing: %v", err)
	}
	if !strings.HasSuffix(string(rolled), "last-old-entry\n") {
		t.Error("the rolled generation does not carry the bytes that were in the log before the roll")
	}
}

// A log that has NOT reached the limit must be left alone. Rolling on every
// open would cap the log at one process's output and leave .1 as the only
// history, which reads as a working log while retaining almost nothing.
func TestOpen_LeavesAnUndersizedLogInPlace(t *testing.T) {
	_, logPath := homeAt(t)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("small\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Open(testProg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.Close()

	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Error("an undersized log was rolled; .1 exists")
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "small\n" {
		t.Errorf("log contents = %q, want the existing bytes untouched", got)
	}
}

func firstBytes(b []byte) string {
	if len(b) > 64 {
		return string(b[:64]) + "..."
	}
	return string(b)
}

// Lazy must not touch the filesystem until something is written. ltk runs as a
// pre-tool hook before EVERY shell command an agent issues, and almost every
// one of those runs logs nothing: an eager open would create ~/.ctxloom/logs
// and the file on each of them, doing a write on the hook path for no record.
// Sync is included because every main flushes on its way out — a Sync that
// opened the file would defeat the laziness on exactly the runs it is for.
func TestLazy_CreatesNothingWhenNothingIsWritten(t *testing.T) {
	home, _ := homeAt(t)

	ws := Lazy(testProg)
	if err := ws.Sync(); err != nil {
		t.Fatalf("Sync on a never-written sink: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".ctxloom")); !os.IsNotExist(err) {
		t.Errorf("a sink nothing was written to created ~/.ctxloom (stat err = %v)", err)
	}
}

// The first write creates the directory and the file; later writes append to
// the same file, and a log that already existed is appended to, not truncated.
func TestLazy_FirstWriteCreatesAndLaterWritesAppend(t *testing.T) {
	_, logPath := homeAt(t)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("earlier\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := Lazy(testProg)
	for _, line := range []string{"first\n", "second\n"} {
		if _, err := ws.Write([]byte(line)); err != nil {
			t.Fatalf("Write(%q): %v", line, err)
		}
	}
	if err := ws.Sync(); err != nil {
		t.Fatalf("Sync after writes: %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "earlier\nfirst\nsecond\n" {
		t.Errorf("log contents = %q, want the earlier entry kept and both writes appended", got)
	}
}

// A sink that cannot be opened fails its writes rather than pretending they
// landed, and says so ONCE on stderr. The stderr line is the only trace a lost
// log leaves — zap's own error channel is this same sink — so without it the
// degradation is indistinguishable from a process that had nothing to say;
// once, because a process that logs often must not repeat it per record.
// HOME pointing at a regular file makes ~/.ctxloom impossible to create.
func TestLazy_AnUnopenableLogFailsTheWriteAndSaysSoOnce(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", notADir)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stderr
	os.Stderr = w
	ws := Lazy(testProg)
	_, err1 := ws.Write([]byte("lost\n"))
	_, err2 := ws.Write([]byte("also lost\n"))
	os.Stderr = real
	w.Close()
	var stderr bytes.Buffer
	_, _ = stderr.ReadFrom(r)
	r.Close()

	if err1 == nil || err2 == nil {
		t.Errorf("a write to a log that could not be opened reported success (errs %v, %v)", err1, err2)
	}
	if n := strings.Count(stderr.String(), testProg+": warning:"); n != 1 {
		t.Errorf("stderr carried %d open-failure warnings, want exactly 1:\n%s", n, stderr.String())
	}
}
