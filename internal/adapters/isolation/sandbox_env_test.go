package isolation

import (
	"strings"
	"testing"
)

// TestRunnerSpec_EnvCannotLoosenSandbox: no environment variable can relax a
// shipped binary's container sandbox. The read-observation probe's strace wrap
// and its ptrace-permitting seccomp profile live in the acceptance harness,
// which rewrites the runtime CLI's argv from outside the binary; the
// production runner spec renders Docker's default seccomp profile, no trace
// mount and an unwrapped command whatever the environment says.
func TestRunnerSpec_EnvCannotLoosenSandbox(t *testing.T) {
	t.Setenv("CTXLOOM_ISOLATION_PROBE_TRACE_DIR", t.TempDir())
	for _, rt := range []Runtime{Docker{}, Docker{rootless: true}, Podman{}, Podman{rootless: true}} {
		spec := runnerSpecFor(rt, "mock", t.TempDir(), nil, nil)
		j := strings.Join(mustRunArgs(t, rt, spec), " ")
		for _, banned := range []string{"security-opt", "seccomp", "SYS_PTRACE", "strace", "ctxloom-probe-trace"} {
			if strings.Contains(j, banned) {
				t.Errorf("%s runner argv carries %q under the old probe env var; got %v", rt.Name(), banned, j)
			}
		}
	}
}
