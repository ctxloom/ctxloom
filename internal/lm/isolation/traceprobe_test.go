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
