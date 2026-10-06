//go:build docker_integration

// Package daemonfixture places a docker-gated test's fixtures where the
// container daemon can name them, chosen through this process's own layer
// (isolation.DaemonLayer) — the layer production reverses every bind source
// through. Where this process shares nothing with the daemon at the same path
// (a CI job container with a private /tmp), a fixture under the process temp
// dir would reach a container as a blank directory the daemon created, never
// as an error.
//
// isolation's own tests cannot import this package (it imports isolation),
// so they call the isolation accessors directly (daemonFixtures there).
package daemonfixture

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// Require gates t on a reachable docker daemon (dockergate.RequireRuntime),
// then roots t's fixtures where it can name them (Root). It is a docker test's
// first call: a temp dir created before it is not under the root.
func Require(t testing.TB, what string) isolation.Layer {
	t.Helper()
	dockergate.RequireRuntime(t, isolation.Docker{}.Available(), what)
	return Root(t, "docker")
}

// Root points TMPDIR, for t, at runtime's daemon's fixture root
// (isolation.FixtureRoot over dockergate.FixtureCandidates), so t.TempDir,
// os.MkdirTemp and every helper over them create sources the daemon can name,
// and returns the layer a test reverses a source through when it hands the
// daemon one itself. A process
// the daemon cannot place fails; a layer with no fixture root skips, as an
// environment capability.
func Root(t testing.TB, runtime string) isolation.Layer {
	t.Helper()
	l, err := isolation.DaemonLayer(runtime)
	if err != nil {
		t.Fatalf("this process's layer on the %s daemon: %v", runtime, err)
	}
	root, err := isolation.FixtureRoot(l, dockergate.FixtureCandidates()...)
	if err != nil {
		dockergate.SkipCapability(t, "no directory this process writes is one the "+runtime+" daemon can name: "+err.Error())
	}
	if root != os.TempDir() {
		t.Setenv("TMPDIR", root)
	}
	return l
}

// Source is view in the daemon's path space: the bind source a test hands
// the daemon for a fixture it mounts itself.
func Source(t testing.TB, l isolation.Layer, view string) string {
	t.Helper()
	host, err := l.Reverse(view)
	if err != nil {
		t.Fatalf("bind source %s: %v", view, err)
	}
	return host
}
