package isolation

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// wedgeRuntimeDir is a fake $XDG_RUNTIME_DIR in the dockerd-rootless.sh
// layout. Not t.TempDir(): that path embeds the test name, and a unix socket
// path past ~108 bytes cannot be bound.
type wedgeRuntimeDir struct{ dir string }

func newWedgeRuntimeDir(t *testing.T) wedgeRuntimeDir {
	t.Helper()
	dir, err := os.MkdirTemp("", "wedge")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return wedgeRuntimeDir{dir: dir}
}

func (r wedgeRuntimeDir) apiSock() string {
	return filepath.Join(r.dir, "dockerd-rootless", "api.sock")
}
func (r wedgeRuntimeDir) containerdDir() string { return filepath.Join(r.dir, "docker", "containerd") }
func (r wedgeRuntimeDir) containerdSock() string {
	return filepath.Join(r.containerdDir(), "containerd.sock")
}
func (r wedgeRuntimeDir) endpoint() string { return "unix://" + r.dir + "/docker.sock" }

// bindSocket listens on a real unix socket at path until the test ends.
func bindSocket(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
}

func (r wedgeRuntimeDir) healthy(t *testing.T) wedgeRuntimeDir {
	bindSocket(t, r.apiSock())
	bindSocket(t, r.containerdSock())
	return r
}

var errNoContext = errors.New("no docker context")

// stubRootlessState answers the wedge check's environment from env and its
// docker-context lookup with ctxHost (an error when ctxHost is "").
func stubRootlessState(t *testing.T, env map[string]string, ctxHost string) *int {
	t.Helper()
	prev := rootlessState
	lookups := new(int)
	rootlessState = rootlessStateProbe{
		getenv: func(k string) string { return env[k] },
		contextHost: func(context.Context, string) (string, error) {
			*lookups++
			if ctxHost == "" {
				return "", errNoContext
			}
			return ctxHost, nil
		},
	}
	t.Cleanup(func() { rootlessState = prev })
	return lookups
}

func rootlessDocker() Docker {
	d := Docker{rootless: true}
	d.reachable = true
	return d
}

func standardWedgeEnv(r wedgeRuntimeDir) map[string]string {
	return map[string]string{"XDG_RUNTIME_DIR": r.dir, "DOCKER_HOST": r.endpoint()}
}

// requireWedge asserts err is the wedge, naming exactly the missing paths and
// carrying the restart remedy.
func requireWedge(t *testing.T, err error, missing []string, present []string) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, errRootlessStateGone)
	remedy, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the wedge must carry its remedy: %v", err)
	assert.Equal(t, rootlessRestartRemedy, remedy)
	assert.Contains(t, remedy, "systemctl --user restart docker")
	assert.Contains(t, remedy, "every other container on it")
	for _, p := range missing {
		assert.Contains(t, err.Error(), p)
	}
	for _, p := range present {
		assert.NotContains(t, err.Error(), p, "a socket that exists must not be reported missing")
	}
}

// TestRootlessDaemonState_Layout pins the rule the justfile's
// _container-preflight recipe applies too (tests/recipes covers that side).
func TestRootlessDaemonState_Layout(t *testing.T) {
	ctx := context.Background()

	t.Run("healthy layout passes", func(t *testing.T) {
		r := newWedgeRuntimeDir(t).healthy(t)
		stubRootlessState(t, standardWedgeEnv(r), "")
		assert.NoError(t, rootlessDocker().daemonState(ctx))
	})

	t.Run("missing api.sock is the wedge", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		bindSocket(t, r.containerdSock())
		stubRootlessState(t, standardWedgeEnv(r), "")
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.apiSock()}, []string{r.containerdSock()})
	})

	t.Run("an api.sock that is not a socket is the wedge", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		bindSocket(t, r.containerdSock())
		require.NoError(t, os.MkdirAll(filepath.Dir(r.apiSock()), 0o700))
		require.NoError(t, os.WriteFile(r.apiSock(), nil, 0o600))
		stubRootlessState(t, standardWedgeEnv(r), "")
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.apiSock()}, nil)
	})

	t.Run("missing containerd.sock under its dir is the wedge", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		bindSocket(t, r.apiSock())
		require.NoError(t, os.MkdirAll(r.containerdDir(), 0o700))
		stubRootlessState(t, standardWedgeEnv(r), "")
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.containerdSock()}, []string{r.apiSock()})
	})

	t.Run("both missing are both named", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		require.NoError(t, os.MkdirAll(r.containerdDir(), 0o700))
		stubRootlessState(t, standardWedgeEnv(r), "")
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.apiSock(), r.containerdSock()}, nil)
	})

	t.Run("no containerd dir skips the containerd check", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		bindSocket(t, r.apiSock())
		stubRootlessState(t, standardWedgeEnv(r), "")
		assert.NoError(t, rootlessDocker().daemonState(ctx))
	})

	t.Run("unset DOCKER_HOST takes the docker context's endpoint", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		lookups := stubRootlessState(t, map[string]string{"XDG_RUNTIME_DIR": r.dir}, r.endpoint())
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.apiSock()}, nil)
		assert.Equal(t, 1, *lookups)
	})

	t.Run("a set DOCKER_HOST is not second-guessed by the context", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		lookups := stubRootlessState(t, standardWedgeEnv(r), "unix:///elsewhere.sock")
		requireWedge(t, rootlessDocker().daemonState(ctx), []string{r.apiSock()}, nil)
		assert.Zero(t, *lookups)
	})

	t.Run("a docker context at another endpoint is not checked", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		stubRootlessState(t, map[string]string{"XDG_RUNTIME_DIR": r.dir}, "unix://"+r.dir+"/other.sock")
		assert.NoError(t, rootlessDocker().daemonState(ctx))
	})

	t.Run("an unknown endpoint is not checked", func(t *testing.T) {
		r := newWedgeRuntimeDir(t)
		stubRootlessState(t, map[string]string{"XDG_RUNTIME_DIR": r.dir}, "")
		assert.NoError(t, rootlessDocker().daemonState(ctx))
	})

	for _, host := range []string{"tcp://10.0.0.1:2376", "unix:///var/run/docker.sock"} {
		t.Run("DOCKER_HOST "+host+" is not checked", func(t *testing.T) {
			r := newWedgeRuntimeDir(t)
			stubRootlessState(t, map[string]string{"XDG_RUNTIME_DIR": r.dir, "DOCKER_HOST": host}, "")
			assert.NoError(t, rootlessDocker().daemonState(ctx))
		})
	}

	t.Run("no XDG_RUNTIME_DIR is not checked", func(t *testing.T) {
		// The endpoint an empty runtime dir would expand to: without the gate
		// this would check /dockerd-rootless/api.sock and fail.
		stubRootlessState(t, map[string]string{"DOCKER_HOST": "unix:///docker.sock"}, "")
		assert.NoError(t, rootlessDocker().daemonState(ctx))
	})
}

// TestRootlessDaemonState_OnlyRootlessDocker: on the very layout that wedges
// rootless docker, every other runtime passes — the check must never
// false-fail a layout it does not own.
func TestRootlessDaemonState_OnlyRootlessDocker(t *testing.T) {
	r := newWedgeRuntimeDir(t)
	require.NoError(t, os.MkdirAll(r.containerdDir(), 0o700))
	stubRootlessState(t, standardWedgeEnv(r), r.endpoint())
	require.Error(t, rootlessDocker().daemonState(context.Background()), "precondition: the layout is wedged")

	rootful := Docker{}
	rootful.reachable = true
	for name, rt := range map[string]Runtime{
		"rootful docker":  rootful,
		"rootless podman": Podman{rootless: true},
		"rootful podman":  Podman{},
		"host":            Host{},
		"fake docker":     fakeRuntime{name: "docker", binary: "docker", available: true},
	} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, rt.daemonState(context.Background()))
		})
	}
}

// errImageReached marks a launchGate that got past the daemon checks.
var errImageReached = errors.New("image step reached")

// TestLaunchGate_RefusesAWedgedRootlessDocker: the launch is refused before
// the image step (which would build on the wedged daemon and hang), with the
// restart remedy — and the check runs on EVERY launch: a daemon that wedges
// after one launch passed is caught on the next, where the shared-fs probe's
// latched success would not notice.
func TestLaunchGate_RefusesAWedgedRootlessDocker(t *testing.T) {
	r := newWedgeRuntimeDir(t).healthy(t)
	stubRootlessState(t, standardWedgeEnv(r), "")
	c := Container{runtime: rootlessDocker()}
	imageCalls := 0
	image := func(context.Context) error { imageCalls++; return errImageReached }

	require.ErrorIs(t, c.launchGate(context.Background(), image), errImageReached, "a healthy daemon reaches the image step")

	require.NoError(t, os.Remove(r.apiSock()))
	err := c.launchGate(context.Background(), image)
	requireWedge(t, err, []string{r.apiSock()}, []string{r.containerdSock()})
	assert.Equal(t, 1, imageCalls, "a wedged daemon must be refused before the image step")
}

// fakeDockerOnPath puts a `docker` first on PATH that answers `image inspect`
// with imageExit (0 = present) and everything else with success and no output.
func fakeDockerOnPath(t *testing.T, imageExit string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1 $2\" = 'image inspect' ] && exit " + imageExit + "\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestDiagnose_ReportsAWedgedRootlessDocker: `ctxloom container check` on the
// wedge used to say only "reachable": true. It now names the fault and the
// remedy, and never starts the marker-probe container the wedged daemon
// could neither finish nor reap.
func TestDiagnose_ReportsAWedgedRootlessDocker(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wedged       bool
		imagePresent bool
	}{
		{"wedged, image absent", true, false},
		{"wedged, image present", true, true},
		{"healthy, image present", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWedgeRuntimeDir(t)
			if !tc.wedged {
				r.healthy(t)
			}
			stubRootlessState(t, standardWedgeEnv(r), "")
			stubRuntimeCandidates(t, runtimeCandidate{"docker", func() (Runtime, RuntimeAxis) {
				return rootlessDocker(), RuntimeContainerRootless
			}})
			imageExit := "1"
			if tc.imagePresent {
				imageExit = "0"
			}
			fakeDockerOnPath(t, imageExit)
			var runs int
			stubDaemonInfo(t, func(_ context.Context, args []string) (string, error) {
				if len(args) > 0 && args[0] == "run" {
					runs++
					return "", errors.New("probe container not run in this test")
				}
				return "daemon-host\n", nil
			})

			// A user-owned image override, so the staleness step's verdict is
			// fixed: it names the override as never inspected.
			d := Diagnose(context.Background(), "claude-code", ImageConfig{Image: "my-registry/wedge-agent:v1"})

			require.Equal(t, "docker", d.Runtime)
			assert.True(t, d.Reachable, "the API does answer")
			require.Equal(t, tc.imagePresent, d.ImagePresent, "precondition: the fake docker's image answer")
			if !tc.wedged {
				assert.Empty(t, d.DaemonFault)
				assert.Positive(t, runs, "a healthy daemon still gets the marker probe")
				return
			}
			assert.Contains(t, d.DaemonFault, r.apiSock())
			assert.Contains(t, d.DaemonFault, errRootlessStateGone.Error())
			assert.Contains(t, strings.Join(d.Guidance, "\n"), rootlessRestartRemedy)
			assert.Zero(t, runs, "no container may be started on a wedged daemon")
			guidance := strings.Join(d.Guidance, "\n")
			if tc.imagePresent {
				assert.Equal(t, "unprobed: the daemon cannot run the probe container (see daemon_fault)", d.SharedFS)
				assert.Contains(t, guidance, "isolation_images", "a present image is still reported on: its staleness step runs")
			} else {
				assert.NotContains(t, d.SharedFS, "daemon_fault", "an absent image takes the no-image advisory")
				assert.Contains(t, guidance, "agent image my-registry/wedge-agent:v1 is not present")
				assert.NotContains(t, guidance, "isolation_images", "there is no image to judge stale")
			}
		})
	}
}

// TestDockerContextHost_AsksTheCLIUnderItsOwnBound: the endpoint lookup asks
// the binary it is given for its current context's endpoint, trims the
// answer, passes a failure through, and runs under dockerContextTimeout of
// its own, even on a caller ctx with no deadline.
func TestDockerContextHost_AsksTheCLIUnderItsOwnBound(t *testing.T) {
	orig := probeExec
	t.Cleanup(func() { probeExec = orig })

	var gotBin string
	var gotArgs []string
	var remaining time.Duration
	var hasDeadline bool
	answer, answerErr := "  unix:///run/user/1/docker.sock\n", error(nil)
	probeExec = func(ctx context.Context, bin string, args []string) (string, error) {
		gotBin, gotArgs = bin, args
		var dl time.Time
		dl, hasDeadline = ctx.Deadline()
		remaining = time.Until(dl)
		return answer, answerErr
	}

	host, err := dockerContextHost(context.Background(), "/opt/docker-bin")
	require.NoError(t, err)
	assert.Equal(t, "unix:///run/user/1/docker.sock", host)
	assert.Equal(t, "/opt/docker-bin", gotBin)
	assert.Equal(t, []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"}, gotArgs)
	require.True(t, hasDeadline, "the lookup must carry its own deadline")
	assert.LessOrEqual(t, remaining, dockerContextTimeout)
	assert.Greater(t, remaining, dockerContextTimeout-500*time.Millisecond)
	assert.Equal(t, 10*time.Second, dockerContextTimeout)

	answerErr = errNoContext
	_, err = dockerContextHost(context.Background(), "docker")
	assert.ErrorIs(t, err, errNoContext)
}

// TestRootlessDaemonState_AFailedContextLookupMakesNoClaim: a context lookup
// that fails makes no claim even if it printed the standard endpoint before
// failing — a half-answer is not an endpoint.
func TestRootlessDaemonState_AFailedContextLookupMakesNoClaim(t *testing.T) {
	r := newWedgeRuntimeDir(t)
	prev := rootlessState
	t.Cleanup(func() { rootlessState = prev })
	rootlessState = rootlessStateProbe{
		getenv: func(k string) string { return map[string]string{"XDG_RUNTIME_DIR": r.dir}[k] },
		contextHost: func(context.Context, string) (string, error) {
			return r.endpoint(), errNoContext
		},
	}
	assert.NoError(t, rootlessDocker().daemonState(context.Background()))
}
