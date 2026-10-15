// No build tag, deliberately: mutation_tmp.sh is the one seam every mutation
// recipe releases its tool through, and a guard that only runs during the
// hour-long run it guards is not a guard.
package mutation

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An agent cell exports GOTMPDIR into its own session directory: deep, and
// inside the real app dir. A mutation tool's coverage run inherits whatever
// GOTMPDIR it is handed, and from there the suite's fixtures overflow the
// file-name limit and its sandbox assertions resolve into the real app dir.
// The wrapper must therefore REPLACE an inherited GOTMPDIR with the run's own
// scratch dir, which it already owns and removes.
func TestMutationTmp_ReplacesInheritedGOTMPDIRWithTheRunDir(t *testing.T) {
	script := repoInput(t, "tests/mutation/mutation_tmp.sh")[0]
	base := filepath.Join(t.TempDir(), "mutation")
	inherited := filepath.Join(t.TempDir(), "cell", "session", "deep", "gotmp")

	cmd := exec.Command("bash", script, base, "bash", "-c",
		`printf '%s\n%s\n' "$TMPDIR" "$GOTMPDIR"`)
	cmd.Env = append(os.Environ(), "GOTMPDIR="+inherited)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mutation_tmp.sh: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want TMPDIR and GOTMPDIR lines, got %q", out)
	}
	tmpdir, gotmpdir := lines[0], lines[1]
	if gotmpdir == inherited {
		t.Fatalf("GOTMPDIR inherited from the caller (%s); the tool must not see it", inherited)
	}
	if gotmpdir != tmpdir {
		t.Fatalf("GOTMPDIR = %q, want the run's own dir %q", gotmpdir, tmpdir)
	}
	if filepath.Dir(gotmpdir) != base {
		t.Fatalf("GOTMPDIR = %q, want a run dir directly under %q", gotmpdir, base)
	}
	if _, err := os.Stat(gotmpdir); !os.IsNotExist(err) {
		t.Fatalf("run dir %q survived the run (stat err %v)", gotmpdir, err)
	}
}

// A mutant that flips a directory mode can leave a directory its owner may
// write and traverse but not read (0360). rm -rf cannot list it, so the run dir
// survives and the wrapper turns a finished mutation run red at cleanup. The
// cleanup must restore the owner's read and search bits, not only write.
func TestMutationTmp_RemovesARunDirAMutantLeftUnreadable(t *testing.T) {
	script := repoInput(t, "tests/mutation/mutation_tmp.sh")[0]
	base := filepath.Join(t.TempDir(), "mutation")

	cmd := exec.Command("bash", script, base, "bash", "-c",
		`mkdir -p "$TMPDIR/home/.ctxloom/sub" && touch "$TMPDIR/home/.ctxloom/sub/f" && chmod 0360 "$TMPDIR/home/.ctxloom"`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mutation_tmp.sh exited non-zero after an unreadable dir was left: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
	}
	if len(entries) != 0 {
		t.Fatalf("run dir survived cleanup: %v", entries)
	}
}

// A MUTANT MUST NEVER REACH THE HOST'S RUNTIME DIR. Tests sweep
// XDG_RUNTIME_DIR, and a mutant of the sweep's filter run against the real one
// deleted the rootless docker daemon's socket and state. The wrapper hands the
// run a fresh runtime dir inside its own scratch (removed with it), and pins
// DOCKER_HOST to the caller's daemon socket before hiding the caller's dir, so
// docker-using tests still reach the daemon without the dir being in reach.
func TestMutationTmp_HidesTheHostRuntimeDirAndPinsDockerHost(t *testing.T) {
	script := repoInput(t, "tests/mutation/mutation_tmp.sh")[0]
	base := filepath.Join(t.TempDir(), "mutation")
	host := t.TempDir()
	sock := filepath.Join(host, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	defer func() { _ = l.Close() }()

	run := func(env ...string) []string {
		t.Helper()
		cmd := exec.Command("bash", script, base, "bash", "-c",
			`printf '%s\n%s\n%s\n' "$TMPDIR" "$XDG_RUNTIME_DIR" "${DOCKER_HOST:-}"; test -d "$XDG_RUNTIME_DIR"`)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mutation_tmp.sh: %v\n%s", err, out)
		}
		return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	}

	got := run("XDG_RUNTIME_DIR="+host, "DOCKER_HOST=")
	if len(got) != 3 {
		t.Fatalf("want TMPDIR, XDG_RUNTIME_DIR and DOCKER_HOST lines, got %q", got)
	}
	if got[1] == host || !strings.HasPrefix(got[1], got[0]+string(os.PathSeparator)) {
		t.Errorf("XDG_RUNTIME_DIR = %q, want a fresh dir inside the run dir %q, never the host's %q", got[1], got[0], host)
	}
	if got[2] != "unix://"+sock {
		t.Errorf("DOCKER_HOST = %q, want the caller's daemon socket unix://%s", got[2], sock)
	}

	// A DOCKER_HOST the caller set is the caller's choice and stands.
	if got := run("XDG_RUNTIME_DIR="+host, "DOCKER_HOST=tcp://elsewhere:2375"); len(got) != 3 || got[2] != "tcp://elsewhere:2375" {
		t.Errorf("a caller's DOCKER_HOST was replaced: %q", got)
	}
}
