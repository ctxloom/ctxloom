package isolation

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	containerfiles "github.com/ctxloom/ctxloom/container"
)

// TraceProbe marks a RunSpec as a PROBE-ONLY container run: when it is non-nil
// renderRunSpec applies a PROBE-ONLY seccomp profile
// (`--security-opt seccomp=<SeccompProfile>`), bind-mounts HostDir out to
// ContainerDir, and wraps the in-container Command in
// `strace -f -e trace=<Syscalls> -o <ContainerDir>/<OutFile>` so the real
// vendor CLI's file READS (openat/stat/access/readlink) — including the ones it
// probes and does NOT find (ENOENT) — are captured. `docker diff`, overlay
// upperdir inspection and the host size/mtime/hash census are all WRITE-ONLY
// and can never see a read; strace can.
//
// WHY A CUSTOM SECCOMP PROFILE, NOT CAP_SYS_PTRACE: strace launches the engine
// as its OWN child, so the ptrace PERMISSION model needs no capability at all
// (parent-traces-own-child is always allowed; ptrace_scope is irrelevant for a
// direct child). The ONLY thing blocking strace in-container is Docker's
// default SECCOMP profile, which denies the ptrace family unless
// CAP_SYS_PTRACE is present. So the probe overrides seccomp with a TIGHT
// profile (containerfiles.ProbeSeccomp() — default policy + the ptrace family
// allowed) instead of granting the far broader capability. This is a strictly
// smaller privilege delta: it permits only same-uid self-child tracing, not the
// privileged cross-uid ptrace CAP_SYS_PTRACE confers.
//
// SECURITY — WHY THIS IS A RUNSPEC FIELD, NOT AN ENV LOOKUP IN renderRunSpec:
// the loosened seccomp profile must be UNREACHABLE from a normal `ctxloom run`.
// renderRunSpec is a PURE function of RunSpec.Trace: it applies the probe
// profile iff this field is non-nil, and NIL is the zero value every production
// spec builder (buildRunSpec, buildRunnerSpec, the docker-exec spec) produces.
// The ONLY code that sets it is traceProbeFromEnv, gated on a dedicated
// probe-only env var (probeTraceEnv) that production never sets. So the
// guarantee is structural: no non-nil Trace ⇒ Docker's DEFAULT seccomp profile
// and no capability grant, and the sole setter is the isolation probe. See
// TestRenderRunSpec_NoTraceProbe_NoSeccompOverride.
type TraceProbe struct {
	HostDir        string // host dir bind-mounted out so the trace survives --rm teardown
	ContainerDir   string // in-container mount target the trace file is written under
	OutFile        string // trace filename under ContainerDir
	Syscalls       string // strace `-e trace=` expression (comma-separated syscall names)
	SeccompProfile string // host path to the probe-only seccomp JSON (--security-opt seccomp=)
}

// ProbeTraceEnvVar is the exported name of the probe-only env var, so the
// out-of-package acceptance probe sets the SAME key traceProbeFromEnv reads.
// ProbeTraceOutFile is the basename the trace lands at under the probe's host
// dir (the same file straceWrapPrefix writes inside the container).
const (
	ProbeTraceEnvVar  = probeTraceEnv
	ProbeTraceOutFile = probeTraceOutFile
	// ProbeTraceContainerDir is the in-container mount target for the trace dir,
	// exported so the write-census assertion can exclude the probe's OWN
	// instrument from the "unexpected write" set — it shows up in `docker diff`
	// as a fresh writable-layer path, but it is the probe, not an engine leak.
	ProbeTraceContainerDir = probeTraceContainerDir
)

const (
	// probeTraceEnv is the dedicated, probe-only env var the isolation probe
	// sets to a host directory. Its presence (and nothing else) turns a
	// container run into a read-observing probe run. Production `ctxloom run`
	// never sets it, so traceProbeFromEnv returns nil and no RunSpec ever
	// carries a Trace — the structural gate that keeps the loosened seccomp
	// profile (and the strace wrap) probe-only.
	probeTraceEnv = "CTXLOOM_ISOLATION_PROBE_TRACE_DIR"
	// probeTraceContainerDir is the fixed in-container mount target for the
	// trace output — outside the engine's HOME so it is trivially distinct
	// from anything the engine writes.
	probeTraceContainerDir = "/ctxloom-probe-trace"
	// probeTraceOutFile is the strace output filename under the mounted dir.
	probeTraceOutFile = "reads.strace"
	// probeSeccompFile is the basename traceProbeFromEnv materializes the
	// embedded probe seccomp profile to, under the host trace dir.
	probeSeccompFile = "probe-seccomp.json"
	// probeTraceSyscalls is the file-name-taking syscall set traced. The stat
	// family is deliberately included alongside open/openat: a real CLI often
	// stat()s a path before opening it, and an ENOENT from stat/access is as
	// informative as one from openat — it is exactly what a silent no-op looks
	// like from outside.
	probeTraceSyscalls = "open,openat,stat,lstat,newfstatat,statx,access,faccessat,readlink,readlinkat"
)

// traceProbeFromEnv returns a *TraceProbe when the probe-only env var is set,
// else nil. It is the SOLE writer of RunSpec.Trace — see TraceProbe's security
// note. Called from the container spec builders (buildRunSpec / buildRunnerSpec);
// a normal run leaves the env unset and gets a nil Trace.
func traceProbeFromEnv() *TraceProbe {
	dir := os.Getenv(probeTraceEnv)
	if dir == "" {
		return nil
	}
	// Materialize the embedded probe-only seccomp profile to a host path the
	// docker CLI can read for `--security-opt seccomp=<path>`. It lands in the
	// probe's own trace dir, so the probe's teardown (os.RemoveAll) reaps it.
	// A write failure yields SeccompProfile="" and renderRunSpec then omits the
	// override — the run keeps Docker's default profile and strace simply fails
	// to trace (surfaced as an empty read-set finding), never a silent
	// isolation weakening.
	seccompPath := filepath.Join(dir, probeSeccompFile)
	if err := os.WriteFile(seccompPath, containerfiles.ProbeSeccomp(), 0o644); err != nil {
		seccompPath = ""
	}
	return &TraceProbe{
		HostDir:        dir,
		ContainerDir:   probeTraceContainerDir,
		OutFile:        probeTraceOutFile,
		Syscalls:       probeTraceSyscalls,
		SeccompProfile: seccompPath,
	}
}

// straceWrapPrefix renders the strace prefix that wraps the in-container engine
// exec: `strace -f -e trace=<syscalls> -o <ContainerDir>/<OutFile>`. `-f`
// follows forks so the trace covers ctxloom AND the vendor CLI it spawns; `-o`
// writes from INSIDE the container to the bind-mounted dir, so — unlike
// watchContainerDiff — there is no teardown race to poll against.
func straceWrapPrefix(tp *TraceProbe) []string {
	return []string{
		"strace", "-f",
		"-e", "trace=" + tp.Syscalls,
		"-o", tp.ContainerDir + "/" + tp.OutFile,
	}
}

// TraceRead is one deduplicated file-access observation parsed from an strace
// trace: the absolute path the CLI touched, the syscall it used, and the
// RESULT — "ok" for a success, or the errno name (ENOENT/EACCES/…) for a
// failure. ENOENT rows are the point of this whole mechanism (a path the CLI
// looked for and did not find), so they are first-class, never filtered noise.
type TraceRead struct {
	Path    string
	Syscall string
	// Result is "ok" on success, else the failure: the symbolic errno name
	// when strace named one (e.g. "ENOENT"), or "errno <ret>" for a negative
	// return it left unnamed. Never "ok" for a negative return — see
	// straceResult.
	Result string
}

// Failed reports whether this read did not succeed (an errno was returned) —
// the ENOENT/EACCES rows a write-only probe can never see.
func (r TraceRead) Failed() bool { return r.Result != "ok" }

// readSyscalls is the set of file-name-taking syscalls ParseStraceReads treats
// as reads — the same family probeTraceSyscalls traces. Filtering here (rather
// than trusting strace's own `-e trace=` filter) keeps the parser honest even
// if the trace was produced with a wider filter or a leading execve slips
// through: only genuine path lookups become TraceReads.
var readSyscalls = map[string]bool{
	"open": true, "openat": true, "stat": true, "lstat": true,
	"newfstatat": true, "statx": true, "access": true, "faccessat": true,
	"faccessat2": true, "readlink": true, "readlinkat": true,
}

// straceLineRe matches one COMPLETE strace syscall line, tolerating the `-f`
// pid prefix in either strace rendering (`[pid 12345] ` or a bare `12345 `):
//   - group 1: the syscall name
//   - group 2: the FIRST quoted string argument — the path for every syscall in
//     probeTraceSyscalls (openat(AT_FDCWD, "path", …), stat("path", …),
//     access("path", …), readlink("path", …), statx(AT_FDCWD, "path", …))
//   - group 3: the return value (may be negative)
//   - group 4: the errno name when the call failed (e.g. ENOENT), else empty
//
// `<unfinished ...>` / `<... resumed>` split lines (rare for these syscalls, and
// only under heavy threading) carry no `word(` + `= result` shape and are
// skipped — a documented, accepted limitation of a line-oriented parser.
var straceLineRe = regexp.MustCompile(`^(?:\[pid\s+\d+\]\s+|\d+\s+)?(\w+)\([^"]*"((?:[^"\\]|\\.)*)".*?\)\s*=\s*(-?\d+)(?:\s+([A-Z][A-Z0-9]+))?`)

// straceResult renders one traced syscall's outcome for TraceRead.Result: the
// symbolic errno name when strace named one, "ok" for a non-negative return,
// and "errno <ret>" for a negative return strace left unnamed — never "ok",
// which TraceRead.Failed reads as a success. A return value that does not parse
// as a number cannot be judged, so it keeps whichever verdict the errno group
// gave.
func straceResult(ret, errno string) string {
	if errno != "" {
		return errno
	}
	n, err := strconv.Atoi(ret)
	if err == nil && n < 0 {
		return "errno " + ret
	}
	return "ok"
}

// ParseStraceReads parses raw strace output (as written by straceWrapPrefix)
// into a sorted, deduplicated read-set. Deduplication is by (path, syscall,
// result), so the same file opened repeatedly collapses to one row while a path
// that both succeeds once and ENOENTs once keeps BOTH observations. Lines that
// are not a complete traced syscall (strace's own banner, signal lines,
// unfinished/resumed split lines) are skipped.
//
// The RETURN VALUE decides success, not merely the presence of a named errno:
// every syscall in readSyscalls returns >= 0 on success (a descriptor, a byte
// count, or 0), so a negative return is a failure whether or not strace could
// name its errno symbolically. An errno strace has no symbolic name for renders
// lowercase ("= -1 errno 4242 (Unknown error 4242)"), and a truncated or
// status-filtered trace can carry a bare "= -1"; treating either as a success
// would invert exactly the observation this probe exists to make.
func ParseStraceReads(raw []byte) []TraceRead {
	seen := map[string]TraceRead{}
	for _, line := range strings.Split(string(raw), "\n") {
		m := straceLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		syscall, path, errno := m[1], m[2], m[4]
		if !readSyscalls[syscall] {
			continue
		}
		result := straceResult(m[3], errno)
		key := path + "\x00" + syscall + "\x00" + result
		if _, ok := seen[key]; !ok {
			seen[key] = TraceRead{Path: path, Syscall: syscall, Result: result}
		}
	}
	out := make([]TraceRead, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Syscall != out[j].Syscall {
			return out[i].Syscall < out[j].Syscall
		}
		return out[i].Result < out[j].Result
	})
	return out
}
