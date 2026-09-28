//go:build !windows

// tmux hosting is POSIX-only (see findTmux's Windows refusal).

package tmuxhost

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCreate_StopArmRaceUnderAlreadyCancelledContext is the sibling of
// TestHost_StopArmRaceUnderAlreadyCancelledContext, aimed at the SECOND
// armStop(context.AfterFunc(...)) registration site.
//
//	tmux_terminal.go:412   Create
//	tmux_host.go:134       host      (covered by the sibling test)
//
// Both sites have the identical shape and are guarded by the same mutex, so
// 965a2b061's fix either holds for both or neither. That symmetry is an
// ARGUMENT, not evidence: the host site was demonstrated 20/20 under -race with
// the guard reverted, while this one rested on inspection alone. A guard that
// is correct at one call site and merely assumed at the other is exactly the
// gap this project treats as untested.
//
// Method, and why the obvious approach does not work: context.AfterFunc runs
// its callback IMMEDIATELY, in a NEW goroutine, when ctx is ALREADY cancelled
// at registration time -- so cancelling BEFORE calling Create puts every trial
// in the failure window rather than hoping to hit it. Even then a single
// isolated trial rarely loses the race, because Go keeps a freshly spawned
// goroutine on the CREATING P's run queue instead of migrating it. What made
// the defect fire in production was scheduling churn from a whole binary's
// worth of goroutines, so this fires many independent trials at once to force
// the cross-P handoffs that expose the window.
//
// Like its sibling, this proves nothing on its own. It is evidence only under
// `-race`, run once with the armStop/disarmStop guard reverted (must fail,
// reproducibly, across separate process invocations -- the race detector
// deduplicates identical signatures WITHIN a process, so -count reruns show one
// report however often it fires) and once restored (must stay clean).
func TestCreate_StopArmRaceUnderAlreadyCancelledContext(t *testing.T) {
	const trials = 2000
	tmpDir := t.TempDir() // shared: no trial's fakeTmuxRunner touches the filesystem

	var wg sync.WaitGroup
	wg.Add(trials)
	for i := 0; i < trials; i++ {
		go func() {
			defer wg.Done()

			// Each trial gets its OWN Terminals/runner, so one trial's
			// bookkeeping locks can never order another trial's racing
			// accesses and mask the thing under test.
			runner := newFakeTmuxRunner()
			l := New(runner, tmpDir)

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // cancelled BEFORE Create registers the AfterFunc trigger

			id, err := l.Create(ctx, Spec{Command: "true"})
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}

			// The AfterFunc callback (Release -> releaseWindow) fires in its
			// own goroutine the moment Create registers it, concurrently with
			// Create's remaining bookkeeping. Its first act inside
			// releaseWindow is disarmStop, and it then issues kill-window for
			// this trial's window -- so observing that call is proof the
			// callback actually ran, i.e. that this trial exercised the race
			// window rather than racing this goroutine's own return.
			deadline := time.Now().Add(2 * time.Second)
			for {
				if args := runner.argsFor("kill-window"); len(args) > 0 {
					// tmux targets the window as "<session>:<window>", so the
					// id is the suffix rather than the whole argument.
					for _, a := range args {
						if a == string(id) || strings.HasSuffix(a, ":"+string(id)) {
							return
						}
					}
					t.Errorf("kill-window missing window %s: %v", id, args)
					return
				}
				if time.Now().After(deadline) {
					t.Errorf("Release never reached kill-window for %s; the AfterFunc callback did not run", id)
					return
				}
				time.Sleep(50 * time.Microsecond)
			}
		}()
	}
	wg.Wait()
}
