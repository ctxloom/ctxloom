//go:build !windows

package hostpty

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStart_ResizePropagatesToTheChildsReportedSize is the pty gate: a
// resize applied on the MASTER this package holds is what the child on the
// SLAVE reports as its own window size. Nothing proxies the size — the
// kernel carries it through the pty, which is the whole reason the runner is
// spawned on one instead of behind a stream. The child is a real process
// (`stty size` reads the tty it was given), so a size that only lived in a Go
// field would fail here.
func TestStart_ResizePropagatesToTheChildsReportedSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// read blocks until the harness has resized; stty then reports the size
	// the slave carries at that moment.
	s, err := Start(ctx, exec.Command("sh", "-c", "read go; stty size"))
	require.NoError(t, err)
	defer s.Kill()

	var out strings.Builder
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		for {
			n, rerr := s.Master().Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if rerr != nil {
				return
			}
		}
	}()

	require.NoError(t, s.Resize(uint16(37), uint16(111)))
	_, err = io.WriteString(s.Master(), "go\n")
	require.NoError(t, err)

	code, err := s.Wait()
	require.NoError(t, err)
	require.Equal(t, 0, code)
	<-done
	mu.Lock()
	got := out.String()
	mu.Unlock()
	require.Contains(t, got, "37 111", "the child must report the size the master was resized to")
}

// TestStart_ChildGetsTheSlaveAsItsControllingTerminal pins the shape the
// runner relies on: the child's stdin IS a tty, so an engine exec'd on the
// runner's inherited stdio renders as it would on the human's terminal.
func TestStart_ChildGetsTheSlaveAsItsControllingTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, exec.Command("sh", "-c", "[ -t 0 ] && [ -t 1 ] && echo ISATTY"))
	require.NoError(t, err)
	defer s.Kill()
	var out strings.Builder
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&out, s.Master())
	}()
	// The child's last bytes are read to EIO before Wait closes the master.
	<-drained
	code, err := s.Wait()
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Contains(t, out.String(), "ISATTY")
}

// TestStart_ExitedFiresBeforeTheMasterCloses pins the reap/close split the
// originator's drive relies on: once the child is gone, Exited is closed
// while the master is still open — its last bytes drain to EIO — and Wait
// is what closes it.
func TestStart_ExitedFiresBeforeTheMasterCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, exec.Command("sh", "-c", "echo LAST"))
	require.NoError(t, err)
	defer s.Kill()
	select {
	case <-s.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("the child was never reaped")
	}
	var out strings.Builder
	_, _ = io.Copy(&out, s.Master()) // EIO, after LAST
	require.Contains(t, out.String(), "LAST", "the master stays readable after the reap")
	code, err := s.Wait()
	require.NoError(t, err)
	require.Equal(t, 0, code)
}
