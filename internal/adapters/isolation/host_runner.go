package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/shared/procsig"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// HostRunner is a directly-launched host runner subprocess — the
// StartRunner host path. The process is `ctxloom <args…>` (`runner <engine>`)
// whose readiness the coordinator observes over its own RunnerChannel
// dial-home (awaitRunner), never here. Its stderr is captured into a bounded
// ring surfaced on Wait failure and via StderrTail — the diagnostics of a
// runner that dies before it dials home. Teardown reaps the whole session
// (killSession), so a grandchild the runner isolated into its own process
// group is swept up too.
type HostRunner struct {
	cmd    *exec.Cmd
	stderr *stderrtail.Ring
	pid    int
	// stop cancels the command's context, which is how Kill asks the runner
	// to end: exec runs cmd.Cancel (procsig.Stop) and, if the runner is still
	// there once WaitDelay has passed, SIGKILLs it.
	stop     context.CancelFunc
	killOnce sync.Once
	// reaped blocks for the ONE background Wait this runner's process gets.
	// A Start()ed process is only released from the process table by Wait,
	// and no production caller ever invoked RunnerHandle.Wait — so before
	// this every host runner spawn leaked a zombie exactly like the
	// container path did (see isolation.reapRunProcess for why a background
	// per-Cmd Wait, not a SIGCHLD reaper).
	reaped func() error
}

// hostRunnerWaitDelay is exec's WaitDelay for the runner, and so bounds two
// waits. After the runner exits, how long its stderr pipe may stay open: the
// runner puts itself in a fresh session, so a grandchild it spawned can hold
// that pipe open and wedge Wait — a wedged Wait is an unreaped child. After
// Kill asks it to stop, how long the runner gets to run its teardown before
// it is SIGKILLed.
const hostRunnerWaitDelay = 10 * time.Second

// StartHostRunner self-execs `ctxloom <args…>` under a fresh session (isolateRunner),
// stamping spawnEnv (the coordinator reach-back trio) onto the SUBPROCESS env
// only — never the process-global launcher env, racy across concurrent spawns.
// It returns after Start: the process is up but not yet dialed home (the
// coordinator's awaitRunner is the readiness barrier).
//
// args must name a subcommand. A bare self-exec is refused rather than started:
// `ctxloom` with no subcommand prints cobra's help and exits 0, so the caller
// would get a healthy-looking *HostRunner for a process that never dials home,
// and the failure would surface only as the coordinator's readiness timeout.
func StartHostRunner(args []string, spawnEnv map[string]string) (*HostRunner, error) {
	return startHostRunnerWithGrace(args, spawnEnv, hostRunnerWaitDelay)
}

func startHostRunnerWithGrace(args []string, spawnEnv map[string]string, grace time.Duration) (*HostRunner, error) {
	ctx, stop := context.WithCancel(context.Background())
	cmd, err := hostRunnerCmd(ctx, args, spawnEnv)
	if err != nil {
		stop()
		return nil, err
	}
	// Fresh session leader so killSession has a safe, scoped teardown boundary.
	isolateRunner(cmd)
	ring := stderrtail.New(stderrtail.DefaultBytes)
	cmd.Stderr = ring
	cmd.Cancel = func() error { return procsig.Stop(cmd.Process) }
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("start host runner: %w", err)
	}
	h := &HostRunner{cmd: cmd, stderr: ring, pid: cmd.Process.Pid, stop: stop}
	// Reap in the background, exactly once: Kill signals the process but only
	// Wait releases it, and nothing in production calls Wait.
	done := make(chan struct{})
	var waitErr error
	go func() {
		defer close(done)
		waitErr = cmd.Wait()
		stop()
	}()
	h.reaped = func() error {
		<-done
		return waitErr
	}
	return h, nil
}

// hostRunnerCmd is StartHostRunner's command: the running binary with args,
// spawnEnv laid over the process env, and the admitted companions first on
// PATH (withPinnedPath) so the engine this runner launches resolves a
// companion's bare name to the bytes admission verified.
func hostRunnerCmd(ctx context.Context, args []string, spawnEnv map[string]string) (*exec.Cmd, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("start host runner: no subcommand in args")
	}
	// Resolve the running binary upgrade-safely (selfexec strips a Linux
	// "(deleted)" suffix after an in-place upgrade), as RunnerCommand does.
	cmd := exec.CommandContext(ctx, selfexec.Path(), args...)
	cmd.Env = withPinnedPath(append(os.Environ(), envPairs(spawnEnv)...))
	return cmd, nil
}

// Kill stops the runner and reaps its whole session (killSession) —
// idempotent. It asks first (procsig.Stop) so the runner runs its own teardown
// — PaneHost.Stop, temp cleanup — and SIGKILLs it only if it is still there
// after hostRunnerWaitDelay; it returns once the runner has been reaped. The
// session sweep comes last, for whatever the runner's teardown left or a
// SIGKILL stranded: a grandchild in a separate process group.
func (h *HostRunner) Kill() {
	h.killOnce.Do(func() {
		h.stop()
		_ = h.reaped()
		killSession(h.pid)
	})
}

// Wait reaps the runner process. On a non-nil exit it wraps the stderr tail
// so a runner that dies pre-dial-home surfaces WHY instead of just "exit
// status N".
func (h *HostRunner) Wait() error {
	err := h.reaped()
	if err != nil {
		if tail := h.stderr.Tail(); tail != "" {
			return fmt.Errorf("host runner exited: %w (stderr tail: %s)", err, tail)
		}
		return fmt.Errorf("host runner exited: %w", err)
	}
	return nil
}

// StderrTail returns the captured stderr tail (bounded), for a caller that
// wants the diagnostic without waiting on exit.
func (h *HostRunner) StderrTail() string { return h.stderr.Tail() }
