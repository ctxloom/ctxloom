//go:build docker_integration

// The live-daemon proof that a credential FILE relocateFiles presents is a
// single-file bind a real runtime accepts, readable at the path the var is
// rewritten to and not writable there. Build-tagged so `just test` never
// compiles it; run with:
//
//	just test-pkg ./internal/adapters/isolation -tags docker_integration -run TestCredentialFileBind_
package isolation

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

func TestCredentialFileBind_ReadableReadOnlyAtTheRewrittenPath(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the credential-file bind integration test")
	rt := ProbeRuntime("docker")
	require.Equal(t, "docker", rt.Name())

	home := testsupport.Isolate(t) // a fixture file under a fake $HOME, never a real credential
	const fixture = "[default]\nregion = fixture-1\n"
	f := filepath.Join(home, "fixture-aws-config")
	require.NoError(t, os.WriteFile(f, []byte(fixture), 0o644))

	env, mounts, err := containerRelocator{rt: rt, home: defaultContainerHome}.relocateFiles(engine.Credentials{
		Env: map[string]string{"AWS_CONFIG_FILE": f}, FileVars: []string{"AWS_CONFIG_FILE"},
	})
	require.NoError(t, err)
	require.Len(t, mounts, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	name := containerName("credfile-itest")
	t.Cleanup(func() { _ = exec.Command(rt.Binary(), "rm", "-f", name).Run() })
	spec := RunSpec{
		Image:   "alpine:latest",
		Name:    name,
		WorkDir: "/",
		Env:     []string{"HOME=" + defaultContainerHome, "AWS_CONFIG_FILE=" + env["AWS_CONFIG_FILE"]},
		Command: []string{"sh", "-c", `cat "$AWS_CONFIG_FILE"; if { echo x >> "$AWS_CONFIG_FILE"; } 2>/dev/null; then echo WRITABLE; else echo READONLY; fi`},
		Mounts:  mounts,
	}
	// Stdout alone is compared: on a cold image cache the CLI writes its pull
	// progress to stderr, and that must not reach the exact-bytes assertion.
	var stdout, stderr bytes.Buffer
	run := exec.CommandContext(ctx, rt.Binary(), mustRunArgs(t, rt, spec)...)
	run.Stdout, run.Stderr = &stdout, &stderr
	require.NoError(t, run.Run(), "run the container: stdout=%s stderr=%s", stdout.String(), stderr.String())
	assert.Equal(t, fixture+"READONLY", strings.TrimSpace(stdout.String()), "the file is read at the rewritten var and refuses a write")

	got, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Equal(t, fixture, string(got), "the host file is untouched")
}
