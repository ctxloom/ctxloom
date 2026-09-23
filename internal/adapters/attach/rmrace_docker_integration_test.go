//go:build docker_integration

package attach_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// wrappedRuntime is a real runtime seen through
// dockergate.RemoveBeforeCreateWrapper: the interactive run is held until the
// teardown's first remove, which answers "No such container" and returns only
// once the daemon has registered the container.
type wrappedRuntime struct {
	isolation.Runtime
	bin string
}

func (r wrappedRuntime) Binary() string { return r.bin }

// startRaced starts an interactive container run on a pty the way the
// originator does (attach.Start with Container.Remove as its teardown by name),
// through the forcing wrapper, once per runtime present.
func forEachRaced(t *testing.T, what string, body func(t *testing.T, s *attach.Session, bin, name string)) {
	for _, r := range []struct {
		name      string
		available func() bool
	}{{"docker", isolation.Docker{}.Available}, {"podman", isolation.Podman{}.Available}} {
		t.Run(r.name, func(t *testing.T) {
			dockergate.RequireNamedRuntime(t, r.name, r.available(), what)
			real := isolation.ProbeRuntime(r.name)
			require.Equal(t, r.name, real.Name(), "the probe substituted another runtime")
			suffix := make([]byte, 4)
			_, _ = rand.Read(suffix)
			name := "ctxloom-attach-rmrace-" + hex.EncodeToString(suffix)
			t.Cleanup(func() { _ = exec.Command(real.Binary(), "rm", "-f", name).Run() })
			rt := wrappedRuntime{Runtime: real, bin: dockergate.RemoveBeforeCreateWrapper(t, real.Binary(), name)}
			spec := isolation.RunSpec{Image: "docker.io/library/alpine:latest", Name: name, Command: []string{"sleep", "300"}, TTY: true}
			pol := isolation.NewContainerFor(rt, "mock")
			s, err := attach.Start(context.Background(), exec.Command(rt.Binary(), rt.RunArgs(spec)...), name, func() { pol.Remove(name) })
			require.NoError(t, err)
			t.Cleanup(s.Kill)
			body(t, s, real.Binary(), name)
		})
	}
}

// TestKill_BeforeCreateLeavesNoContainer: a launch that fails before the
// runner dials home is torn down with Session.Kill (the originator's
// teardownTransport) — possibly before the daemon has created the container.
// Kill's remove then answers "No such container"; if it trusts that and ends
// the CLI, the create lands behind it and the container is orphaned.
func TestKill_BeforeCreateLeavesNoContainer(t *testing.T) {
	forEachRaced(t, "the interactive kill-before-create race test", func(t *testing.T, s *attach.Session, bin, name string) {
		s.Kill()
		_, _ = s.Wait()
		require.Empty(t, dockergate.ContainersNamed(t, bin, name),
			"Kill must not orphan a container whose create landed after its remove said \"No such container\"")
	})
}

// TestEnd_BeforeCreateLeavesNoContainer: End is the teardown the coordinator
// holds (the run record's close), fired by a launch that fails before attach.
// It removes by name and leaves the CLI to finish relaying; if the remove came
// before the create, the CLI goes on to create and start the container, and
// End's relay bound then ends the CLI with the container still running.
func TestEnd_BeforeCreateLeavesNoContainer(t *testing.T) {
	forEachRaced(t, "the interactive end-before-create race test", func(t *testing.T, s *attach.Session, bin, name string) {
		s.End()
		select {
		case <-s.Exited():
		case <-time.After(30 * time.Second):
			t.Fatal("the run CLI outlived End's relay bound")
		}
		require.Empty(t, dockergate.ContainersNamed(t, bin, name),
			"End must not leave running a container whose create landed after its remove said \"No such container\"")
	})
}
