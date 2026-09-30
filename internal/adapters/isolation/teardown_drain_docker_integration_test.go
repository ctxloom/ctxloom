//go:build docker_integration

package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// payloadBytes exceeds what the ORIGINATOR's pty can hold unread (the line
// discipline's buffer plus the tty flip buffers), so while nobody reads the
// master the tail of the payload cannot have left the relay — the run CLI
// and the daemon's attach stream — when End runs. It is small enough that the
// container's own tty and the relay absorb it, so the in-container write
// completes and the runner reaches its "exit reported" signal.
const payloadBytes = 96 * 1024

// TestEnd_TheContainersLastBytesSurviveTheRelay is the container twin of
// hostpty's TestEnd_LeavesTheChildsLastBytesReadable. On the container path
// the pty's child is not the runner but the runtime CLI relaying the
// container's tty through the daemon, so "the runner has written its bytes"
// does not mean they are in the master yet. The coordinator ends the run the
// moment the runner reports its exit (End), possibly before the drive has
// read anything: every byte the runner wrote must still reach the master.
//
// The race is FORCED, not waited for: the master is not read until End has
// returned, and the payload is larger than the originator pty can buffer, so
// part of it is necessarily still inside the relay when End runs.
func TestEnd_TheContainersLastBytesSurviveTheRelay(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container attach End drain test")
	rt := ProbeRuntime("docker")

	// The image is made present before the measured run: under a TTY the CLI's
	// own stderr shares the master with the container's output, so a pull at
	// run time would put its progress text into the stream being counted.
	const image = "alpine:latest"
	if exec.Command(rt.Binary(), "image", "inspect", image).Run() != nil {
		out, err := exec.Command(rt.Binary(), "pull", image).CombinedOutput()
		require.NoError(t, err, "pull %s before the measured run: %s", image, out)
	}

	sig := t.TempDir()
	written := filepath.Join(sig, "written")
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "ctxloom-attach-drain-" + hex.EncodeToString(suffix)
	const endMark = "LAST-BYTES-c47e"

	// The runner stand-in: write the payload and a terminal mark to the tty,
	// report the exit (the marker file — the runner's RunCompleted), exit.
	script := fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x; printf %s; : > /sig/written", payloadBytes, endMark)
	spec := RunSpec{
		Image:   image,
		Name:    name,
		Command: []string{"sh", "-c", script},
		Mounts:  []mount{{Host: sig, Container: "/sig"}},
		TTY:     true,
	}
	pol := NewContainerFor(rt, "mock")
	s, err := attach.Start(context.Background(), exec.Command(rt.Binary(), mustRunArgs(t, rt, spec)...), name, removeOnExit(pol, name))
	require.NoError(t, err)
	t.Cleanup(func() { s.Kill(); _ = exec.Command(rt.Binary(), "rm", "-f", name).Run() })

	require.Eventually(t, func() bool { _, err := os.Stat(written); return err == nil }, 60*time.Second, 5*time.Millisecond,
		"the container never reported its exit")

	s.End()

	type result struct {
		out []byte
	}
	read := make(chan result, 1)
	go func() {
		out, _ := io.ReadAll(s.Master()) // ends with EIO once the CLI is gone
		read <- result{out}
	}()
	var out []byte
	select {
	case r := <-read:
		out = r.out
	case <-time.After(60 * time.Second):
		s.Kill()
		t.Fatal("the master never reached EIO after End: the relay held the pty open")
	}
	got := string(out)
	assert.Equal(t, payloadBytes, strings.Count(got, "x"), "every payload byte the runner wrote reaches the master after End (read %d bytes)", len(out))
	assert.True(t, strings.Contains(got, endMark), "the runner's final bytes (%q) reach the master after End", endMark)
}
