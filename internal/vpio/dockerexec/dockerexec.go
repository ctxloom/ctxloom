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
// exec rides the daemon's control socket (the routable in-container listener
// vulnerability class cannot recur on this path).
package dockerexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

// outputDrainGrace bounds Wait's wait for the output pump to finish on its own
// (the child's exit closes the pty slave, so the master read returns and the
// pump ends) before Wait force-closes the master as a backstop against a
// grandchild still holding the slave open.
const outputDrainGrace = 2 * time.Second

// cancelGrace bounds how long a cancelled turn's exec CLI has to act on the
// SIGTERM Cancel sends before os/exec kills it outright (cmd.WaitDelay).
const cancelGrace = 2 * time.Second

// The exit codes docker/podman `exec` reserves for its OWN failures, as opposed
// to propagating the exec'd command's status. Any other code is the engine's.
const (
	// exitNoSuchContainer: the exec could not be set up at all (no such
	// container, or it is not running).
	exitNoSuchContainer = 125
	// exitCannotInvoke: the container command was found but could not be
	// invoked (not executable, wrong permissions).
	exitCannotInvoke = 126
	// exitCommandNotFound: the container command does not exist on the
	// in-container PATH.
	exitCommandNotFound = 127
)

// isDockerLevelExit reports whether code is the RUNTIME's own failure rather
// than the exec'd engine's exit status. The distinction decides whether Wait
// returns a loud error or a plain ExitStatus.
func isDockerLevelExit(code int32) bool {
	switch code {
	case exitNoSuchContainer, exitCannotInvoke, exitCommandNotFound:
		return true
	default:
		return false
	}
}

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
	if err := l.validate(); err != nil {
		return nil, err
	}
	return startPTYCommand(ctx, l.buildExecCmd(ctx), spec)
}

// validate refuses a turn that would render a well-formed but meaningless
// command. Every one of these fields is required for the exec to mean anything,
// and a blank one otherwise surfaces as the runtime's (or the in-container
// ctxloom's) complaint about argv — a message about the symptom, from a process
// that has no idea which caller field was empty. This one does.
func (l *Launcher) validate() error {
	switch {
	case l.rt == nil:
		return fmt.Errorf("vpio/dockerexec: no container runtime")
	case l.containerName == "":
		return fmt.Errorf("vpio/dockerexec: no container name to exec into")
	case l.turn.Backend == "":
		return fmt.Errorf("vpio/dockerexec: TurnSpec.Backend is empty; there is no backend to run the turn against")
	case l.turn.StartPath == "":
		return fmt.Errorf("vpio/dockerexec: TurnSpec.StartPath is empty; the turn has no --start handoff file to read")
	}
	return nil
}

// startPTYCommand starts cmd wired to a fresh host pty pair (its stdio becomes
// the pty slave, so `docker exec -t`'s tty check passes) and pumps spec.Stdin →
// master and master → spec.Stdout (teeing a bounded tail for failure
// diagnostics). Factored out of Start so the pty/exit/resize plumbing is
// unit-testable against a plain command (sh/cat) with no docker daemon.
func startPTYCommand(ctx context.Context, cmd *exec.Cmd, spec vpio.ProcessSpec) (*Session, error) {
	// A nil spec.Stdout used to silently substitute io.Discard —
	// the pump ran, Wait still returned ExitStatus{Code: 0}, nil, and the
	// ENTIRE interactive session's output vanished with no error, no
	// warning, no log: exit 0, success, zero bytes delivered. vpio.ProcessSpec
	// documents nil-handling for Stdin only (a non-interactive turn); nil
	// Stdout is not a sanctioned input for this transport, so refuse it
	// loudly instead of guessing.
	if spec.Stdout == nil {
		return nil, fmt.Errorf("vpio/dockerexec: ProcessSpec.Stdout is nil; this transport has nowhere to deliver the session's output")
	}
	// Cancellation ASKS before it kills. os/exec's default Cancel for a
	// CommandContext is Process.Kill(), i.e. SIGKILL with no grace — and this
	// subprocess is the exec CLI holding a live attachment to an in-container
	// turn, so SIGKILL drops the attachment without the CLI ever telling the
	// daemon: nothing in the container is asked to stop. SIGTERM lets it
	// detach; WaitDelay is the backstop that still kills a CLI which ignores
	// it, so cancellation can never hang.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = cancelGrace

	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("vpio/dockerexec: start exec under host pty: %w", err)
	}
	s := &Session{
		master:  master,
		cmd:     cmd,
		ring:    stderrtail.New(stderrtail.DefaultBytes),
		stderr:  spec.Stderr,
		outDone: make(chan struct{}),
		inDone:  make(chan struct{}),
	}

	// The stdin pump ends when the master is closed: the copy's next write
	// fails with os.ErrClosed rather than reaching whatever the kernel has
	// since given that descriptor number to. os.Stdin's own Read cannot be
	// interrupted from here, so the goroutine may still be parked in it until
	// one more keystroke arrives — but a parked reader that can no longer write
	// anywhere is inert, and inDone makes the moment it retires observable
	// instead of invisible.
	if spec.Stdin != nil {
		go func() {
			defer close(s.inDone)
			_, _ = io.Copy(master, spec.Stdin)
		}()
	} else {
		close(s.inDone)
	}

	go func() {
		defer close(s.outDone)
		_, _ = io.Copy(io.MultiWriter(spec.Stdout, s.ring), master)
	}()

	return s, nil
}

// Session is the docker-exec vpio.Session: the exec subprocess plus the host
// pty master its interactive stream rides.
type Session struct {
	master *os.File
	cmd    *exec.Cmd
	ring   *stderrtail.Ring
	// stderr is where this transport's own diagnostics go — the frontend's
	// diagnostic stream, which nothing below the seam otherwise uses.
	stderr  io.Writer
	outDone chan struct{}
	// inDone closes when the stdin pump has retired. It is not waited on:
	// os.Stdin's Read is uninterruptible from here, so a session that ends
	// while the user is not typing leaves the pump parked until the next
	// keystroke. Closing the master first is what makes that parked reader
	// harmless — its next write can only fail.
	inDone chan struct{}

	mu           sync.Mutex
	masterClosed bool
	// resizeWarnOnce keeps a persistently failing ioctl to one line: SIGWINCH
	// can fire on every drag of a window edge, and a warning per pixel would
	// be worse than the silence it replaces.
	resizeWarnOnce sync.Once

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
//
// A resize arriving after the session has ended is dropped in silence — the
// seam documents Resize as drop-rather-than-stall, and there is nothing left to
// resize. A resize that FAILS while the session is live is a different thing:
// the container's TTY keeps the stale geometry for the rest of the turn and the
// agent redraws into the wrong box. vpio.Session.Resize returns nothing, so the
// only place to say so is the frontend's diagnostic stream, once per session.
func (s *Session) Resize(rows, cols uint32) {
	s.mu.Lock()
	over := s.masterClosed
	s.mu.Unlock()
	if over {
		return
	}
	if err := pty.Setsize(s.master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		s.resizeWarnOnce.Do(func() {
			s.warn("terminal resize did not reach the container (%v); its TTY keeps the previous size for this turn", err)
		})
	}
}

// warn emits one transport diagnostic on the frontend's stderr, falling back to
// clidiag's process sink when the caller supplied none.
func (s *Session) warn(format string, args ...any) {
	if s.stderr == nil {
		clidiag.Warn("ctxloom", format, args...)
		return
	}
	clidiag.Fwarn(s.stderr, "ctxloom", format, args...)
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
			s.closeMaster()
			<-s.outDone
		}
		// The session is over, so its fd goes with it. Closing here — not only
		// on the backstop arm above — is what stops a frontend driving several
		// container turns under one long-lived context from leaking one master
		// per turn.
		s.closeMaster()

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := int32(exitErr.ExitCode())
			if isDockerLevelExit(code) {
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

// closeMaster closes the host pty master exactly once and records that the
// session's fd is gone, so a later Resize can tell "the ioctl failed" from
// "there is no session left to resize".
func (s *Session) closeMaster() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.masterClosed {
		return
	}
	s.masterClosed = true
	_ = s.master.Close()
}

// withOutputTail attaches whatever the ring captured to a docker-level failure,
// under ONE label: the same bytes must read the same way (and grep the same
// way) whichever failure produced them. base is wrapped, so an underlying
// error stays reachable through errors.Is/As.
func (s *Session) withOutputTail(base error) error {
	if tail := s.ring.Tail(); tail != "" {
		return fmt.Errorf("%w (output tail: %s)", base, tail)
	}
	return base
}

// dockerLevelError renders a runtime-level exec failure code (see
// isDockerLevelExit) with the captured output tail.
func (s *Session) dockerLevelError(code int32) error {
	return s.withOutputTail(fmt.Errorf("container exec failed (code %d — the runtime, not the engine)", code))
}

// dockerLevelErrorWrap renders a non-exit failure — the CLI process itself
// could not run or be reaped — with the captured output tail.
func (s *Session) dockerLevelErrorWrap(err error) error {
	return s.withOutputTail(fmt.Errorf("container exec failed: %w", err))
}
