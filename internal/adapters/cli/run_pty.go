package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// runnerTermValue is the TERM the RUNNER process runs under on the pty:
// `dumb`, deliberately. The runner is `ctxloom` on the interactive terminal,
// and ctxloom's package-init terminal-capability detection (lipgloss/termenv
// querying the background via OSC 11 + a DSR terminator) would otherwise fire
// and READ the response from this same stdin, swallowing the human's first
// keystrokes. `dumb` makes termenv skip the query entirely. The ENGINE keeps
// real color: its env is the runner's overridden by the launch's engine env,
// into which the originator stamps the terminal it is actually watching
// (stampTerminalEnv).
const runnerTermValue = "dumb"

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
// runner process runs under runnerTermValue.
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

// ptyStarter is the owner run's starter for an INTERACTIVE host launch: the
// runner (`ctxloom runner <engine>`) is started on a pseudo-terminal whose
// MASTER this process keeps (adapters/hostpty) — the terminal layer wraps
// the master, keystrokes and resizes reach the engine through the pty the
// kernel carries. The reach-back trio rides the runner's process env. The
// session is recorded on the state for the drive; its Kill is the run's
// teardown handle.
func (st *runState) ptyStarter() coord.OwnedRunStarter {
	return func(_ context.Context, spawnEnv map[string]string) (func(), string, error) {
		cmd := exec.Command(selfexec.Path(), "runner", st.backendName)
		env := append([]string(nil), os.Environ()...)
		env = append(env, "TERM="+runnerTermValue)
		kv := make([]string, 0, len(spawnEnv))
		for k, v := range spawnEnv {
			kv = append(kv, k+"="+v)
		}
		sort.Strings(kv)
		cmd.Env = append(env, kv...)
		// The launch ctx scopes preparation and attach only; teardown has
		// one door (Kill), so the child must not die with the ctx.
		s, err := hostpty.Start(context.Background(), cmd)
		if err != nil {
			return nil, "", fmt.Errorf("start the runner on a pty: %w", err)
		}
		st.pty = s
		return s.Kill, "", nil
	}
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
					_ = st.pty.Resize(uint16(ws.GetRows()), uint16(ws.GetCols()))
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
