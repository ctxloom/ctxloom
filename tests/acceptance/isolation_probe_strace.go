//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// READ OBSERVATION lives here, in the harness, and nowhere in the shipped
// binary: no environment variable can relax a production container's sandbox
// (TestRunnerSpec_EnvCannotLoosenSandbox pins that). The harness instead puts a
// runtime-CLI shim ahead of PATH for the ctxloom it launches. For a runner
// container's `run` (recognised by the ctxloom.harp label every runner spec
// carries) the shim adds, before the image:
//
//   - `--security-opt seccomp=<profile>`: Docker's default policy plus the
//     ptrace family, testdata/probe-seccomp.json. NOT CAP_SYS_PTRACE and not
//     `unconfined`: strace parents its tracee, so the ptrace permission model
//     needs no capability, and the default seccomp filter is the only thing in
//     the way.
//   - a bind of the host trace dir to probeTraceContainerDir, so the trace
//     survives the container's --rm teardown with no race.
//   - `--entrypoint` the probe entry script, which runs the image's own
//     entrypoint with `strace ... "$@"` as its command. The identity remap and
//     privilege drop therefore happen first and strace becomes the engine's
//     parent, as if the image had been built with the wrap.
//
// Every other invocation (info, ps, image inspect, build, diff, rm, and `run`s
// without the label) goes to the real binary untouched.
const (
	// probeTraceContainerDir is the in-container mount target for the trace
	// dir, outside the engine's HOME so it is distinct from anything the
	// engine writes.
	probeTraceContainerDir = "/ctxloom-probe-trace"
	// probeTraceOutFile is the strace output filename under the trace dir.
	probeTraceOutFile = "reads.strace"
	// probeImageEntrypoint is the entrypoint every ctxloom-built agent image
	// bakes (isolation's generated Containerfile); the entry script hands off
	// to it so the run keeps the image's identity contract.
	probeImageEntrypoint = "/usr/local/bin/ctxloom-entrypoint"
	// probeRunnerLabel is the label key on every runner container's `run`
	// argv (isolation's labelHarp); the shim wraps only those runs.
	probeRunnerLabel = "ctxloom.harp"
	// probeTraceSyscalls is the file-name-taking syscall set traced. The stat
	// family is included alongside open/openat: a CLI often stat()s a path
	// before opening it, and an ENOENT from stat/access is as informative as
	// one from openat.
	probeTraceSyscalls = "open,openat,stat,lstat,newfstatat,statx,access,faccessat,readlink,readlinkat"
)

// probeEntryScript runs the image's entrypoint with the strace-wrapped
// command. `-f` follows forks so the trace covers ctxloom and the vendor CLI
// it spawns; `-o` writes into the bind-mounted trace dir.
func probeEntryScript() string {
	return fmt.Sprintf("#!/bin/sh\nexec %s strace -f -e trace=%s -o %s/%s \"$@\"\n",
		probeImageEntrypoint, probeTraceSyscalls, probeTraceContainerDir, probeTraceOutFile)
}

// probeRuntimeShim is the runtime-CLI shim script: wrap a labelled runner
// `run`, forward everything else verbatim to realBin.
func probeRuntimeShim(realBin, traceDir string) string {
	return fmt.Sprintf(`#!/bin/sh
if [ "$1" = run ]; then
  for a in "$@"; do
    case "$a" in
      %[3]s=*)
        shift
        exec %[1]q run --security-opt seccomp=%[2]q/probe-seccomp.json --mount type=bind,source=%[2]q,target=%[4]s --entrypoint %[4]s/probe-entry.sh "$@" ;;
    esac
  done
fi
exec %[1]q "$@"
`, realBin, traceDir, probeRunnerLabel, probeTraceContainerDir)
}

// installProbeTrace prepares traceDir for a read-observing run under
// runtimeBin ("docker" | "podman") and returns the directory to put first on
// the launched ctxloom's PATH. The seccomp profile and entry script land in
// traceDir (mounted into the container); the shim lands in a subdirectory of
// it, so the caller's RemoveAll of traceDir reaps everything.
func installProbeTrace(traceDir, runtimeBin string) (string, error) {
	realBin, err := exec.LookPath(runtimeBin)
	if err != nil {
		return "", fmt.Errorf("probe trace: resolve %s: %w", runtimeBin, err)
	}
	profile, err := os.ReadFile(filepath.Join("testdata", "probe-seccomp.json"))
	if err != nil {
		return "", fmt.Errorf("probe trace: seccomp profile: %w", err)
	}
	shimDir := filepath.Join(traceDir, "shim-bin")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return "", fmt.Errorf("probe trace: shim dir: %w", err)
	}
	files := []struct {
		path string
		body []byte
		mode os.FileMode
	}{
		{filepath.Join(traceDir, "probe-seccomp.json"), profile, 0o644},
		{filepath.Join(traceDir, "probe-entry.sh"), []byte(probeEntryScript()), 0o755},
		{filepath.Join(shimDir, runtimeBin), []byte(probeRuntimeShim(realBin, traceDir)), 0o755},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.body, f.mode); err != nil {
			return "", fmt.Errorf("probe trace: write %s: %w", f.path, err)
		}
	}
	return shimDir, nil
}

// prependPath returns env with dir put first on its effective PATH (the LAST
// PATH entry, which is the one exec honours).
func prependPath(env []string, dir string) []string {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	return append(env, "PATH="+dir+string(os.PathListSeparator)+path)
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

// ParseStraceReads parses raw strace output (as written by the probe entry script, probeEntryScript)
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
