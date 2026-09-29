package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
	return func(ctx context.Context, spawnEnv map[string]string) (coord.OwnedRunner, error) {
		in, err := st.env.Interactive(ctx, isolation.RunnerRequest{Engine: st.backendName, Label: st.label, Env: st.runnerTerminalEnv(spawnEnv)})
		if err != nil {
			return coord.OwnedRunner{}, err
		}
		// The launch ctx scopes preparation and attach only; teardown has
		// one door (Kill), so the child must not die with the ctx.
		if in.Teardown == nil {
			s, err := hostpty.Start(context.Background(), in.Cmd)
			if err != nil {
				return coord.OwnedRunner{}, fmt.Errorf("start the runner on a pty: %w", err)
			}
			st.pty = s
			return coord.OwnedRunner{Kill: s.End, Wait: s.ExitErr}, nil
		}
		s, err := attach.Start(context.Background(), in.Cmd, in.Name, func(runExited <-chan struct{}) { teardownOnExit(in.Teardown, runExited) })
		if err != nil {
			return coord.OwnedRunner{}, fmt.Errorf("attach the container runner on a pty: %w", err)
		}
		st.pty = s
		return coord.OwnedRunner{Kill: s.End, Wait: s.ExitErr, ContainerName: in.Name}, nil
	}
}

// runnerTerminalEnv adds, to an interactive runner's env, where its
// diagnostics go while the terminal UI owns the terminal — the decision
// prepareSessionIO makes for this process's own (a real tty, and no
// --plain-terminal). The runner's stderr is the engine's pty, so this
// process's redirect cannot reach what the runner prints there. A container
// runner cannot open a host path and keeps its stderr.
func (st *runState) runnerTerminalEnv(spawnEnv map[string]string) map[string]string {
	if runPlainTerminal || st.activeHarp == "" || !termIsTerminal(int(os.Stdin.Fd())) {
		return spawnEnv
	}
	path, err := diagnosticsLogPath(st.activeHarp)
	if err != nil {
		return spawnEnv
	}
	env := maps.Clone(spawnEnv)
	if env == nil {
		env = map[string]string{}
	}
	env[sessions.EnvDiagnosticsLog] = path
	return env
}

// teardownOnExit runs an interactive runner's teardown with a ctx that is
// done once the run CLI has exited — the signal the teardown's wait for a
// not-yet-created runner ends on.
func teardownOnExit(teardown func(context.Context) error, runExited <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-runExited:
			cancel()
		case <-ctx.Done():
		}
	}()
	_ = teardown(ctx)
}

// driveOwnedInteractive drives an interactive owner run over the pty: the
// terminal seams (raw mode, the terminal layer) are pumped onto the master
// — keystrokes in, the engine's bytes out, each resize onto the pty — until
// the runner exits, and the run's outcome is read from the coordinator's
// own event stream (RunCompleted), which the runner emitted for the
// engine's exit before it exited itself.
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
	go func() {
		defer close(drained)
		// Ends with EIO once the slave's last holder exits: the kernel
		// delivers the runner's last bytes ahead of it, which is why the
		// master is drained BEFORE Wait closes it.
		_, _ = io.Copy(sio.stdout, master)
	}()
	select {
	case <-drained:
	case <-st.pty.Exited():
		// The runner was reaped; give the pty's tail its bound. A holder of
		// the slave that outlives the runner cannot hold this session open.
		select {
		case <-drained:
		case <-time.After(ptyDrainGrace):
		}
	}
	_, waitErr := st.pty.Wait()
	sio.restore()
	convertVendorTranscriptOnExit(st.activeHarp)
	if waitErr != nil {
		return fmt.Errorf("the runner ended: %w", waitErr)
	}
	return st.ownedRunOutcome()
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
