//go:build docker_integration

// The live-daemon proof that a store declaring Files gives a container those
// files and nothing else (local-secrets risk 2): a write to the store's
// settings.json inside the container cannot reach the host, the rest of the
// store is not visible, and the declared credential file still takes the
// write claude's refresh makes. Build-tagged so `just test` never compiles
// it; run with:
//
//	just test-pkg ./internal/adapters/isolation -tags docker_integration -run TestLoginStoreBind_
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

// containerScript plays the two things a container agent does with the
// store: an attack (a hook into settings.json, a look at the transcripts)
// and claude's refresh (a rename over the credential file, which a
// single-file bind refuses with EBUSY, then claude's in-place fallback).
const containerScript = `d="$HOME/.claude"
echo '{"hooks":"PWNED"}' > "$d/settings.json"
mkdir -p "$d/projects/evil" && echo x > "$d/projects/evil/t.jsonl"
ls -A "$d"
echo refreshed > "$d/.cred.tmp"
if mv "$d/.cred.tmp" "$d/.credentials.json" 2>/dev/null; then echo RENAMED; else echo RENAME-REFUSED; fi
echo refreshed > "$d/.credentials.json"`

func TestLoginStoreBind_OnlyTheCredentialFileReachesTheHost(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the login-store bind integration test")
	rt := ProbeRuntime("docker")
	require.Equal(t, "docker", rt.Name())

	home := testsupport.Isolate(t) // a fixture store under a fake $HOME, never a real login
	store := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "projects"), 0o700))
	host := map[string]string{
		".credentials.json": "fixture-credential",
		"settings.json":     `{"hooks":{}}`,
		"history.jsonl":     "fixture-transcript",
	}
	for f, body := range host {
		require.NoError(t, os.WriteFile(filepath.Join(store, f), []byte(body), 0o600))
	}

	env, mounts, err := containerRelocator{rt: rt, home: defaultContainerHome}.relocateStores([]sharedStore{{
		SharedStore: engine.SharedStore{Var: "STORE_VAR", HomeRel: ".claude", Files: []string{".credentials.json"}},
		hostDir:     store,
	}})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	name := containerName("loginstore-itest")
	t.Cleanup(func() { _ = exec.Command(rt.Binary(), "rm", "-f", name).Run() })
	spec := RunSpec{
		Image:   "alpine:latest",
		Name:    name,
		WorkDir: "/",
		Env:     []string{"HOME=" + defaultContainerHome, "STORE_VAR=" + env["STORE_VAR"]},
		Command: []string{"sh", "-c", containerScript},
		Mounts:  mounts,
	}
	var stdout, stderr bytes.Buffer
	run := exec.CommandContext(ctx, rt.Binary(), mustRunArgs(t, rt, spec)...)
	run.Stdout, run.Stderr = &stdout, &stderr
	require.NoError(t, run.Run(), "run the container: stdout=%s stderr=%s", stdout.String(), stderr.String())
	out := stdout.String()

	assert.NotContains(t, out, "history.jsonl", "the human's transcripts are not visible in the container")
	assert.Contains(t, out, "RENAME-REFUSED", "a rename over the bound file is refused, so claude takes its in-place arm")
	for _, f := range []string{"settings.json", "history.jsonl"} {
		got, err := os.ReadFile(filepath.Join(store, f))
		require.NoError(t, err)
		assert.Equal(t, host[f], string(got), "%s on the host is untouched", f)
	}
	assert.NoDirExists(t, filepath.Join(store, "projects", "evil"), "nothing the container creates reaches the host store")
	got, err := os.ReadFile(filepath.Join(store, ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, "refreshed", strings.TrimSpace(string(got)), "the in-place rewrite of the credential file reaches the host")
}
