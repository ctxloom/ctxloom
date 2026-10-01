package mock

import (
	"context"
	"fmt"
	"net"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// EnvWakeSocket names the mock's own wake socket: the mock's interactive
// runtime listens on it and takes each line posted there exactly as a typed
// line, and the mock's wake binds to it. The mock is woken natively — never
// through another engine's tool.
const EnvWakeSocket = "CTXLOOM_MOCK_WAKE_SOCKET"

// socketWake is the mock's declared WakeSpec.
type socketWake struct{}

func (socketWake) Bind(_ context.Context, env engine.WakeEnv) (engine.Wake, error) {
	path, _ := env(EnvWakeSocket)
	if path == "" {
		return nil, fmt.Errorf("%w: no %s in this environment", engine.ErrWakeUnbound, EnvWakeSocket)
	}
	return boundSocketWake{path: path}, nil
}

type boundSocketWake struct{ path string }

// Fire posts the wake text as one line and closes.
func (w boundSocketWake) Fire(ctx context.Context, nonce string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", w.path)
	if err != nil {
		return fmt.Errorf("mock wake: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, engine.WakeText(nonce)); err != nil {
		return fmt.Errorf("mock wake: writing to %s: %w", w.path, err)
	}
	return nil
}
