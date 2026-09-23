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
	name   string
	remove func()
	once   sync.Once
}

// Start starts cmd — the runtime's attached run — on a pty and returns the
// session. name is the container the run names; remove force-removes it,
// and is Kill's first act. Wait alone reaps the CLI: a container that ended
// on its own has nothing left to remove.
func Start(ctx context.Context, cmd *exec.Cmd, name string, remove func()) (*Session, error) {
	s, err := hostpty.Start(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return &Session{Session: s, name: name, remove: remove}, nil
}

// Name is the container's name — the roster's handle on it.
func (s *Session) Name() string { return s.name }

// relayGrace bounds how long the run CLI may outlive End. It is a liveness
// bound for a relay that never finishes (a wedged daemon), not the drain's
// synchronization: a removed container ends the attach stream, and the CLI
// exits on its own once it has written the container's last bytes.
const relayGrace = 10 * time.Second

// End ends the RUNNER — the container, removed by name — and leaves the run
// CLI to finish relaying and the master to its reader. The pty's child here
// is the relay, not the runner: the container's last bytes can still be in
// the daemon's attach stream or the CLI when the runner reports its exit, and
// ending the CLI then discards them. A CLI still alive relayGrace after End
// is ended. Idempotent.
func (s *Session) End() {
	s.once.Do(func() {
		if s.remove != nil {
			s.remove()
		}
		go func() {
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

// Kill is End, then ends the CLI at once and releases the pty. Idempotent;
// safe after Wait.
func (s *Session) Kill() {
	s.End()
	s.Session.Kill()
}
