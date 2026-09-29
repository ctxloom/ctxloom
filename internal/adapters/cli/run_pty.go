package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// runnerTTY is the interactive runner's terminal as the drive pumps it:
// the pty master this process holds around the runner — a host process
// (adapters/hostpty) or the container runtime's attached run
// (adapters/attach) — with the exit and the teardown behind it.
type runnerTTY interface {
	Master() io.ReadWriter
	Resize(rows, cols uint16) error
	Exited() <-chan struct{}
	Wait() (int, error)
	// End ends the runner and leaves the master to the drive; Kill also
	// releases it.
	End()
	Kill()
}

// ptyDrainGrace bounds the drain of the pty master after the runner was
// reaped: its last bytes are already in the pty and arrive at once; only a
// slave holder that outlived the runner keeps the read open past this.
const ptyDrainGrace = time.Second

// runnerExitAfterDrain bounds how long a runner may outlive the end of its
// drain before the drive ends it. The drain normally ends with EIO the
// moment the slave's last holder exits, a hair before the reap; a drain that
// ended under a runner still alive past this means the output side failed
// (the terminal's writes error after a hangup), and nothing can render that
// runner any more.
const runnerExitAfterDrain = time.Second

// runnerEndGrace bounds how long End may take to end the runner before the
// drive escalates to Kill: End is asynchronous for a container relay, and a
// wedged relay must not hold a signalled session open. A var so a test can
// shorten it.
var runnerEndGrace = 5 * time.Second

// errSessionOutputLost reports an interactive session whose runner was ended
// because its output could no longer be delivered to the terminal.
var errSessionOutputLost = errors.New("the session's terminal stopped accepting output, so its runner was ended")

// ownerRunCompletionWait bounds the wait for the run's terminal event after
// the runner process has exited: the runner emits RunCompleted before it
// reports its exit, and the in-process watch delivers it in the same order,
// so the event is already in flight when the pty closes.
const ownerRunCompletionWait = 5 * time.Second

// stampTerminalEnv copies the originator's TERM/COLORTERM into the launch's
// engine env (never clobbering a value the launch already carries), so the
// engine renders in the terminal the human is watching even though the
// runner process runs under isolation.RunnerTerm.
func stampTerminalEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		out[k] = v
	}
	for _, k := range []string{"TERM", "COLORTERM"} {
		if v := os.Getenv(k); v != "" {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
	}
	return out
}

// ptyStarter is the owner run's starter for an INTERACTIVE launch: the
// runner — `ctxloom runner <engine>` self-exec'd on a host cell, `docker
// run -i -t … ctxloom runner <engine>` as a container's foreground process —
// is started on a pseudo-terminal whose MASTER this process keeps, so the
// terminal layer wraps one master wherever the runner runs and keystrokes,
// the engine's bytes and resizes cross the pty the kernel (and, for a
// container, the daemon's tty) carries. The reach-back trio rides the
// runner's process env. The session is recorded on the state for the drive.
// The coordinator is handed End (the container removed by name first), not
// Kill: it ends the run when the runner reports its exit, which is BEFORE the
// drive has necessarily read the runner's last bytes, and closing the master
// there discards them. The drive's own teardown releases the master.
func (st *runState) ptyStarter() coord.OwnedRunStarter {
	return func(ctx context.Context, spawnEnv map[string]string) (func(), string, error) {
		cmd, name, err := st.policy.InteractiveRunner(ctx, st.backendName, st.ws, spawnEnv)
		if err != nil {
			return nil, "", err
		}
		// The launch ctx scopes preparation and attach only; teardown has
		// one door (Kill), so the child must not die with the ctx.
		if name == "" {
			s, err := hostpty.Start(context.Background(), cmd)
			if err != nil {
				return nil, "", fmt.Errorf("start the runner on a pty: %w", err)
			}
			st.pty = s
			return s.End, "", nil
		}
		container, ok := st.policy.(interface {
			Remove(name string, runExited <-chan struct{})
		})
		if !ok {
			return nil, "", fmt.Errorf("policy %q names container %q but cannot remove one", st.policy.Name(), name)
		}
		s, err := attach.Start(context.Background(), cmd, name, func(runExited <-chan struct{}) { container.Remove(name, runExited) })
		if err != nil {
			return nil, "", fmt.Errorf("attach the container runner on a pty: %w", err)
		}
		st.pty = s
		return s.End, name, nil
	}
}

// driveOwnedInteractive drives an interactive owner run over the pty: the
// terminal seams (raw mode, the terminal layer) are pumped onto the master
// — keystrokes in, the engine's bytes out, each resize onto the pty — until
// the runner exits, and the run's outcome is read from the coordinator's
// own event stream (RunCompleted), which the runner emitted for the
// engine's exit before it exited itself. A shutdown signal (the run's ctx)
// or a terminal that stops accepting output ends the runner instead, and
// the drive returns that cause: signal.NotifyContext has taken over those
// signals' default action, so this drive observing them is the only way the
// process ends on one.
func (st *runState) driveOwnedInteractive() error {
	sio := st.prepareSessionIO()
	defer sio.restore()
	master := st.pty.Master()

	pumpCtx, stopPumps := context.WithCancel(st.ctx)
	defer stopPumps()
	if sio.stdin != nil {
		go func() { _, _ = io.Copy(master, sio.stdin) }()
	}
	if sio.resize != nil {
		go func() {
			for {
				select {
				case ws, ok := <-sio.resize:
					if !ok {
						return
					}
					_ = st.pty.Resize(ws.Rows, ws.Cols)
				case <-pumpCtx.Done():
					return
				}
			}
		}()
	}
	drained := make(chan struct{})
	var drainErr error
	go func() {
		defer close(drained)
		// Ends with EIO once the slave's last holder exits: the kernel
		// delivers the runner's last bytes ahead of it, which is why the
		// master is drained BEFORE Wait closes it.
		_, drainErr = io.Copy(sio.stdout, master)
	}()
	// Whichever ends this wait, the runner is ended before Wait: Wait blocks
	// until the runner is reaped, so reaching it with a live runner parks the
	// process for as long as that runner lives — with the shutdown signals
	// already absorbed by the run's ctx, which is how a closed terminal left
	// a coordinator owning its project indefinitely.
	var interrupted error
	select {
	case <-drained:
		if st.endRunnerAfter(runnerExitAfterDrain) {
			interrupted = fmt.Errorf("%w: %v", errSessionOutputLost, drainErr)
		}
	case <-st.pty.Exited():
	case <-st.ctx.Done():
		interrupted = fmt.Errorf("the session was signalled to end: %w", st.ctx.Err())
		st.endRunnerAfter(0)
	}
	// The runner is gone; give the pty's tail its bound. A holder of the
	// slave that outlives the runner cannot hold this session open.
	select {
	case <-drained:
	case <-time.After(ptyDrainGrace):
	}
	_, waitErr := st.pty.Wait()
	sio.restore()
	convertVendorTranscriptOnExit(st.activeHarp)
	if interrupted != nil {
		return interrupted
	}
	if waitErr != nil {
		return fmt.Errorf("the runner ended: %w", waitErr)
	}
	return st.ownedRunOutcome()
}

// endRunnerAfter ends a runner that has not exited within grace: End first,
// which leaves the master to the drain for the runner's last bytes, then Kill
// once runnerEndGrace passes without an exit. It returns once the runner has
// exited or been Killed, so the Wait that follows is bounded, and reports
// whether the runner had to be ended rather than exiting on its own.
func (st *runState) endRunnerAfter(grace time.Duration) bool {
	select {
	case <-st.pty.Exited():
		return false
	case <-time.After(grace):
	}
	st.pty.End()
	select {
	case <-st.pty.Exited():
	case <-time.After(runnerEndGrace):
		st.pty.Kill()
	}
	return true
}

// ownedRunOutcome reads the interactive run's terminal from the coordinator's
// event stream once the runner has exited: SUCCEEDED is a clean exit, anything
// else the engine's failure. A run whose terminal never arrives is reported
// as such rather than assumed green.
func (st *runState) ownedRunOutcome() error {
	ctx, cancel := context.WithTimeout(context.Background(), ownerRunCompletionWait)
	defer cancel()
	_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, st.ownedRun.outcome.RunID, st.ownedRun.events, make(chan string, 1), false)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("the runner exited but the run %s never reported its outcome", st.ownedRun.outcome.RunID)
	}
	return err
}
