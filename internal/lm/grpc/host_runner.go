package grpc

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// hostRunnerStderrTailBytes bounds the stderr ring buffer a directly-launched
// host runner keeps for its failure diagnostics — enough to carry a panic
// trace or a config error, small enough to never grow unbounded across a long
// run.
const hostRunnerStderrTailBytes = stderrtail.DefaultBytes

// HostRunner is a directly-launched, BARE (non-go-plugin) host runner
// subprocess — the StartRunner host path. Unlike NewSelfInvokingClient* it
// completes NO go-plugin handshake: the process is `ctxloom <args…>` (e.g.
// `llm host <backend>`) whose readiness the coordinator observes over its own
// RunnerChannel dial-home (awaitRunner), never here. Its stderr is captured
// into a bounded ring surfaced on Wait failure and via StderrTail — the
// diagnostics that replace go-plugin's Diagnose on this path. Teardown reaps
// the whole session (killSession), exactly as the go-plugin host path's Kill
// does (realLLMConnection.Kill), so a grandchild the runner isolated into its
// own process group is swept up too.
type HostRunner struct {
	cmd      *exec.Cmd
	stderr   *stderrtail.Ring
	pid      int
	killOnce sync.Once
	// reaped blocks for the ONE background Wait this runner's process gets.
	// A Start()ed process is only released from the process table by Wait,
	// and no production caller ever invoked RunnerHandle.Wait — so before
	// this every host runner spawn leaked a zombie exactly like the
	// container path did (see isolation.reapRunProcess for why a background
	// per-Cmd Wait, not a SIGCHLD reaper).
	reaped func() error
}

// hostRunnerWaitDelay bounds the gap between the runner process exiting and
// its stderr pipe closing: the runner puts itself in a fresh session, so a
// grandchild it spawned can hold that pipe open and wedge Wait — a wedged
// Wait is an unreaped child.
const hostRunnerWaitDelay = 10 * time.Second

// StartHostRunner self-execs `ctxloom <args…>` under a fresh session (isolateRunner),
// stamping spawnEnv (the coordinator reach-back trio) onto the SUBPROCESS env
// only — never the process-global launcher env, racy across concurrent spawns.
// It returns after Start: the process is up but not yet dialed home (the
// coordinator's awaitRunner is the readiness barrier).
func StartHostRunner(args []string, spawnEnv map[string]string) (*HostRunner, error) {
	// Resolve the running binary upgrade-safely (selfexec strips a Linux
	// "(deleted)" suffix after an in-place upgrade), matching the self-invoke
	// path (NewSelfInvokingClientForLabelEnv).
	cmd := exec.Command(selfexec.Path(), args...)
	if len(spawnEnv) > 0 {
		kv := make([]string, 0, len(spawnEnv))
		for k, v := range spawnEnv {
			kv = append(kv, k+"="+v)
		}
		sort.Strings(kv)
		cmd.Env = append(os.Environ(), kv...)
	}
	// Fresh session leader so killSession has a safe, scoped teardown boundary
	// (the same reason dialLLMConnection sets it — see realLLMConnection.Kill).
	isolateRunner(cmd)
	ring := stderrtail.New(hostRunnerStderrTailBytes)
	cmd.Stderr = ring
	cmd.WaitDelay = hostRunnerWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start host runner: %w", err)
	}
	h := &HostRunner{cmd: cmd, stderr: ring, pid: cmd.Process.Pid}
	// Reap in the background, exactly once: Kill signals the process but only
	// Wait releases it, and nothing in production calls Wait.
	done := make(chan struct{})
	var waitErr error
	go func() {
		defer close(done)
		waitErr = cmd.Wait()
	}()
	h.reaped = func() error {
		<-done
		return waitErr
	}
	return h, nil
}

// Kill terminates the runner and reaps its whole session (killSession) —
// idempotent. Mirrors the go-plugin host path's teardown: a raw process kill
// reaches only the runner's own pid, so the session sweep is what catches a
// grandchild in a separate process group.
func (h *HostRunner) Kill() {
	h.killOnce.Do(func() {
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		killSession(h.pid)
	})
}

// Wait reaps the runner process. On a non-nil exit it wraps the stderr tail
// (the go-plugin Diagnose replacement) so a runner that dies pre-dial-home
// surfaces WHY instead of just "exit status N".
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
