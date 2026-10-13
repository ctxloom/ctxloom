//go:build !windows

package testenv

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// gatedWriter holds every write until open closes: the drain has read the
// process's bytes off the master but not yet delivered them to the capture.
type gatedWriter struct {
	w    io.Writer
	open <-chan struct{}
}

func (g gatedWriter) Write(p []byte) (int, error) {
	<-g.open
	return g.w.Write(p)
}

// TestPTYSessionReportsExitOnlyOnceItsLastLineIsCaptured forces the
// interleaving behind the engine-death test's lost "never reported its
// outcome" line: the process writes its last line and exits, and the reap is
// signalled while the drain still holds that line. The drain is released only
// by the settle fence, so a session that reports the exit without settling
// reports it with the line still held — every time, not when the scheduler
// happens to lose the race.
func TestPTYSessionReportsExitOnlyOnceItsLastLineIsCaptured(t *testing.T) {
	open := make(chan struct{})
	var release sync.Once
	opened := func() { release.Do(func() { close(open) }) }
	t.Cleanup(opened)

	s, err := startPTY(ptyHooks{
		drainTo:      func(w io.Writer) io.Writer { return gatedWriter{w: w, open: open} },
		fenceWritten: opened,
	}, t.TempDir(), []string{"TERM=dumb", "PATH=" + os.Getenv("PATH")}, "/bin/sh", 80, 24, "-c", "echo LAST-LINE")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	exited, err := s.AwaitExit(t)
	if !exited || err != nil {
		t.Fatalf("AwaitExit = (%v, %v), want a clean exit; captured: %q", exited, err, s.Output())
	}
	out := s.Output()
	if !strings.Contains(out, "LAST-LINE") {
		t.Fatalf("exit reported before the drain delivered the process's last line; captured: %q", out)
	}
	if strings.Contains(out, ptyFence) {
		t.Fatalf("the settle fence leaked into the capture: %q", out)
	}
}

// TestPtyCaptureCutsAFenceSplitAcrossWrites: the fence is reported and cut
// even when the drain delivers it in pieces, and what surrounds it is kept.
func TestPtyCaptureCutsAFenceSplitAcrossWrites(t *testing.T) {
	c := &ptyCapture{}
	seen := c.expect(ptyFence)
	half := len(ptyFence) / 2
	_, _ = c.Write([]byte("before" + ptyFence[:half]))
	select {
	case <-seen:
		t.Fatal("fence reported from half of it")
	default:
	}
	_, _ = c.Write([]byte(ptyFence[half:] + "after"))
	select {
	case <-seen:
	default:
		t.Fatal("fence not reported once whole")
	}
	if got := c.String(); got != "beforeafter" {
		t.Fatalf("capture = %q, want %q", got, "beforeafter")
	}
}
