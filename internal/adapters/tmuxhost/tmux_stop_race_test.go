//go:build !windows

// tmux hosting is POSIX-only (see findTmux's Windows refusal).

package tmuxhost

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// TestHost_StopArmRaceUnderAlreadyCancelledContext targets the exact race the
// detector caught in production (fixed by 965a2b061):
//
//	write  tmuxhost.(*Terminals).host              tmux_host.go:134
//	read   tmuxhost.(*Terminals).releaseWindow     tmux_terminal.go:506
//	       via host.func1, a context.AfterFunc callback
//
// context.AfterFunc runs its callback IMMEDIATELY, in a NEW goroutine, when
// ctx is ALREADY cancelled at registration time -- so cancelling ctx BEFORE
// calling host (rather than racing a real cancel against it) puts every trial
// squarely in the failure window, instead of relying on timing that fired
// incidentally less than 1 in 6 full-package runs.
//
// The window itself is a handful of instructions (AfterFunc returning ->
// the assignment of its result completing), and Go's scheduler normally keeps
// a freshly spawned goroutine on the CREATING P's local run queue rather than
// migrating it to an idle P immediately -- so a single, isolated trial rarely
// wins the race even though the defect is real. What made it fire in
// production was scheduling churn from a whole test binary's worth of other
// goroutines. This test reproduces that churn directly: it fires many
// independent host() trials at once, so the scheduler has far more runnable
// goroutines than P's and is forced into exactly the cross-P handoffs that
// expose the window.
//
// This test proves nothing on its own: it only becomes evidence when run
// under `-race`, once with 965a2b061's guard reverted (must fail,
// reproducibly, across repeated invocations) and once restored (must stay
// clean across the same number of invocations).
func TestHost_StopArmRaceUnderAlreadyCancelledContext(t *testing.T) {
	const trials = 3000
	tmpDir := t.TempDir() // shared: each trial's launcher files carry its own window name

	type trial struct {
		runner *fakeTmuxRunner
		h      *tmuxTerminal
	}
	started := make([]trial, trials)

	// Phase 1: every host() call runs concurrently, so each AfterFunc callback
	// still fires inside the scheduling churn that exposes the race window.
	var wg sync.WaitGroup
	wg.Add(trials)
	for i := 0; i < trials; i++ {
		go func() {
			defer wg.Done()

			// Each trial gets its OWN Terminals/runner so one trial's
			// bookkeeping locks can never themselves order another trial's
			// racing accesses and mask the very thing under test.
			runner := newFakeTmuxRunner()
			l := New(runner, tmpDir)

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // cancelled BEFORE host registers the AfterFunc trigger

			h, err := l.host(ctx, hostSpec{Command: "true"})
			if err != nil {
				t.Errorf("host: %v", err)
				return
			}
			started[i] = trial{runner, h}
		}()
	}
	wg.Wait()

	// Phase 2: the callback's first act is disarmStop, and it then issues
	// kill-window on its trial's window -- so observing that call is proof
	// disarmStop ran, i.e. the trial exercised the race window. The deadline
	// starts only once EVERY host() has returned: a per-trial clock started
	// at that trial's own return charges it for its siblings' setup I/O, and
	// on a starved single-CPU runner that alone exceeds the bound.
	deadline, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	for _, tr := range started {
		if tr.h == nil {
			continue // host failed; already reported
		}
		select {
		case <-tr.runner.seen("kill-window"):
			args := tr.runner.argsFor("kill-window")
			if !slices.Contains(args, tr.h.window) {
				t.Errorf("kill-window missing window %s: %v", tr.h.window, args)
			}
		case <-deadline.Done():
			t.Errorf("releaseWindow never reached kill-window for %s; the AfterFunc callback did not run", tr.h.window)
		}
	}
}
