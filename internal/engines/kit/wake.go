package kit

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/engine"
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

// wakeSocketName is the socket's leaf in the session-private root. It is
// short because a unix socket path is capped (108 bytes on linux, 104 on
// darwin) and that root nests under the ctxloom home; a path past the cap
// fails the listen at session start, loudly.
const wakeSocketName = "wake.sock"

// Path is the socket an interactive session listens on, in its
// session-private root; "" for a structured session or one without that
// root, which no wake reaches.
func (w SocketWake) Path(s engine.Session) string {
	root := s.Roots.SessionHome.Engine
	if s.Mode != engine.Interactive || root == "" {
		return ""
	}
	return filepath.Join(root, wakeSocketName)
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
