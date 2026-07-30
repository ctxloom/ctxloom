//go:build linux

package procsec_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/procsec"
)

const (
	// helperEnv guards TestHelperInspectionTarget so that an ordinary `go
	// test` run of this package, which matches it like any other Test
	// function, is an instant no-op.
	helperEnv = "CTXLOOM_PROCSEC_HELPER_PROCESS"
	// helperHardenEnv selects whether the helper runs the production startup
	// path. Unset is the UNHARDENED baseline — the configuration that proves
	// the hazard this package exists to raise the cost of.
	helperHardenEnv = "CTXLOOM_PROCSEC_HELPER_HARDEN"

	// helperSecretEnv/helperSecret stand in for the coordinator credential
	// without importing the package that owns it: a 64-char token placed in
	// the child's EXEC environment, where /proc/<pid>/environ snapshots it.
	helperSecretEnv = "CTXLOOM_PROCSEC_HELPER_CRED"
	helperSecret    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// TestHelperInspectionTarget is not a test — it is the re-exec target the
// cases below spawn via os.Args[0] (the standard library's TestHelperProcess
// idiom, os/exec_test.go). It plays an ordinary ctxloom process: run the
// startup path (or, for the baseline, do not), then announce readiness and
// block so the parent can inspect a LIVE /proc entry. Readiness is a file
// rather than a stdout line because the process whose /proc is under
// inspection must be past its hardening call before the parent reads.
func TestHelperInspectionTarget(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	if os.Getenv(helperHardenEnv) == "1" {
		procsec.HardenAtStartup("ctxloom")
	}
	readyPath := os.Args[len(os.Args)-1]
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write ready file:", err)
		os.Exit(1)
	}
	time.Sleep(100 * time.Second)
}

// TestSameUIDEnvironExposure is the measurement this package is built from.
// It asserts the PAYLOAD at every step — the secret's bytes present or the
// read denied, and the warning's text — never an exit code, because the whole
// failure mode being guarded against is a process that reports success while
// leaving the secret readable.
func TestSameUIDEnvironExposure(t *testing.T) {
	tests := []struct {
		name string
		// harden runs the production startup path in the child.
		harden bool
		// allowInspection sets the bypass env var in the child.
		allowInspection bool
		// wantSecretReadable is the whole point: whether a same-uid peer
		// (this test process) can lift the credential with a file read.
		wantSecretReadable bool
		wantStderrContains []string
	}{
		{
			// The hazard, unmitigated. If this case ever stops finding the
			// secret, the other two prove nothing.
			name:               "baseline: an unhardened process leaks its exec environment to same-uid peers",
			wantSecretReadable: true,
		},
		{
			name:   "hardened: the read is denied",
			harden: true,
		},
		{
			name:               "bypassed: readability is restored and loudly reported",
			harden:             true,
			allowInspection:    true,
			wantSecretReadable: true,
			wantStderrContains: []string{
				"ctxloom: warning:",
				procsec.EnvAllowProcessInspection + "=1",
				"/environ",
				"CTXLOOM_COORD_CRED",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			readyPath := dir + "/ready"
			stderrPath := dir + "/stderr"

			// The child's stderr goes to a file, not an in-memory buffer: the
			// parent reads it while the child is still running.
			stderrFile, err := os.Create(stderrPath)
			require.NoError(t, err)
			defer func() { _ = stderrFile.Close() }()

			env := []string{
				helperEnv + "=1",
				helperSecretEnv + "=" + helperSecret,
				"HOME=" + os.Getenv("HOME"),
				"PATH=" + os.Getenv("PATH"),
			}
			if tc.harden {
				env = append(env, helperHardenEnv+"=1")
			}
			if tc.allowInspection {
				env = append(env, procsec.EnvAllowProcessInspection+"=1")
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperInspectionTarget$", "-test.timeout=0", "--", readyPath)
			cmd.Env = env
			cmd.Stderr = stderrFile
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})

			pid := cmd.Process.Pid
			waitForFile(t, readyPath)

			data, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			secret := helperSecretEnv + "=" + helperSecret

			if tc.wantSecretReadable {
				require.NoError(t, readErr)
				require.Contains(t, string(data), secret,
					"the exec-time environment snapshot still holds the credential")
			} else {
				require.Error(t, readErr, "same-uid read of a hardened process's environ must fail")
				require.ErrorIs(t, readErr, fs.ErrPermission)
				require.Empty(t, data, "a denied read must yield no bytes at all")
				require.NotContains(t, string(data), secret)
				t.Logf("denied with errno %v", errnoOf(readErr))
			}

			stderrText := readFileNow(t, stderrPath)
			for _, want := range tc.wantStderrContains {
				require.Contains(t, stderrText, want)
			}
			if len(tc.wantStderrContains) == 0 {
				require.NotContains(t, stderrText, "warning:",
					"a run that hardened successfully has nothing to report")
			}
		})
	}
}

// waitForFile blocks until path exists, failing the test rather than hanging
// forever if the helper never got that far.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper never became ready (%s not written)", path)
}

// readFileNow reads a file that a live process may still be appending to;
// a partial read is fine, the assertions are substring checks.
func readFileNow(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// errnoOf reports the raw errno behind a *PathError, for the log line that
// records WHICH denial the kernel produced.
func errnoOf(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("%d (%s)", int(errno), errno.Error())
	}
	return strings.TrimSpace(err.Error())
}
