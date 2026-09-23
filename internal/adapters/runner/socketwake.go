package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// socketWake posts the wake to an engine's cross-session messaging socket
// (claude exports its path as CLAUDE_CODE_MESSAGING_SOCKET). The receiver
// starts a turn with the posted text as the prompt when idle, and leaves a
// human's draft in the composer untouched.
//
// The wire is ONE newline-terminated line, {"type":"user","message":
// {"role":"user","content":<text>}} — recovered from the claude binary, not
// documented — and the receiver sends nothing back on success or failure.
type socketWake struct{ path string }

// NewSocketWake binds a wake that posts to the unix socket at path.
func NewSocketWake(path string) engine.Wake { return socketWake{path: path} }

type socketWakeLine struct {
	Type    string             `json:"type"`
	Message socketWakeUserTurn `json:"message"`
}

type socketWakeUserTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Fire connects, writes the one line, and closes. With no acknowledgement on
// the wire, a refused connection or a failed write is the only failure it can
// observe, and it returns it: a silent wake is indistinguishable from a
// delivered one.
func (w socketWake) Fire(ctx context.Context, nonce string) error {
	line, err := json.Marshal(socketWakeLine{Type: "user", Message: socketWakeUserTurn{Role: "user", Content: spool.WakeText(nonce)}})
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", w.path)
	if err != nil {
		return fmt.Errorf("socket wake: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("socket wake: writing to %s: %w", w.path, err)
	}
	return nil
}
