package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Off a terminal gomutants is silent until the summary unless --verbose, so a
// logged run must be handed it; on a terminal its progress bar stays, and a
// verbosity the launcher already names is never contradicted.
func TestProgressArgs(t *testing.T) {
	for _, c := range []struct {
		launcher []string
		tty      bool
		want     []string
	}{
		{[]string{"gomutants"}, false, []string{"--verbose"}},
		{[]string{"gomutants"}, true, nil},
		{[]string{"bash", "w.sh", "tmp", "gomutants"}, false, []string{"--verbose"}},
		{[]string{"gomutants", "--quiet"}, false, nil},
		{[]string{"gomutants", "-q"}, false, nil},
		{[]string{"gomutants", "-v"}, false, nil},
		{[]string{"gomutants", "--verbose=false"}, false, nil},
		{[]string{"gomutants", "quiet"}, false, []string{"--verbose"}},
	} {
		if got := progressArgs(c.launcher, c.tty); !slices.Equal(got, c.want) {
			t.Errorf("progressArgs(%q, tty=%v) = %q, want %q", c.launcher, c.tty, got, c.want)
		}
	}
}

// isTerminal answers as gomutants does: a character device is a terminal, a
// file or a buffer is not.
func TestIsTerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	file, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if !isTerminal(devNull) || isTerminal(file) || isTerminal(&strings.Builder{}) {
		t.Errorf("isTerminal: char device %v, file %v, buffer %v; want true, false, false",
			isTerminal(devNull), isTerminal(file), isTerminal(&strings.Builder{}))
	}
}

// End to end: a run whose output is captured hands gomutants --verbose.
func TestCommands_RunOffTerminalIsVerbose(t *testing.T) {
	_, launcher, argv := cmdModule(t)
	t.Setenv("STATUS", "KILLED")
	if code, out := mutshard("run", "-scope", "tree", "-out", t.TempDir(), "--", launcher); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	if handed, _ := os.ReadFile(argv); !strings.Contains(string(handed), "--verbose\n") {
		t.Errorf("gomutants argv = %q, want --verbose", handed)
	}
}
