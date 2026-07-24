// Package dockerexec is the docker-exec implementation of the
// VIRTUALIZED-PROCESS-IO seam (internal/vpio): it drives an interactive agent
// turn over `docker|podman exec -it` into an ALREADY-RUNNING container, for the
// container-isolation runtime — the swap registered as future work at
// internal/vpio/vpio.go:27-28 and internal/vpio/goplugin/goplugin.go:6-11.
//
// It is the sibling of internal/vpio/goplugin: above-the-seam callers
// (internal/cli/run.go) reference only vpio.Launcher/vpio.Session, so selecting
// this transport for a container-policy interactive top-level run needs no
// change above the seam (the Ctrl-] observation surround + injection wrap stay
// host-side, applied to the streams BEFORE the Launcher is constructed).
//
// Unlike goplugin (which wraps an already-connected go-plugin Run stream), this
// Launcher spawns the exec CLI as a subprocess under a HOST-SIDE pty pair
// (github.com/creack/pty) so the CLI's own `-t requires a tty` check is
// satisfied while spec.Stdin/Stdout stay the frontend's already-wrapped
// streams. The exec'd in-container command is `ctxloom llm turn <backend>
// --start <path>`, which runs the Run-RPC body (Setup→Execute→Cleanup) DIRECTLY
// on the exec TTY. There is NO in-container listener and NO published port: the
// exec rides the daemon's control socket (the mauve-state class cannot recur on
// this path).
package dockerexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

// execTailBytes bounds the ring of exec-CLI/turn output kept for a docker-level
// failure's diagnostic tail (mirrors startDirectRunner's stderr ring).
const execTailBytes = 8192

// outputDrainGrace bounds Wait's wait for the output pump to finish on its own
// (the child's exit closes the pty slave, so the master read returns and the
// pump ends) before Wait force-closes the master as a backstop against a
// grandchild still holding the slave open.
const outputDrainGrace = 2 * time.Second

// TurnSpec is the in-container interactive turn one Launcher drives: which
// backend, its config label, the in-container path of the 0600 RunStart handoff
// file (§5.A4 — RunStart NEVER rides argv/env), and the env NAMES forwarded to
// the turn process (bare `-e NAME`, values on the exec subprocess env).
type TurnSpec struct {
	Backend   string
	Label     string
	StartPath string
	// Env crosses to the exec'd turn as bare `-e NAME` (values ride the exec
	// subprocess env, never the world-readable argv) — the coordinator
	// reach-back trio the turn's runner-MCP standup consumes.
	Env map[string]string
}

// Launcher binds a runtime + the RUNNING container's name + the interactive
// turn command into a vpio.Launcher. Start runs one `exec -it` turn.
type Launcher struct {
	rt            isolation.Runtime
	containerName string
	turn          TurnSpec
}

var _ vpio.Launcher = (*Launcher)(nil)

// NewLauncher builds a Launcher for one interactive turn into containerName.
func NewLauncher(rt isolation.Runtime, containerName string, turn TurnSpec) *Launcher {
	return &Launcher{rt: rt, containerName: containerName, turn: turn}
}

// turnTermValue is the TERM the turn PROCESS runs under: `dumb`, deliberately.
// The turn is `ctxloom` on the interactive TTY, and ctxloom's package-init
// terminal-capability detection (lipgloss/termenv querying the background via
// OSC 11 + a DSR terminator) would otherwise fire and READ the response from
// this same stdin, swallowing the user's first keystrokes. `dumb` makes termenv
// skip the query entirely. The ENGINE child keeps real color: its env is
// os.Environ() overridden by RunStart.Options.Env, into which the host stamps
// the real TERM/COLORTERM (see startContainerInteractive), so the override wins
// over this `dumb` for the engine while the turn process itself stays quiet.
const turnTermValue = "dumb"

// buildExecCmd renders the `<binary> exec -i -t [-e NAME…] <name> ctxloom llm
// turn <backend> [--label L] --start <path>` command. Env NAMES cross bare; the
// values ride the subprocess env so the daemon forwards them without ever
// putting them on the argv.
func (l *Launcher) buildExecCmd(ctx context.Context) *exec.Cmd {
	command := []string{"ctxloom", "llm", "turn", l.turn.Backend}
	if l.turn.Label != "" {
		command = append(command, "--label", l.turn.Label)
	}
	command = append(command, "--start", l.turn.StartPath)

	// Force the turn process's TERM=dumb (via the bare-name channel: the value
	// rides the exec subprocess env, the daemon forwards it). Overrides any TERM
	// a caller put in TurnSpec.Env.
	fwd := make(map[string]string, len(l.turn.Env)+1)
	for k, v := range l.turn.Env {
		fwd[k] = v
	}
	fwd["TERM"] = turnTermValue

	names := make([]string, 0, len(fwd))
	for k := range fwd {
		names = append(names, k)
	}
	sort.Strings(names)

	args := l.rt.ExecArgs(l.containerName, true, names, command)
	cmd := exec.CommandContext(ctx, l.rt.Binary(), args...)
	kv := make([]string, 0, len(names))
	for _, k := range names {
		kv = append(kv, k+"="+fwd[k])
	}
	cmd.Env = append(os.Environ(), kv...)
	return cmd
}

// Start runs the exec turn under a host pty pair and wires spec's stdio to it.
// It can fail SYNCHRONOUSLY (the exec CLI refusing to attach — dead/absent
// container, missing binary), exactly the case the seam anticipated for a
// docker-exec transport.
func (l *Launcher) Start(ctx context.Context, spec vpio.ProcessSpec) (vpio.Session, error) {
	return startPTYCommand(ctx, l.buildExecCmd(ctx), spec)
}

// startPTYCommand starts cmd wired to a fresh host pty pair (its stdio becomes
// the pty slave, so `docker exec -t`'s tty check passes) and pumps spec.Stdin →
// master and master → spec.Stdout (teeing a bounded tail for failure
// diagnostics). Factored out of Start so the pty/exit/resize plumbing is
// unit-testable against a plain command (sh/cat) with no docker daemon.
func startPTYCommand(ctx context.Context, cmd *exec.Cmd, spec vpio.ProcessSpec) (*Session, error) {
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("vpio/dockerexec: start exec under host pty: %w", err)
	}
	s := &Session{master: master, cmd: cmd, ring: newTailRing(execTailBytes), outDone: make(chan struct{})}

	if spec.Stdin != nil {
		go func() { _, _ = io.Copy(master, spec.Stdin) }()
	}

	go func() {
		defer close(s.outDone)
		var dst io.Writer = io.Discard
		if spec.Stdout != nil {
			dst = spec.Stdout
		}
		_, _ = io.Copy(io.MultiWriter(dst, s.ring), master)
	}()

	return s, nil
}

// Session is the docker-exec vpio.Session: the exec subprocess plus the host
// pty master its interactive stream rides.
type Session struct {
	master  *os.File
	cmd     *exec.Cmd
	ring    *tailRing
	outDone chan struct{}

	waitOnce sync.Once
	result   vpio.ExitStatus
	waitErr  error
}

var _ vpio.Session = (*Session)(nil)

// Resize retargets the host pty master's winsize (pty.Setsize). The kernel
// SIGWINCHes the exec CLI (foreground pgrp of the slave), which forwards the
// resize to the daemon → the exec's container TTY → the in-container turn
// process's own SIGWINCH → its Execute resize channel. Non-blocking (a single
// ioctl), so it never stalls above-the-seam's resize pump.
func (s *Session) Resize(rows, cols uint32) {
	_ = pty.Setsize(s.master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Signal reports non-support: `docker exec` has no out-of-band signal channel,
// so interactive interrupts ride as raw stdin bytes under the terminal's own
// raw mode instead (identical to goplugin.Session). No above-the-seam caller
// invokes this today.
func (s *Session) Signal(sig os.Signal) error {
	return fmt.Errorf("vpio/dockerexec: signal delivery is not supported over docker exec (requested %v); interactive interrupts ride as raw stdin bytes under the terminal's raw mode", sig)
}

// Wait blocks for the exec turn to end and maps its exit code. The exec CLI
// PROPAGATES the exec'd command's exit code, so a normal engine exit (0 or the
// engine's own nonzero) returns as ExitStatus{Code} with a NIL error — run.go's
// epilogue turns a nonzero into an ExitError. docker/podman's OWN failure codes
// (125/126/127 — no such container, cannot attach, not executable) instead
// surface as a loud error carrying the CLI output tail, mirroring
// startDirectRunner's ring pattern.
func (s *Session) Wait() (vpio.ExitStatus, error) {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		// The output pump ends when the child's exit closes the pty slave (the
		// master read returns); backstop-close the master so a grandchild still
		// holding the slave can never wedge Wait.
		select {
		case <-s.outDone:
		case <-time.After(outputDrainGrace):
			_ = s.master.Close()
			<-s.outDone
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := int32(exitErr.ExitCode())
			if code == 125 || code == 126 || code == 127 {
				s.waitErr = s.dockerLevelError(code)
				return
			}
			s.result = vpio.ExitStatus{Code: code}
			return
		}
		if err != nil {
			// A non-exit failure: the CLI process itself could not run/be
			// reaped. Loud, with whatever tail we captured.
			s.waitErr = s.dockerLevelErrorWrap(err)
			return
		}
		s.result = vpio.ExitStatus{Code: 0}
	})
	return s.result, s.waitErr
}

func (s *Session) dockerLevelError(code int32) error {
	if tail := s.ring.tail(); tail != "" {
		return fmt.Errorf("container exec failed (code %d — the runtime, not the engine): %s", code, tail)
	}
	return fmt.Errorf("container exec failed (code %d — the runtime, not the engine)", code)
}

func (s *Session) dockerLevelErrorWrap(err error) error {
	if tail := s.ring.tail(); tail != "" {
		return fmt.Errorf("container exec failed: %w (output tail: %s)", err, tail)
	}
	return fmt.Errorf("container exec failed: %w", err)
}

// tailRing keeps only the last max bytes written to it — a bounded output tail
// for a docker-level failure's diagnostics (the exec CLI's stderr is on the
// shared pty, so this tees the whole stream). Concurrency-safe: the output pump
// writes while Wait reads.
type tailRing struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailRing(max int) *tailRing { return &tailRing{max: max} }

func (r *tailRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *tailRing) tail() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}
