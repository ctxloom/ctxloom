package isolation

import (
	"os"
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

	// A path the CLI OPENED and found — the read a write-only probe can never see.
	if !hasRead(reads, "/home/ctxloom/project/CLAUDE.md", "openat", "ok") {
		t.Errorf("expected a successful openat read of project CLAUDE.md; got %+v", reads)
	}
	// THE POINT: a path the CLI probed and did NOT find — an ENOENT, first-class.
	if !hasRead(reads, "/home/ctxloom/.claude/CLAUDE.md", "openat", "ENOENT") {
		t.Errorf("expected an ENOENT openat on ~/.claude/CLAUDE.md (the silent-no-op signal); got %+v", reads)
	}
	if !hasRead(reads, "/home/ctxloom/.claude/settings.json", "access", "ENOENT") {
		t.Errorf("expected an ENOENT access on ~/.claude/settings.json; got %+v", reads)
	}
	if !hasRead(reads, "/home/ctxloom/.claude/settings.json", "stat", "ok") {
		t.Errorf("expected a successful stat of ~/.claude/settings.json; got %+v", reads)
	}
	if !hasRead(reads, "/home/ctxloom/project/.claude/settings.local.json", "newfstatat", "ENOENT") {
		t.Errorf("expected an ENOENT newfstatat on project .claude/settings.local.json; got %+v", reads)
	}
	if !hasRead(reads, "/proc/self/exe", "readlink", "ok") {
		t.Errorf("expected the readlink of /proc/self/exe; got %+v", reads)
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

// TestRenderRunSpec_TraceProbe: a spec carrying a Trace MUST apply the
// probe-only seccomp profile (NOT a capability), the trace-dir mount, and the
// strace command wrap.
func TestRenderRunSpec_TraceProbe(t *testing.T) {
	tp := &TraceProbe{
		HostDir:        "/host/trace",
		ContainerDir:   "/ctxloom-probe-trace",
		OutFile:        "reads.strace",
		Syscalls:       "openat,stat",
		SeccompProfile: "/host/trace/probe-seccomp.json",
	}
	spec := RunSpec{
		Image:   "img",
		Name:    "c1",
		WorkDir: "/w",
		Home:    "/home/ctxloom",
		Command: []string{"ctxloom", "llm", "host", "claude-code"},
		Trace:   tp,
	}
	args := renderRunSpec(spec)
	joined := strings.Join(args, " ")

	// The mechanism is a seccomp override, never CAP_SYS_PTRACE.
	if strings.Contains(joined, "SYS_PTRACE") || strings.Contains(joined, "cap-add") {
		t.Errorf("Trace spec must NOT grant a capability; got %v", args)
	}
	if !strings.Contains(joined, "--security-opt seccomp=/host/trace/probe-seccomp.json") {
		t.Errorf("Trace spec must apply the probe seccomp profile; got %v", args)
	}
	if !strings.Contains(joined, "type=bind,source=/host/trace,target=/ctxloom-probe-trace") {
		t.Errorf("Trace spec must bind-mount the trace dir out; got %v", args)
	}
	// The engine exec must be wrapped in strace, before the original command.
	straceIdx, cmdIdx := argIndex(args, "strace"), argIndex(args, "ctxloom")
	if straceIdx < 0 || cmdIdx < 0 || straceIdx > cmdIdx {
		t.Errorf("engine exec must be wrapped in strace before the command; got %v", args)
	}
	if !strings.Contains(joined, "-o /ctxloom-probe-trace/reads.strace") {
		t.Errorf("strace must write -o into the mounted trace dir; got %v", args)
	}
	// --security-opt must sit before the image (a docker `run` flag).
	if imgIdx := argIndex(args, "img"); imgIdx < 0 || argIndex(args, "--security-opt") > imgIdx {
		t.Errorf("--security-opt must precede the image in the argv; got %v", args)
	}
}

// TestRenderRunSpec_NoTraceProbe_NoSeccompOverride is the SECURITY GUARD: a
// normal (production) spec — nil Trace — must use Docker's DEFAULT seccomp
// profile (NO --security-opt override), must carry NO SYS_PTRACE capability,
// must not mount a trace dir, and must not strace-wrap its command. This is the
// structural proof that the loosened profile is unreachable without a Trace.
func TestRenderRunSpec_NoTraceProbe_NoSeccompOverride(t *testing.T) {
	spec := RunSpec{
		Image:   "img",
		Name:    "c1",
		Command: []string{"ctxloom", "llm", "host", "claude-code"},
		// Trace deliberately nil — the production zero value.
	}
	args := renderRunSpec(spec)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "SYS_PTRACE") || strings.Contains(joined, "cap-add") {
		t.Fatalf("a production run (nil Trace) MUST NOT get a ptrace capability; got %v", args)
	}
	if strings.Contains(joined, "security-opt") || strings.Contains(joined, "seccomp") {
		t.Fatalf("a production run (nil Trace) MUST keep Docker's DEFAULT seccomp profile (no override); got %v", args)
	}
	if strings.Contains(joined, "strace") || strings.Contains(joined, "ctxloom-probe-trace") {
		t.Fatalf("a production run (nil Trace) MUST NOT be strace-wrapped/mounted; got %v", args)
	}
}

// TestRunArgs_NoTraceProbe_NoSeccompOverride proves the guard at the RunArgs
// choke point (not just renderRunSpec): the full Docker/Podman argv for a
// nil-Trace spec carries neither a capability grant nor a seccomp override.
func TestRunArgs_NoTraceProbe_NoSeccompOverride(t *testing.T) {
	spec := RunSpec{Image: "img", Name: "c1", Command: []string{"ctxloom"}}
	for _, rt := range []Runtime{Docker{}, Docker{rootless: true}, Podman{}, Podman{rootless: true}} {
		j := strings.Join(rt.RunArgs(spec), " ")
		if strings.Contains(j, "SYS_PTRACE") || strings.Contains(j, "security-opt") || strings.Contains(j, "seccomp") {
			t.Errorf("%s.RunArgs(nil-Trace) must not grant ptrace nor override seccomp; got %v", rt.Name(), rt.RunArgs(spec))
		}
	}
}

// TestRunArgs_TraceProbe_AppliesSeccomp proves the positive path through the
// real RunArgs choke point.
func TestRunArgs_TraceProbe_AppliesSeccomp(t *testing.T) {
	spec := RunSpec{
		Image:   "img",
		Name:    "c1",
		Command: []string{"ctxloom"},
		Trace:   &TraceProbe{HostDir: "/h", ContainerDir: "/ctxloom-probe-trace", OutFile: "reads.strace", Syscalls: "openat", SeccompProfile: "/h/probe-seccomp.json"},
	}
	j := strings.Join(Docker{}.RunArgs(spec), " ")
	if !strings.Contains(j, "--security-opt seccomp=/h/probe-seccomp.json") {
		t.Error("Docker.RunArgs(Trace) must apply the probe seccomp profile")
	}
	if strings.Contains(j, "SYS_PTRACE") {
		t.Error("Docker.RunArgs(Trace) must NOT grant CAP_SYS_PTRACE")
	}
}

func TestTraceProbeFromEnv(t *testing.T) {
	t.Setenv(probeTraceEnv, "")
	if tp := traceProbeFromEnv(); tp != nil {
		t.Errorf("unset probe env must yield nil Trace, got %+v", tp)
	}
	dir := t.TempDir()
	t.Setenv(probeTraceEnv, dir)
	tp := traceProbeFromEnv()
	if tp == nil || tp.HostDir != dir || tp.ContainerDir != probeTraceContainerDir || tp.OutFile != probeTraceOutFile {
		t.Fatalf("set probe env must yield a populated Trace, got %+v", tp)
	}
	// The seccomp profile must be materialized to a readable host file that is a
	// tight profile (errno default) permitting ptrace.
	if tp.SeccompProfile == "" {
		t.Fatal("probe Trace must carry a materialized seccomp profile path")
	}
	data, err := os.ReadFile(tp.SeccompProfile)
	if err != nil {
		t.Fatalf("seccomp profile file must exist: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "SCMP_ACT_ERRNO") || !strings.Contains(s, "ptrace") {
		t.Errorf("seccomp profile must be a tight (errno-default) profile allowing ptrace; got %d bytes", len(data))
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

func argIndex(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
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

// TestParseStraceReads_NegativeReturnIsNeverOK pins U064-F04: the parser
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

// TestTraceProbeFromEnv_ActivationIsNeverSilent pins U064-F11's corrected
// invariant. The row read TraceProbe's security note as claiming the loosened
// seccomp profile is STRUCTURALLY unreachable from a normal run, and objected
// that the gate is a plain os.Getenv — an environment variable is inherited
// from whatever launched ctxloom, so any parent that exports it turns an
// ordinary `ctxloom run` into a probe run with a loosened profile and an strace
// wrap. That is correct about the gate; what is structural is only that
// renderRunSpec applies the profile from RunSpec.Trace alone and that this
// function is its sole setter. The residue — an inherited variable silently
// changing a run's isolation posture — is closed by making activation LOUD, and
// this pins it: the probe announces itself and names the variable to unset.
func TestTraceProbeFromEnv_ActivationIsNeverSilent(t *testing.T) {
	t.Setenv(probeTraceEnv, t.TempDir())
	buf := captureWarnings(t)

	if tp := traceProbeFromEnv(); tp == nil {
		t.Fatal("the probe env var must still activate the probe")
	}
	out := buf.String()
	if !strings.Contains(out, probeTraceEnv) {
		t.Errorf("the activation warning must name the variable to unset; got %q", out)
	}
	if !strings.Contains(out, "seccomp") {
		t.Errorf("the activation warning must name the isolation property being loosened; got %q", out)
	}
}

// TestTraceProbeFromEnv_UnsetIsSilentAndNil: the ordinary run says nothing and
// gets no Trace — the warning must not become ambient noise.
func TestTraceProbeFromEnv_UnsetIsSilentAndNil(t *testing.T) {
	t.Setenv(probeTraceEnv, "")
	buf := captureWarnings(t)

	if tp := traceProbeFromEnv(); tp != nil {
		t.Fatalf("an unset probe var must yield a nil Trace; got %+v", tp)
	}
	if buf.Len() != 0 {
		t.Errorf("an ordinary run must be silent; got %q", buf.String())
	}
}
