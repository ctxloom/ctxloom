//go:build !windows

package hostpty

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStart_InputReachesTheChildRawAndUnechoed: a mouse report written to the
// master arrives at the child without a newline behind it (non-canonical) and
// is never echoed back out the master — the echo is what painted `^[[<35;…M`
// into the engine's prompt. The child reads exactly the report's length, so a
// cooked pty holding input for a newline would time the test out instead.
func TestStart_InputReachesTheChildRawAndUnechoed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	report := "\x1b[<35;42;17M"
	s, err := Start(ctx, exec.Command("sh", "-c", "head -c "+strconv.Itoa(len(report))+" >/dev/null; echo done"))
	require.NoError(t, err)
	defer s.Kill()

	_, err = io.WriteString(s.Master(), report)
	require.NoError(t, err)
	out, _ := io.ReadAll(s.Master()) // ends with EIO once the child is gone
	code, err := s.Wait()
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Equal(t, "done\r\n", string(out), "only the child's own output, output processing intact")
}

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
// is what closes it. The master is read concurrently, as the drive reads it:
// on macOS the child's exit cannot complete until its output is drained.
func TestStart_ExitedFiresBeforeTheMasterCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, exec.Command("sh", "-c", "echo LAST"))
	require.NoError(t, err)
	defer s.Kill()
	out, drained := drain(s)
	select {
	case <-s.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("the child was never reaped")
	}
	_, err = s.master.Stat()
	require.NoError(t, err, "the reap leaves the master open")
	<-drained // EIO, after LAST
	require.Contains(t, out.String(), "LAST", "the master stays readable across the reap")
	code, err := s.Wait()
	require.NoError(t, err)
	require.Equal(t, 0, code)
	_, err = s.master.Stat()
	require.ErrorIs(t, err, os.ErrClosed, "Wait is what closes the master")
}

// drain reads s's master to EIO in the background, as the originator's drive
// does; out is safe to read once drained is closed.
func drain(s *Session) (out *strings.Builder, drained <-chan struct{}) {
	out = &strings.Builder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(out, s.Master())
	}()
	return out, done
}

// TestEnd_LeavesTheChildsLastBytesReadable forces the order an interactive
// run's teardown takes: the runner has written its last bytes to the slave,
// and the coordinator ends it (the run reported its exit) BEFORE the drive has
// read them. End is that coordinator-side handle, so it must end the child and
// leave the master to the reader; closing the master there discarded the
// runner's final output.
func TestEnd_LeavesTheChildsLastBytesReadable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	written := filepath.Join(t.TempDir(), "written")
	// A short grace: on macOS the SIGTERMed child cannot finish exiting while
	// its bytes are unread, so End returns on the grace's SIGKILL, not the reap.
	s, err := start(ctx, exec.Command("sh", "-c", "printf LAST-BYTES-9f3a; : > "+written+"; exec sleep 30"), 300*time.Millisecond)
	require.NoError(t, err)
	defer s.Kill()
	require.Eventually(t, func() bool { _, err := os.Stat(written); return err == nil }, 5*time.Second, 5*time.Millisecond)

	s.End()
	out, _ := io.ReadAll(s.Master()) // ends with EIO once the child is gone
	<-s.Exited()
	require.Contains(t, string(out), "LAST-BYTES-9f3a")
}

// TestExitErr_ReportsTheExitAndLeavesTheMasterOpen: ExitErr is the
// coordinator's "is the runner gone, and why" — it must name a non-zero
// status, answer every caller alike, and leave the child's last bytes on the
// master for the drive (Wait would have closed it and discarded them).
func TestExitErr_ReportsTheExitAndLeavesTheMasterOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, exec.Command("sh", "-c", "echo LAST; exit 3"))
	require.NoError(t, err)
	defer s.Kill()
	out, drained := drain(s)

	done := make(chan error, 1)
	go func() { done <- s.ExitErr() }()
	var first error
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExitErr never returned for a child that exited")
	}
	require.ErrorContains(t, first, "status 3")
	require.Equal(t, first.Error(), s.ExitErr().Error(), "every caller sees the same exit")

	_, err = s.master.Stat()
	require.NoError(t, err, "ExitErr must not close the master")
	<-drained
	require.Contains(t, out.String(), "LAST")
}
