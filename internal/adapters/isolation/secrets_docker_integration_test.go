//go:build docker_integration && !windows

// The live-daemon proof of local-secrets risk 1: a credential the
// originator materialized (Container.bind + Container.environment) reaches
// the engine inside a REAL container through the read-only secret mount, at
// the path the Placement names, and appears nowhere in the container's own
// configuration. Build-tagged so `just test` never compiles it; run with:
//
//	just test-pkg ./internal/adapters/isolation -tags docker_integration -run TestSecretMount_
package isolation

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// fakeEngine stands in for the runner + engine pair: it takes its credential
// from the file the Placement names (as runner.redeemSecrets does), proves it
// authenticates by echoing the exact value it holds, proves the mount refuses
// a write, then stays up so the container can be inspected.
const fakeEngine = `tok="$(cat "$SECRET_FILE")" && printf 'AUTH:%s\n' "$tok"; ` +
	`if { echo x >> "$SECRET_FILE"; } 2>/dev/null; then echo WRITABLE; else echo READONLY; fi; sleep 60`

func TestSecretMount_ARealContainerAuthenticatesFromTheMountedSecret(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the secret-mount integration test")
	// The runtime CLIs run under the REAL environment: rootless podman keeps
	// its image store under $HOME, which Isolate replaces. The secret lives
	// on the real user tmpfs when there is one, as in production.
	realEnv := os.Environ()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	testsupport.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	env, _, prepared := preparedSecretCell(t)
	m := secretMount(t, prepared)
	file := env.Placement().SecretFiles[secretVar]
	require.NotEmpty(t, file)

	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			rt := ProbeRuntime(name)
			if rt == nil || !rt.Available() {
				t.Skipf("%s is not available", name)
			}
			cname := containerName("secret-itest-" + name)
			t.Cleanup(func() { rm := exec.Command(rt.Binary(), "rm", "-f", cname); rm.Env = realEnv; _ = rm.Run() })
			spec := RunSpec{
				Image:   "alpine:latest",
				Name:    cname,
				WorkDir: "/",
				Env:     []string{"SECRET_FILE=" + file},
				Command: []string{"sh", "-c", fakeEngine},
				Mounts:  []mount{m},
			}
			args := mustRunArgs(t, rt, spec)
			assert.NotContains(t, strings.Join(args, " "), fixtureSecret, "the run argv never carries the value")

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			run := exec.CommandContext(ctx, rt.Binary(), args...)
			// The run CLI's own env holds no credential either.
			run.Env = realEnv
			stderr := stderrtail.New(stderrtail.DefaultBytes)
			run.Stderr = stderr
			out, err := run.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, run.Start())
			defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

			lines := bufio.NewScanner(out)
			var got []string
			for len(got) < 2 && lines.Scan() {
				got = append(got, lines.Text())
			}
			require.Equal(t, []string{"AUTH:" + fixtureSecret, "READONLY"}, got, "the engine holds the exact value, and the mount refuses a write (args %q, stderr %s)", args, stderr.Tail())

			inspectCmd := exec.CommandContext(ctx, rt.Binary(), "inspect", cname)
			inspectCmd.Env = realEnv
			inspect, err := inspectCmd.CombinedOutput()
			require.NoError(t, err, "inspect the running container: %s", inspect)
			assert.NotContains(t, string(inspect), fixtureSecret, "the container's configuration holds no credential value")
			assert.Contains(t, string(inspect), secretsTarget, "premise: inspected the container that mounts the secret")
		})
	}
}
