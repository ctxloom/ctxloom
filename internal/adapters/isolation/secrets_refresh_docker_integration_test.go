//go:build docker_integration && !windows

// The live-daemon proof of the container re-auth path: a restarted
// coordinator rewrites a re-adopted container's secrets file IN PLACE
// (RefreshSecrets, on the host), and the container — which mounts the secret
// DIR read-only — reads the new value at its next turn. What only a real
// runtime can show is that the host-side atomic rewrite is visible through the
// mount; the runner's per-turn read itself (turnExec) is unit-tested. Build-
// tagged so `just test` never compiles it; run with:
//
//	just test-pkg ./internal/adapters/isolation -tags docker_integration -run TestSecretRefresh_
package isolation

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// turningRunner stands in for the runner: every turn (a short tick) it reads
// its credential out of the dotenv file the Placement names, as
// runner.readSecretFiles does, and reports a digest of what it read — never
// the value — whenever that changes.
const turningRunner = `last=; while :; do ` +
	`tok="$(sed -n "s/^${SECRET_VAR}=\"\(.*\)\"\$/\1/p" "$SECRET_FILE" 2>/dev/null)"; ` +
	`if [ -n "$tok" ]; then d="$(printf %s "$tok" | sha256sum | cut -d' ' -f1)"; ` +
	`if [ "$d" != "$last" ]; then echo "TURN:$d"; last="$d"; fi; fi; sleep 0.2; done`

// refreshedSecret is the credential the restart hands the container.
const refreshedSecret = "sk-ant-oat01-refreshed-fixture"

// digest is the turn's report of value.
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "TURN:" + hex.EncodeToString(sum[:])
}

func TestSecretRefresh_ARealContainersNextTurnReadsTheRewrittenSecret(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the secret-refresh integration test")
	realEnv := os.Environ()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	testsupport.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			rt := ProbeRuntime(name)
			dockergate.RequireNamedRuntime(t, name, rt != nil && rt.Available(), "the secret-refresh integration test")
			env, _, prepared := preparedSecretCell(t)
			m := secretMount(t, prepared)
			file := env.Placement().SecretFiles[secretVar]
			require.NotEmpty(t, file)

			cname := containerName("secret-refresh-itest-" + name)
			t.Cleanup(func() { rm := exec.Command(rt.Binary(), "rm", "-f", cname); rm.Env = realEnv; _ = rm.Run() })
			args := mustRunArgs(t, rt, RunSpec{
				Image:   "alpine:latest",
				Name:    cname,
				WorkDir: "/",
				Env:     []string{"SECRET_FILE=" + file, "SECRET_VAR=" + secretVar},
				Command: []string{"sh", "-c", turningRunner},
				Mounts:  []mount{m},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			run := exec.CommandContext(ctx, rt.Binary(), args...)
			run.Env = realEnv
			stderr := stderrtail.New(stderrtail.DefaultBytes)
			run.Stderr = stderr
			out, err := run.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, run.Start())
			defer func() { _ = run.Process.Kill(); _ = run.Wait() }()

			turns := make(chan string, 8)
			go func() {
				defer close(turns)
				for s := bufio.NewScanner(out); s.Scan(); {
					turns <- s.Text()
				}
			}()
			next := func(what string) string {
				t.Helper()
				select {
				case line, ok := <-turns:
					require.True(t, ok, "the container ended before %s (stderr %s)", what, stderr.Tail())
					require.True(t, strings.HasPrefix(line, "TURN:"), "a turn reports a digest only")
					return line
				case <-time.After(60 * time.Second):
					t.Fatalf("no turn reported %s (stderr %s)", what, stderr.Tail())
					return ""
				}
			}
			require.Equal(t, digest(fixtureSecret), next("the launch credential"), "premise: the first turn read the credential it launched with")

			// The coordinator that launched the container dies: its lock goes,
			// the dir and the container's mount of it stay. The restarted one
			// takes the dir over and rewrites the credential in place.
			require.NoError(t, env.(*containerEnvironment).cw.secrets.scratch.lock.Unlock())
			release, err := RefreshSecrets(env.SecretsFile(), map[string]string{secretVar: refreshedSecret})
			require.NoError(t, err)
			defer release()

			require.Equal(t, digest(refreshedSecret), next("the refreshed credential"),
				"the container's next turn reads the rewritten secret through its read-only mount")
		})
	}
}
