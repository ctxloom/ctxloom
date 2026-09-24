// Package attach is the ORIGINATOR side of an interactive turn in a
// container: the container runtime's CLI (`docker run -i -t … ctxloom
// runner <engine>`, the runner as the container's foreground process) is
// started on a pseudo-terminal this process holds the MASTER of, so the
// CLI's `-it` attachment IS the terminal the runner's stdio lands on —
// keystrokes, the engine's bytes and resizes cross the pty and the daemon's
// tty, never a proxied stream, an exec into a keepalive or a handoff file.
//
// This is the container counterpart of adapters/hostpty, which owns the SAME
// shape for a host launch; both hand the frontend one pty master, blind to
// where the runner runs. What attach adds is teardown by NAME: the CLI's
// death does not end the container the daemon runs, so Kill removes the
// container first.
package attach

import (
	"context"
	"os/exec"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
)

// Session is a live container attach: the runtime CLI on the pty, the
// container's name, and the removal that teardown runs before ending it.
type Session struct {
	*hostpty.Session
	name       string
	remove     func(runExited <-chan struct{})
	removeOnce sync.Once
	endOnce    sync.Once
}

// Start starts cmd — the runtime's attached run — on a pty and returns the
// session. name is the container the run names; remove force-removes it,
// given the channel that closes when the run CLI has exited (a remove that
// finds nothing is only final once that CLI can no longer create the
// container), and is teardown's first act. Wait alone reaps the CLI: a
// container that ended on its own has nothing left to remove.
func Start(ctx context.Context, cmd *exec.Cmd, name string, remove func(runExited <-chan struct{})) (*Session, error) {
	s, err := hostpty.Start(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return &Session{Session: s, name: name, remove: remove}, nil
}

// Name is the container's name — the roster's handle on it.
func (s *Session) Name() string { return s.name }

// relayGrace bounds how long the run CLI may outlive the removal End starts.
// It is a liveness bound for a relay that never finishes (a wedged daemon),
// not the drain's synchronization: a removed container ends the attach
// stream, and the CLI exits on its own once it has written the container's
// last bytes.
const relayGrace = 10 * time.Second

// removeContainer runs the removal exactly once, whichever of End and Kill
// reaches it first; a concurrent caller waits for it to finish.
func (s *Session) removeContainer() {
	s.removeOnce.Do(func() {
		if s.remove != nil {
			s.remove(s.Exited())
		}
	})
}

// End ends the RUNNER — the container, removed by name — and leaves the run
// CLI to finish relaying and the master to its reader. The pty's child here
// is the relay, not the runner: the container's last bytes can still be in
// the daemon's attach stream or the CLI when the runner reports its exit, and
// ending the CLI then discards them. So End does not block: the removal may
// wait on the CLI's exit, which can need the master read first. A CLI still
// alive relayGrace after the removal is ended. Idempotent.
func (s *Session) End() {
	s.endOnce.Do(func() {
		go func() {
			s.removeContainer()
			grace := time.NewTimer(relayGrace)
			defer grace.Stop()
			select {
			case <-s.Exited():
			case <-grace.C:
				s.Session.End()
			}
		}()
	})
}

// Kill removes the container, then ends the CLI at once and releases the pty.
// The removal completes BEFORE the CLI is ended: ending a CLI whose create has
// not landed yet would orphan the container that create makes. Idempotent;
// safe after Wait.
func (s *Session) Kill() {
	s.removeContainer()
	s.End()
	s.Session.Kill()
}
