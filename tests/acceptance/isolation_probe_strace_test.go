//go:build acceptance

package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// capturedStrace is a real fragment of `strace -f -e trace=... -o file` output
// (pid-prefixed, mixed success and ENOENT) — the shape the in-container wrap
// writes. It exercises: the openat success path, an ENOENT on a CLAUDE.md the
// CLI probed and did not find (the whole point of read-observation), an access
// ENOENT, a stat success, a readlink, a duplicate openat that must collapse,
// and a noise line (a signal) that must be ignored.
const capturedStrace = `1 execve("/usr/local/bin/ctxloom", ["ctxloom"], 0x7ffd) = 0
12 openat(AT_FDCWD, "/home/ctxloom/project/CLAUDE.md", O_RDONLY|O_CLOEXEC) = 3
12 openat(AT_FDCWD, "/home/ctxloom/project/CLAUDE.md", O_RDONLY|O_CLOEXEC) = 3
[pid    13] openat(AT_FDCWD, "/home/ctxloom/.claude/CLAUDE.md", O_RDONLY) = -1 ENOENT (No such file or directory)
13 access("/home/ctxloom/.claude/settings.json", R_OK) = -1 ENOENT (No such file or directory)
13 stat("/home/ctxloom/.claude/settings.json", 0x7ffe) = 0
13 newfstatat(AT_FDCWD, "/home/ctxloom/project/.claude/settings.local.json", 0x7ffe, 0) = -1 ENOENT (No such file or directory)
13 readlink("/proc/self/exe", "/usr/local/bin/ctxloom", 4096) = 22
13 --- SIGCHLD {si_signo=SIGCHLD, si_code=CLD_EXITED} ---
13 openat(AT_FDCWD, "/home/ctxloom/.claude.json", O_RDONLY) = 5
`

func TestParseStraceReads(t *testing.T) {
	reads := ParseStraceReads([]byte(capturedStrace))

	for _, want := range []struct{ path, syscall, result, why string }{
		// A path the CLI OPENED and found — the read a write-only probe can never see.
		{"/home/ctxloom/project/CLAUDE.md", "openat", "ok", "a successful openat read of project CLAUDE.md"},
		// THE POINT: a path the CLI probed and did NOT find — an ENOENT, first-class.
		{"/home/ctxloom/.claude/CLAUDE.md", "openat", "ENOENT", "an ENOENT openat on ~/.claude/CLAUDE.md (the silent-no-op signal)"},
		{"/home/ctxloom/.claude/settings.json", "access", "ENOENT", "an ENOENT access on ~/.claude/settings.json"},
		{"/home/ctxloom/.claude/settings.json", "stat", "ok", "a successful stat of ~/.claude/settings.json"},
		{"/home/ctxloom/project/.claude/settings.local.json", "newfstatat", "ENOENT", "an ENOENT newfstatat on project .claude/settings.local.json"},
		{"/proc/self/exe", "readlink", "ok", "the readlink of /proc/self/exe"},
	} {
		if !hasRead(reads, want.path, want.syscall, want.result) {
			t.Errorf("expected %s; got %+v", want.why, reads)
		}
	}

	// The duplicate openat of project/CLAUDE.md must collapse to exactly one row.
	if n := countRead(reads, "/home/ctxloom/project/CLAUDE.md", "openat", "ok"); n != 1 {
		t.Errorf("duplicate reads must dedupe to 1 row, got %d", n)
	}
	// The signal line and the execve arg vector must never parse as a read.
	for _, r := range reads {
		if r.Syscall == "execve" || strings.Contains(r.Syscall, "SIG") {
			t.Errorf("noise line parsed as a read: %+v", r)
		}
	}
}

func TestParseStraceReads_FailedFlag(t *testing.T) {
	reads := ParseStraceReads([]byte(capturedStrace))
	var enoent, ok int
	for _, r := range reads {
		if r.Failed() {
			enoent++
		} else {
			ok++
		}
	}
	if enoent == 0 || ok == 0 {
		t.Fatalf("expected both failed (ENOENT) and ok reads; got failed=%d ok=%d (%+v)", enoent, ok, reads)
	}
}

func hasRead(reads []TraceRead, path, syscall, result string) bool {
	return countRead(reads, path, syscall, result) > 0
}

func countRead(reads []TraceRead, path, syscall, result string) int {
	n := 0
	for _, r := range reads {
		if r.Path == path && r.Syscall == syscall && r.Result == result {
			n++
		}
	}
	return n
}

// unnamedErrnoStrace carries the two real strace renderings of a FAILED
// syscall whose errno the parser's `([A-Z][A-Z0-9]+)` name group cannot
// match: an errno strace has no symbolic name for (rendered lowercase, `errno
// 4242`), and a bare negative return with no errno clause at all (what a
// truncated or `-e status`-filtered trace produces). Both are failures — the
// return value is negative, and every syscall in the traced set returns >= 0
// on success.
const unnamedErrnoStrace = `12 openat(AT_FDCWD, "/home/ctxloom/.config/odd", O_RDONLY) = -1 errno 4242 (Unknown error 4242)
13 access("/home/ctxloom/.config/bare", R_OK) = -1
14 readlink("/proc/self/exe", "/usr/local/bin/ctxloom", 4096) = 22
`

// TestParseStraceReads_NegativeReturnIsNeverOK pins that the parser
// captured the syscall's return value and then ignored it, deciding "ok"
// purely on whether a NAMED errno was present. A failure strace did not name
// symbolically therefore read back as a success — the exact inversion this
// probe exists to catch, since a silent no-op looks like a failed lookup from
// outside.
func TestParseStraceReads_NegativeReturnIsNeverOK(t *testing.T) {
	reads := ParseStraceReads([]byte(unnamedErrnoStrace))

	for _, tc := range []struct{ path, syscall string }{
		{"/home/ctxloom/.config/odd", "openat"},
		{"/home/ctxloom/.config/bare", "access"},
	} {
		if hasRead(reads, tc.path, tc.syscall, "ok") {
			t.Errorf("%s(%s) returned -1 and must never be recorded as a success; got %+v", tc.syscall, tc.path, reads)
		}
		found := false
		for _, r := range reads {
			if r.Path == tc.path && r.Syscall == tc.syscall {
				found = true
				if !r.Failed() {
					t.Errorf("%s(%s) returned -1 and must read as Failed; got %+v", tc.syscall, tc.path, r)
				}
			}
		}
		if !found {
			t.Errorf("%s(%s) must still be recorded (a failed lookup is the point of the probe); got %+v", tc.syscall, tc.path, reads)
		}
	}

	// A non-negative return stays a success: readlink returns a byte count.
	if !hasRead(reads, "/proc/self/exe", "readlink", "ok") {
		t.Errorf("a non-negative return is still a success; got %+v", reads)
	}
}

// TestProbeSeccompProfile_TightDefault: the probe profile is Docker's default
// policy plus the ptrace family, never `unconfined`.
func TestProbeSeccompProfile_TightDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "probe-seccomp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("the probe seccomp profile must be valid JSON: %v", err)
	}
	if profile.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Errorf("defaultAction = %q, want SCMP_ACT_ERRNO (a tight default, never unconfined)", profile.DefaultAction)
	}
	allowsPtrace := false
	for _, sc := range profile.Syscalls {
		for _, n := range sc.Names {
			if n == "ptrace" && sc.Action == "SCMP_ACT_ALLOW" {
				allowsPtrace = true
			}
		}
	}
	if !allowsPtrace {
		t.Error("the probe profile must allow ptrace, or strace cannot trace in-container")
	}
}

// probeShimRun installs the probe trace for a fake runtime that records its
// argv, runs the shim with args, and returns what the "real" runtime saw.
func probeShimRun(t *testing.T, args ...string) (traceDir string, got []string) {
	t.Helper()
	fakeDir := t.TempDir()
	record := filepath.Join(fakeDir, "argv")
	fake := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + record + "\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	traceDir = t.TempDir()
	shimDir, err := installProbeTrace(traceDir, "docker")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(filepath.Join(shimDir, "docker"), args...).CombinedOutput(); err != nil {
		t.Fatalf("shim: %v: %s", err, out)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return traceDir, strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// TestProbeShim_WrapsLabelledRunnerRun: a runner `run` gets the probe profile,
// the trace bind and the strace entry script, all before the image, and keeps
// every original argument in order.
func TestProbeShim_WrapsLabelledRunnerRun(t *testing.T) {
	orig := []string{"--rm", "--name", "c1", "--label", probeRunnerLabel + "=sweet-jumpy-tiger", "img", "ctxloom", "runner", "claude-code"}
	traceDir, got := probeShimRun(t, append([]string{"run"}, orig...)...)
	want := append([]string{"run",
		"--security-opt", "seccomp=" + traceDir + "/probe-seccomp.json",
		"--mount", "type=bind,source=" + traceDir + ",target=" + probeTraceContainerDir,
		"--entrypoint", probeTraceContainerDir + "/probe-entry.sh",
	}, orig...)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("wrapped argv\n got %q\nwant %q", got, want)
	}
	entry, err := os.ReadFile(filepath.Join(traceDir, "probe-entry.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "exec "+probeImageEntrypoint+" strace -f ") ||
		!strings.Contains(string(entry), "-o "+probeTraceContainerDir+"/"+probeTraceOutFile+` "$@"`) {
		t.Errorf("entry script must hand the strace-wrapped command to the image entrypoint; got %q", entry)
	}
}

// TestProbeShim_ForwardsEverythingElse: an unlabelled `run` and any other
// subcommand reach the real runtime verbatim.
func TestProbeShim_ForwardsEverythingElse(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--rm", "img", "cat", "/probe/marker"},
		{"info", "--format", "{{.SecurityOptions}}"},
		{"diff", "c1"},
	} {
		_, got := probeShimRun(t, args...)
		if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
			t.Errorf("argv %q forwarded as %q", args, got)
		}
	}
}

// TestPrependPath_LastPathWins: the shim dir goes first on the PATH exec will
// actually use, which is the last PATH entry in the environment.
func TestPrependPath_LastPathWins(t *testing.T) {
	env := prependPath([]string{"PATH=/a", "HOME=/h", "PATH=/b"}, "/shim")
	if last := env[len(env)-1]; last != "PATH=/shim"+string(os.PathListSeparator)+"/b" {
		t.Errorf("last entry = %q", last)
	}
}
