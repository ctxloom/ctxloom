package isolation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDaemonInfo swaps the probeExec seam with a stub that ignores the marker
// path (daemonName issues an `info` call, not a `run`) and restores it plus the
// probe memo afterwards — so the diagnose path is exercised without a runtime.
func stubDaemonInfo(t *testing.T, fn func(ctx context.Context, args []string) (string, error)) {
	t.Helper()
	orig := probeExec
	probeExec = func(ctx context.Context, _ string, args []string) (string, error) {
		return fn(ctx, args)
	}
	t.Cleanup(func() {
		probeExec = orig
		sharedFSMu.Lock()
		sharedFSResults = map[string]error{}
		sharedFSMu.Unlock()
	})
}

// TestDaemonName_SeamTimeoutAndPortableTemplate: daemonName routes
// through the probeExec seam (so the advisory is testable without a runtime),
// bounds itself with its own deadline, and picks a template that EXISTS on both
// docker (top-level {{.Name}}) and podman ({{.Host.Hostname}}) — so podman no
// longer fails the template and silently breaks the advisory.
func TestDaemonName_SeamTimeoutAndPortableTemplate(t *testing.T) {
	tests := []struct {
		runtimeName  string
		wantTemplate string
	}{
		{"docker", "{{.Name}}"},
		{"podman", "{{.Host.Hostname}}"},
	}
	for _, tt := range tests {
		t.Run(tt.runtimeName, func(t *testing.T) {
			var gotArgs []string
			hasDeadline := false
			stubDaemonInfo(t, func(ctx context.Context, args []string) (string, error) {
				gotArgs = args
				_, hasDeadline = ctx.Deadline()
				return "daemon-host\n", nil
			})
			name, err := daemonName(context.Background(), fakeRuntime{name: tt.runtimeName, binary: tt.runtimeName})
			require.NoError(t, err)
			assert.Equal(t, "daemon-host", name)
			assert.Equal(t, []string{"info", "--format", tt.wantTemplate}, gotArgs, "portable per-runtime template")
			assert.True(t, hasDeadline, "daemonName imposes its own timeout even on a deadline-less ctx")
		})
	}
}

// TestDaemonName_Error: a runtime error (unreachable daemon, or a template the
// runtime rejects) is surfaced, not swallowed.
func TestDaemonName_Error(t *testing.T) {
	stubDaemonInfo(t, func(context.Context, []string) (string, error) {
		return "", errors.New("cannot connect to the daemon")
	})
	_, err := daemonName(context.Background(), fakeRuntime{name: "docker", binary: "docker"})
	require.Error(t, err)
}

// TestDiagnoseAdvisory: the no-image heuristic reads the daemon name through the
// same seam — matching hostname reads as likely-shared, a differing name inside
// a container as possibly-the-host's-daemon (with guidance), an info error as
// unprobed.
func TestDiagnoseAdvisory(t *testing.T) {
	host, herr := os.Hostname()
	require.NoError(t, herr)

	t.Run("name matches hostname → likely shared", func(t *testing.T) {
		stubDaemonInfo(t, func(context.Context, []string) (string, error) { return host, nil })
		d := &Diagnosis{}
		diagnoseAdvisory(context.Background(), fakeRuntime{name: "docker", binary: "docker"}, d)
		assert.Contains(t, d.SharedFS, "likely shared")
	})

	t.Run("differing name in a container → possibly the host's daemon", func(t *testing.T) {
		stubDaemonInfo(t, func(context.Context, []string) (string, error) { return "some-other-daemon", nil })
		d := &Diagnosis{InContainer: true}
		diagnoseAdvisory(context.Background(), fakeRuntime{name: "docker", binary: "docker"}, d)
		assert.Contains(t, d.SharedFS, "possibly the host's daemon")
		assert.NotEmpty(t, d.Guidance, "an in-container host-daemon mismatch gets docker-outside-of-docker guidance")
	})

	t.Run("info error → unprobed", func(t *testing.T) {
		stubDaemonInfo(t, func(context.Context, []string) (string, error) { return "", errors.New("boom") })
		d := &Diagnosis{}
		diagnoseAdvisory(context.Background(), fakeRuntime{name: "docker", binary: "docker"}, d)
		assert.Contains(t, d.SharedFS, "no local image to probe with")
	})
}

// TestDiagnoseProbe_DistinguishesMismatchFromRunFailure: a probe
// RUN failure (daemon down, image unreadable) is reported as unprobed with its
// real cause — NOT as a filesystem-sharing mismatch (the wrong fix-it hint).
func TestDiagnoseProbe_DistinguishesMismatchFromRunFailure(t *testing.T) {
	rt := probeRuntime{fakeRuntime{name: "docker", binary: "docker", available: true}}

	t.Run("run failure → unprobed, not mismatch", func(t *testing.T) {
		stubProbeExec(t, func(string) (string, error) {
			_, err := exec.Command("sh", "-c", "echo 'daemon is down' >&2; exit 1").Output()
			return "", err
		})
		d := &Diagnosis{}
		diagnoseProbe(context.Background(), rt, "img", []string{t.TempDir()}, d)
		assert.True(t, strings.HasPrefix(d.SharedFS, "unprobed:"), "a run failure is not a sharing verdict: %q", d.SharedFS)
		assert.Contains(t, d.SharedFS, "daemon is down")
		assert.NotContains(t, d.SharedFS, "mismatch")
	})

	t.Run("content mismatch → mismatch", func(t *testing.T) {
		stubProbeExec(t, func(string) (string, error) { return "", nil }) // empty read = DooD signature
		d := &Diagnosis{}
		diagnoseProbe(context.Background(), rt, "img", []string{t.TempDir()}, d)
		assert.True(t, strings.HasPrefix(d.SharedFS, "mismatch:"), "%q", d.SharedFS)
	})

	t.Run("shared → ok", func(t *testing.T) {
		stubProbeExec(t, func(markerPath string) (string, error) {
			b, err := os.ReadFile(markerPath)
			return string(b), err
		})
		d := &Diagnosis{}
		diagnoseProbe(context.Background(), rt, "img", []string{t.TempDir()}, d)
		assert.Equal(t, "ok", d.SharedFS)
	})
}

// TestDiagnoseStaleness_NamesTheNotCheckedCases pins the in-scope half of a
// finding: `ImageStale bool json:"image_stale,omitempty"` cannot distinguish
// "not stale" from "not checked", and there are TWO ways to reach not-checked
// — a user-owned isolation_images override (no build sources, so ctxloom never
// inspects it) and an unresolvable expected provenance (the same fail-open
// case closed on the run path). Both used to report false with the report
// saying nothing about it. The field's ambiguity is a JSON payload shape
// question and is escalated separately; the guidance must at minimum SAY which
// case produced the false.
func TestDiagnoseStaleness_NamesTheNotCheckedCases(t *testing.T) {
	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}

	t.Run("user-owned override is never inspected", func(t *testing.T) {
		c := Container{
			runtime: fakeRuntime{name: "docker", binary: "true", available: true},
			image:   "my-registry/my-kiro:v2",
		}
		d := &Diagnosis{ImagePresent: true}
		diagnoseStaleness(context.Background(), c, "kiro", nil, nil, d)

		assert.False(t, d.ImageStale)
		require.NotEmpty(t, d.Guidance, "a never-inspected image must not report a bare false")
		assert.Contains(t, strings.Join(d.Guidance, "\n"), "isolation_images")
	})

	t.Run("unresolvable provenance is not checked", func(t *testing.T) {
		clearProvenanceCache(t)
		orig := resolveSelfExe
		resolveSelfExe = func() (string, error) { return "", errors.New("no linux ctxloom here") }
		t.Cleanup(func() { resolveSelfExe = orig })

		c := Container{
			runtime:    fakeRuntime{name: "docker", binary: "true", available: true},
			image:      "ctxloom-agent-diagnose-unverifiable:latest",
			engineSpec: spec,
		}
		sources, devBase, _ := c.containerBuildSources("")
		require.NotEmpty(t, sources, "precondition: a composable spec has build sources")

		d := &Diagnosis{ImagePresent: true}
		diagnoseStaleness(context.Background(), c, "kiro", sources, devBase, d)

		assert.False(t, d.ImageStale)
		require.NotEmpty(t, d.Guidance)
		assert.Contains(t, strings.Join(d.Guidance, "\n"), "could not be checked",
			"an unverifiable image must say so rather than read as up to date")
	})
}
