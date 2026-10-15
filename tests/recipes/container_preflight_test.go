// Package recipes tests justfile recipes by running the real justfile through
// `just` against fakes, the way a human would invoke them.
package recipes

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// preflightEnvKeys is every variable _container-preflight reads. Each case
// starts with all of them stripped, so the developer's or CI's own values
// (CI=true above all, which would skip every case) cannot leak in.
var preflightEnvKeys = []string{
	"DEVCONTAINER", "CI", "GITHUB_ACTIONS", "CTXLOOM_SKIP_CONTAINER_PREFLIGHT",
	"CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT", "DOCKER_HOST", "XDG_RUNTIME_DIR",
	"CONTAINER_CMD", "JUST_JUSTFILE",
}

// The message fragments a reader of the failure needs: the remedy, and the
// warning that the remedy takes every other container down with it.
const (
	restartFix     = "Fix: systemctl --user restart docker"
	restartWarning = "every other container on it"
)

// fakeRuntime is a fake $XDG_RUNTIME_DIR laid out like dockerd-rootless.sh's.
type fakeRuntime struct{ dir string }

// newRuntime makes an empty runtime dir. It is NOT t.TempDir(): that path
// embeds the test name, and a socket path past ~108 bytes cannot be bound.
func newRuntime(t *testing.T) fakeRuntime {
	t.Helper()
	dir, err := os.MkdirTemp("", "cpf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return fakeRuntime{dir: dir}
}

func (r fakeRuntime) apiSock() string       { return filepath.Join(r.dir, "dockerd-rootless", "api.sock") }
func (r fakeRuntime) containerdDir() string { return filepath.Join(r.dir, "docker", "containerd") }
func (r fakeRuntime) containerdSock() string {
	return filepath.Join(r.containerdDir(), "containerd.sock")
}
func (r fakeRuntime) endpoint() string { return "unix://" + filepath.Join(r.dir, "docker.sock") }

// bind creates a real listening unix socket at path, closed at cleanup.
func bind(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

// healthy lays out both sockets a healthy rootless daemon keeps.
func (r fakeRuntime) healthy(t *testing.T) fakeRuntime {
	bind(t, r.apiSock())
	bind(t, r.containerdSock())
	return r
}

// fakeEngine writes an executable named name whose body is script, recording
// each invocation's argv (one line per call) in the returned calls file.
func fakeEngine(t *testing.T, name, script string) (path, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	path = filepath.Join(dir, name)
	body := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, calls
}

// rootlessDocker answers `info` as rootless docker does and `context inspect`
// with ctxEndpoint.
func rootlessDocker(t *testing.T, ctxEndpoint string) (string, string) {
	return fakeEngine(t, "docker", `case "$1" in
info) printf 'Server:\n Security Options:\n  seccomp\n  rootless\n';;
context) echo '`+ctxEndpoint+`';;
esac`)
}

type preflightRun struct {
	code    int
	out     string
	elapsed time.Duration
}

// runPreflight runs `just _container-preflight [engine]` from the real
// justfile with env (KEY=VALUE) layered over a scrubbed environment.
func runPreflight(t *testing.T, env []string, engine ...string) preflightRun {
	t.Helper()
	root := repoRoot(t)
	// Read the justfile here so go test's cache keys on it: a run through
	// exec is read by just, not this binary, and an edit to the recipe would
	// otherwise be answered with a cached PASS.
	if _, err := os.ReadFile(filepath.Join(root, "justfile")); err != nil {
		t.Fatal(err)
	}
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); this test drives the real _container-preflight recipe", err)
	}
	args := append([]string{"--justfile", filepath.Join(root, "justfile"), "--working-directory", root,
		"_container-preflight"}, engine...)
	cmd := exec.Command(just, args...)
	cmd.Env = append(scrubbedEnv(), env...)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	run := preflightRun{out: string(out), elapsed: time.Since(start)}
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running just: %v\n%s", err, out)
		}
		run.code = exitErr.ExitCode()
	}
	return run
}

func scrubbedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		keep := true
		for _, k := range preflightEnvKeys {
			if strings.HasPrefix(kv, k+"=") {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	return env
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func readCalls(t *testing.T, calls string) string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func wantPass(t *testing.T, r preflightRun) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", r.code, r.out)
	}
}

func wantFail(t *testing.T, r preflightRun, fragments ...string) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("want a failure, got exit 0:\n%s", r.out)
	}
	for _, f := range fragments {
		if !strings.Contains(r.out, f) {
			t.Errorf("output lacks %q:\n%s", f, r.out)
		}
	}
}

func standardEnv(rt fakeRuntime) []string {
	return []string{"XDG_RUNTIME_DIR=" + rt.dir, "DOCKER_HOST=" + rt.endpoint()}
}

func TestContainerPreflight_RootlessLayout(t *testing.T) {
	t.Parallel()

	t.Run("healthy rootless daemon passes", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t).healthy(t)
		docker, calls := rootlessDocker(t, "")
		wantPass(t, runPreflight(t, standardEnv(rt), docker))
		if got := readCalls(t, calls); !strings.HasPrefix(got, "info\n") {
			t.Errorf("docker must be probed with `info`; calls:\n%s", got)
		}
	})

	t.Run("missing api.sock is the wedge", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		bind(t, rt.containerdSock())
		docker, _ := rootlessDocker(t, "")
		r := runPreflight(t, standardEnv(rt), docker)
		wantFail(t, r, "runtime state is gone", rt.apiSock(), restartFix, restartWarning)
		if strings.Contains(r.out, rt.containerdSock()) {
			t.Errorf("containerd.sock is present and must not be reported missing:\n%s", r.out)
		}
	})

	t.Run("api.sock that is not a socket is the wedge", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		bind(t, rt.containerdSock())
		if err := os.MkdirAll(filepath.Dir(rt.apiSock()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rt.apiSock(), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		docker, _ := rootlessDocker(t, "")
		wantFail(t, runPreflight(t, standardEnv(rt), docker), rt.apiSock())
	})

	t.Run("missing containerd.sock under an existing containerd dir is the wedge", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		bind(t, rt.apiSock())
		if err := os.MkdirAll(rt.containerdDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		docker, _ := rootlessDocker(t, "")
		r := runPreflight(t, standardEnv(rt), docker)
		wantFail(t, r, rt.containerdSock(), restartFix, restartWarning)
		if strings.Contains(r.out, rt.apiSock()) {
			t.Errorf("api.sock is present and must not be reported missing:\n%s", r.out)
		}
	})

	t.Run("both sockets missing are both named", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		if err := os.MkdirAll(rt.containerdDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		docker, _ := rootlessDocker(t, "")
		wantFail(t, runPreflight(t, standardEnv(rt), docker), rt.apiSock(), rt.containerdSock())
	})

	t.Run("no containerd dir skips the containerd check", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		bind(t, rt.apiSock())
		docker, _ := rootlessDocker(t, "")
		wantPass(t, runPreflight(t, standardEnv(rt), docker))
	})

	t.Run("unset DOCKER_HOST resolves the endpoint from the docker context", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, calls := rootlessDocker(t, rt.endpoint())
		wantFail(t, runPreflight(t, []string{"XDG_RUNTIME_DIR=" + rt.dir}, docker), rt.apiSock())
		if got := readCalls(t, calls); !strings.Contains(got, "context inspect --format {{.Endpoints.docker.Host}}\n") {
			t.Errorf("the endpoint must come from `docker context inspect`; calls:\n%s", got)
		}
	})

	t.Run("a docker context at another endpoint is not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, _ := rootlessDocker(t, "unix://"+filepath.Join(rt.dir, "other.sock"))
		wantPass(t, runPreflight(t, []string{"XDG_RUNTIME_DIR=" + rt.dir}, docker))
	})

	t.Run("a non-standard DOCKER_HOST is not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, _ := rootlessDocker(t, rt.endpoint())
		env := []string{"XDG_RUNTIME_DIR=" + rt.dir, "DOCKER_HOST=unix://" + filepath.Join(rt.dir, "other.sock")}
		wantPass(t, runPreflight(t, env, docker))
	})

	t.Run("a remote DOCKER_HOST is not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, _ := rootlessDocker(t, rt.endpoint())
		wantPass(t, runPreflight(t, []string{"XDG_RUNTIME_DIR=" + rt.dir, "DOCKER_HOST=tcp://10.0.0.1:2376"}, docker))
	})

	t.Run("no XDG_RUNTIME_DIR is not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, _ := rootlessDocker(t, rt.endpoint())
		wantPass(t, runPreflight(t, []string{"DOCKER_HOST=" + rt.endpoint()}, docker))
	})

	t.Run("rootful docker at the standard endpoint is not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, _ := fakeEngine(t, "docker", `echo 'Security Options: seccomp'`)
		wantPass(t, runPreflight(t, standardEnv(rt), docker))
	})

	t.Run("podman reporting rootless is probed with version and not checked", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		podman, calls := fakeEngine(t, "podman", `[ "$1" = version ] || exit 7
echo 'rootless: true'`)
		wantPass(t, runPreflight(t, standardEnv(rt), podman))
		if got := readCalls(t, calls); got != "version\n" {
			t.Errorf("podman must be probed with `version` only; calls:\n%s", got)
		}
	})

	t.Run("the engine defaults to CONTAINER_CMD", func(t *testing.T) {
		t.Parallel()
		rt := newRuntime(t)
		docker, calls := rootlessDocker(t, "")
		wantFail(t, runPreflight(t, append(standardEnv(rt), "CONTAINER_CMD="+docker)), rt.apiSock())
		if readCalls(t, calls) == "" {
			t.Error("CONTAINER_CMD's engine was never called")
		}
	})
}

func TestContainerPreflight_EngineProbe(t *testing.T) {
	t.Parallel()

	t.Run("an engine not on PATH fails", func(t *testing.T) {
		t.Parallel()
		wantFail(t, runPreflight(t, nil, "ctxloom-no-such-engine"), "'ctxloom-no-such-engine' is not on PATH")
	})

	t.Run("a failing probe fails with its exit code", func(t *testing.T) {
		t.Parallel()
		docker, _ := fakeEngine(t, "docker", `echo 'Cannot connect to the Docker daemon' >&2; exit 3`)
		wantFail(t, runPreflight(t, nil, docker), "info' failed (exit 3)", "Cannot connect", "Start the engine")
	})

	t.Run("a hung docker is cut off by the timeout and names the restart", func(t *testing.T) {
		t.Parallel()
		docker, _ := fakeEngine(t, "docker", `exec sleep 30`)
		r := runPreflight(t, []string{"CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT=1"}, docker)
		wantFail(t, r, "did not answer within 1s (daemon hung)", restartFix, restartWarning)
		if r.elapsed > 15*time.Second {
			t.Errorf("the probe was not bounded: took %s", r.elapsed)
		}
	})

	t.Run("a hung non-docker engine is cut off without the docker remedy", func(t *testing.T) {
		t.Parallel()
		podman, _ := fakeEngine(t, "podman", `exec sleep 30`)
		r := runPreflight(t, []string{"CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT=1"}, podman)
		wantFail(t, r, "'"+podman+" version' did not answer within 1s (engine hung)")
		if strings.Contains(r.out, restartFix) {
			t.Errorf("a non-docker engine must not be told to restart docker:\n%s", r.out)
		}
	})
}

func TestContainerPreflight_Skips(t *testing.T) {
	t.Parallel()
	for _, kv := range []string{"DEVCONTAINER=1", "CI=true", "GITHUB_ACTIONS=true", "CTXLOOM_SKIP_CONTAINER_PREFLIGHT=1"} {
		t.Run(kv, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime(t) // the wedge: no sockets at all
			docker, calls := rootlessDocker(t, "")
			wantPass(t, runPreflight(t, append(standardEnv(rt), kv), docker))
			if got := readCalls(t, calls); got != "" {
				t.Errorf("a skipped preflight must not touch the engine; calls:\n%s", got)
			}
		})
	}
}
