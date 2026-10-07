package kit

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/internal/shared/procsig"
)

// Transport is the I/O seam for one per-turn engine process: a writable
// stdin, a readable stdout, and the process's two endings. Spawn makes the
// real one; tests build one over in-memory pipes so they never spawn
// anything.
//
// The turn's CONTEXT is the interrupt: when it ends, a spawned process is
// asked to stop (procsig.Interrupt) and killed only after its grace, so
// stdout reaches EOF either way and the driver reads the process's last
// words rather than cutting them off.
type Transport struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	// Teardown ends the process NOW and reaps it: the error paths' close.
	// nil: nothing to end.
	Teardown func() error
	// Reap reaps a process that is ending on its own (stdout reached EOF,
	// or the interrupt asked it to) and reports how it exited. nil:
	// nothing to reap, a clean exit.
	Reap func() error
	// ended records that the TURN ended the process (a Close, or a reap
	// that outran its grace and killed it), so its exit status is
	// ctxloom's doing, not the engine's.
	ended atomic.Bool
}

// Close tears the transport down (and unblocks a reader parked on stdout).
func (t *Transport) Close() error {
	t.ended.Store(true)
	if t.Teardown != nil {
		return t.Teardown()
	}
	return nil
}

// Wait reaps the ended process and reports its exit.
func (t *Transport) Wait() error {
	if t.Reap != nil {
		return t.Reap()
	}
	return nil
}

// TransportFunc opens one turn's process: binary with args, env laid over
// the process's own, in workDir.
type TransportFunc func(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*Transport, error)

// DefaultInterruptGrace is how long an interrupted turn's process gets to
// unwind before it is killed, and how long the driver keeps relaying its
// last words.
const DefaultInterruptGrace = 10 * time.Second

// Spawn launches the real process with piped stdio (NOT a pty) under
// DefaultInterruptGrace. stderr passes through for diagnostics.
func Spawn(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*Transport, error) {
	return spawnGrace(ctx, binary, args, env, workDir, DefaultInterruptGrace)
}

// spawnGrace is Spawn with the grace named. ctx ending interrupts the
// process (procsig.Interrupt, to the process group procsig.SpawnAttr made it
// lead) and exec kills it once grace has passed (WaitDelay): the kill fires
// whether or not anyone is waiting yet.
func spawnGrace(ctx context.Context, binary string, args []string, env map[string]string, workDir string, grace time.Duration) (*Transport, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = procsig.SpawnAttr()
	cmd.Cancel = func() error { return procsig.Interrupt(cmd.Process) }
	cmd.WaitDelay = grace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reap := sync.OnceValue(cmd.Wait)
	tr := &Transport{
		Stdin:  stdin,
		Stdout: stdout,
		Teardown: func() error {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			return reap()
		},
	}
	tr.Reap = func() error {
		_ = stdin.Close()
		killed, err := reapWithin(reap, grace, cmd.Process)
		if killed {
			tr.ended.Store(true)
		}
		return err
	}
	return tr, nil
}

// reapWithin reaps a process whose stdout has ended; one that has not exited
// within grace is killed (killed): its turn is over either way, and a wait
// that could hang would hold the turn open forever.
func reapWithin(reap func() error, grace time.Duration, p *os.Process) (killed bool, err error) {
	done := make(chan error, 1)
	go func() { done <- reap() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(grace):
		_ = p.Kill()
		return true, <-done
	}
}

// engineExit is the status the turn's process exited with ON ITS OWN
// (exitstatus.Of, the one computation every launch path shares), nil when
// there is none to report: the turn ended it (ctx's interrupt, a teardown, a
// reap past its grace), so a 130 or 137 would be ctxloom's signal read as the
// engine's failure; or the reap failed without an exit status.
func engineExit(ctx context.Context, tr *Transport, waitErr error) *int {
	if ctx.Err() != nil || tr.ended.Load() {
		return nil
	}
	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return nil
		}
		code = exitstatus.Of(ee)
	}
	return &code
}
