//go:build !windows

package testenv

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

// gatedDrain holds the drain's deliveries until settle starts waiting on its
// boundary, and is the hook pair that does it.
func gatedDrain(t *testing.T) ptyHooks {
	open := make(chan struct{})
	var release sync.Once
	opened := func() { release.Do(func() { close(open) }) }
	t.Cleanup(opened)
	return ptyHooks{
		drainTo:  func(w io.Writer) io.Writer { return gatedWriter{w: w, open: open} },
		settling: opened,
	}
}

// assertLastLineCaptured runs a process whose last line the drain holds when
// the reap is signalled, and asserts the reported exit came with that line —
// settled on a real boundary, not on ptySettleBound's backstop.
func assertLastLineCaptured(t *testing.T, hooks ptyHooks) {
	t.Helper()
	start := time.Now()
	s, err := startPTY(hooks, t.TempDir(), []string{"TERM=dumb", "PATH=" + os.Getenv("PATH")}, "/bin/sh", 80, 24, "-c", "echo LAST-LINE")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	exited, err := s.AwaitExit(t)
	if !exited || err != nil {
		t.Fatalf("AwaitExit = (%v, %v), want a clean exit; captured: %q", exited, err, s.Output())
	}
	if took := time.Since(start); took >= ptySettleBound {
		t.Fatalf("exit reported after %v: settle waited out its backstop instead of finding its boundary", took)
	}
	out := s.Output()
	if !strings.Contains(out, "LAST-LINE") {
		t.Fatalf("exit reported before the drain delivered the process's last line; captured: %q", out)
	}
	if strings.Contains(out, ptyFence) {
		t.Fatalf("the settle fence leaked into the capture: %q", out)
	}
}

// TestPTYSessionReportsExitOnlyOnceItsLastLineIsCaptured forces the
// interleaving behind the engine-death test's lost "never reported its
// outcome" line: the process writes its last line and exits, and the reap is
// signalled while the drain still holds that line. The drain is released only
// once settle waits on its boundary, so a session that reports the exit
// without settling reports it with the line still held — every time, not
// when the scheduler happens to lose the race.
func TestPTYSessionReportsExitOnlyOnceItsLastLineIsCaptured(t *testing.T) {
	assertLastLineCaptured(t, gatedDrain(t))
}

// TestPTYSessionSettlesWhenTheTerminalRefusesTheFence is the same guarantee
// where the fence cannot be written: darwin revokes a session leader's
// controlling terminal when it exits, so every open slave descriptor —
// the harness's own included — fails from then on. Closing the harness's
// slave after the reap leaves the same state on any platform: the fence
// write fails and no slave remains open, so the drain's end is the boundary.
func TestPTYSessionSettlesWhenTheTerminalRefusesTheFence(t *testing.T) {
	hooks := gatedDrain(t)
	hooks.beforeFence = func(slave *os.File) { _ = slave.Close() }
	assertLastLineCaptured(t, hooks)
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

// shPTY runs script under /bin/sh on a fresh pty.
func shPTY(t *testing.T, script string) *PTYSession {
	t.Helper()
	s, err := startPTY(ptyHooks{}, t.TempDir(), []string{"TERM=dumb", "PATH=" + os.Getenv("PATH")}, "/bin/sh", 100, 30, "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// screenWaitBudget is far beyond anything a fail-fast wait takes (a few
// milliseconds); a wait that spends it has failed slow, and says so.
const screenWaitBudget = 30 * time.Second

var engineMenu = Unexpected{Name: "an engine menu", Signature: "Select your AI engine"}

// TestWaitForScreenFailsFastNamingAnUnexpectedMenu: an interview that shows
// a menu its step never answers, then sits on it, fails the wait at once and
// names what it saw — with the whole budget still unspent, so a step's wait
// can never burn a deadline it shares with later scenarios.
func TestWaitForScreenFailsFastNamingAnUnexpectedMenu(t *testing.T) {
	s := shPTY(t, `printf 'Select your AI engine (press Enter for recommended):\n  1) claude-code\n  2) opencode\n> '; read x`)

	err := s.WaitForScreen(screenWaitBudget, `the question "personal repositories?"`,
		func(out string) bool { return strings.Contains(out, "personal ctxloom repositories?") }, engineMenu)
	if err == nil {
		t.Fatal("the wait succeeded on a screen that never asked")
	}
	for _, want := range []string{"an engine menu", "Select your AI engine", "2) opencode", "personal repositories?"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "never showed within") {
		t.Errorf("the wait spent its budget instead of failing on the menu:\n%v", err)
	}
}

// TestWaitForScreenFailsFastWhenTheProcessExitsFirst: a process that ends
// without ever showing the screen fails the wait on its exit, not the budget.
func TestWaitForScreenFailsFastWhenTheProcessExitsFirst(t *testing.T) {
	s := shPTY(t, `echo "Error: no AI engines installed"; exit 3`)

	err := s.WaitForScreen(screenWaitBudget, "the engine question", func(out string) bool { return strings.Contains(out, "engine?") })
	if err == nil || !strings.Contains(err.Error(), "exited (code 3) first") || !strings.Contains(err.Error(), "no AI engines installed") {
		t.Fatalf("want a failure naming the exit and carrying the output, got: %v", err)
	}
}

// TestWaitForScreenTakesTheScreenALastWriteShows: a process that prints the
// awaited screen and exits at once satisfies the wait; the exit is never
// mistaken for a miss.
func TestWaitForScreenTakesTheScreenALastWriteShows(t *testing.T) {
	s := shPTY(t, `echo "all done"`)

	if err := s.WaitForScreen(screenWaitBudget, `"all done"`, func(out string) bool { return strings.Contains(out, "all done") }); err != nil {
		t.Fatal(err)
	}
}

// TestWaitForExitUnlessFailsFastOnAPromptNobodyAnswers: an exit wait fails
// at once on a listed prompt, and reports a clean exit as one otherwise.
func TestWaitForExitUnlessFailsFastOnAPromptNobodyAnswers(t *testing.T) {
	stuck := shPTY(t, `printf 'Select your AI engine (press Enter for recommended):\n> '; read x`)
	if _, err := stuck.WaitForExitUnless(screenWaitBudget, engineMenu); err == nil || !strings.Contains(err.Error(), "an engine menu") {
		t.Fatalf("want a failure naming the engine menu, got: %v", err)
	}

	done := shPTY(t, `echo bye`)
	exitErr, err := done.WaitForExitUnless(screenWaitBudget, engineMenu)
	if err != nil || exitErr != nil {
		t.Fatalf("WaitForExitUnless = (%v, %v), want a clean exit", exitErr, err)
	}
}
