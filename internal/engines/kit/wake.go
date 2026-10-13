package kit

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// SocketWake is a WakeSpec over a unix socket the session's own runtime
// listens on: the interactive session's Exec names the socket in EnvVar
// (Path), the runner binds the wake from that same env, and Fire posts the
// wake text as one line. What reads the line (the engine's runtime, a bridge
// ctxloom generated into it) is the engine's.
type SocketWake struct {
	// EnvVar is the engine-owned variable naming the socket.
	EnvVar string
}

// wakeSocketName is the socket's leaf in the session's socket root
// (engine.Session.SocketRoot), within the engine.SocketLeafMax that root
// leaves room for.
const wakeSocketName = "wake.sock"

// Path is the socket an interactive session listens on, in its short socket
// root; "" for a structured session or one without that root, which no wake
// reaches.
func (w SocketWake) Path(s engine.Session) string {
	if s.Mode != engine.Interactive || s.SocketRoot == "" {
		return ""
	}
	return filepath.Join(s.SocketRoot, wakeSocketName)
}

// Ensure is Path with the socket root made ready for the listener to bind
// in: an owner-only directory, created when absent. A root that exists as
// anything but a directory (a link included: the name is predictable, and a
// shared temp dir is a fallback base) is refused, as is one the session's
// user cannot restrict to itself. "" with no error where Path is "".
func (w SocketWake) Ensure(s engine.Session) (string, error) {
	path := w.Path(s)
	if path == "" {
		return "", nil
	}
	root, dir := safefs.New(), s.SocketRoot
	if l, ok := root.Fs.(afero.Lstater); ok {
		if st, _, err := l.LstatIfPossible(dir); err == nil && !st.IsDir() {
			return "", fmt.Errorf("socket wake: socket root %s is not a directory of the session's own (%s)", dir, st.Mode().Type())
		}
	}
	if err := root.Private.Ensure(dir); err != nil {
		return "", fmt.Errorf("socket wake: socket root %s: %w", dir, err)
	}
	return path, nil
}

// ReleaseSocketRoot removes the session's socket root once its engine has
// ended, when it is empty: the listener unlinks its own socket on close, and
// a socket still there belongs to a process of the same session that is
// still running (a concurrent resume shares the root), so it is left.
func ReleaseSocketRoot(s engine.Session) {
	if s.SocketRoot != "" {
		_ = safefs.New().Fs.Remove(s.SocketRoot)
	}
}

// Bind reads the socket from the session's env; no socket is ErrWakeUnbound.
func (w SocketWake) Bind(_ context.Context, env engine.WakeEnv) (engine.Wake, error) {
	path, _ := env(w.EnvVar)
	if path == "" {
		return nil, fmt.Errorf("%w: no %s in this environment", engine.ErrWakeUnbound, w.EnvVar)
	}
	return boundSocketWake{path: path}, nil
}

type boundSocketWake struct{ path string }

// Fire posts the wake text as one line and closes.
func (w boundSocketWake) Fire(ctx context.Context, nonce string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", w.path)
	if err != nil {
		return fmt.Errorf("socket wake: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, engine.WakeText(nonce)); err != nil {
		return fmt.Errorf("socket wake: writing to %s: %w", w.path, err)
	}
	return nil
}

var _ engine.WakeSpec = SocketWake{}
