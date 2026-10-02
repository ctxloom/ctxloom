package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Claude's wake is its cross-session messaging endpoint: claude listens on
// it and exports where (and, when it has one, the token that authenticates a
// sender) to every process it spawns. A post is delivered as this session's
// own — starting a turn when idle, without the permission-parity hold a
// foreign sender meets — only when the poster is SELF-SENT: claude's
// descendant on Linux (by process ancestry) and macOS (ancestry, token as
// fallback), the token-holder on Windows. So the wake is bound and fired in
// the one ctxloom process claude itself spawns, the session relay
// (RelayCommand), from that process's own environment. Mail never rides the
// post: it carries only the nonce, and the turn-start hook delivers the mail.
//
// SECURITY: same-uid is not a boundary here and this wake does not pretend
// otherwise — a same-uid process can already type into the session's
// terminal or write the spool the hook reads. Containers are the boundary.
// The token is a different matter: on Windows it alone grants parity, so it
// never leaves the relay process — never logged, never in an error, never
// exported onward.
const (
	envMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	envMessagingToken  = "CLAUDE_CODE_MESSAGING_TOKEN"
)

// messagingWake is claude's declared WakeSpec.
type messagingWake struct{}

// Bind reads the endpoint claude exported, and the token when there is one.
// tokenRequired is the per-OS fact: Windows decides a sender by the token
// alone, so a bind there without it could only post a held message.
func (messagingWake) Bind(_ context.Context, env engine.WakeEnv) (engine.Wake, error) {
	endpoint, _ := env(envMessagingSocket)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: claude exported no %s to this process", engine.ErrWakeUnbound, envMessagingSocket)
	}
	token, _ := env(envMessagingToken)
	if tokenRequired && token == "" {
		return nil, fmt.Errorf("%w: claude exported no %s to this process, and this OS admits a sender by it alone", engine.ErrWakeUnbound, envMessagingToken)
	}
	return boundMessagingWake{endpoint: endpoint, token: token}, nil
}

// boundMessagingWake posts to one session's messaging endpoint.
type boundMessagingWake struct{ endpoint, token string }

// Format prints the endpoint only: a %v of a struct prints its unexported
// fields raw, and the token must never reach a log line that way.
func (w boundMessagingWake) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "claude messaging wake at "+w.endpoint)
}

// messagingLine is one line of claude's messaging wire, recovered from the
// claude binary and measured, not documented.
type messagingLine struct {
	Type    string            `json:"type"`
	Token   string            `json:"token,omitempty"`
	Message *messagingMessage `json:"message,omitempty"`
}

type messagingMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Fire connects, writes the auth line (whenever there is a token) and the one
// user line, and closes. Nothing comes back on a self-sent post, so a refused
// connection or a failed write is the only failure it can observe; it returns
// it, because a silent wake is indistinguishable from a delivered one.
func (w boundMessagingWake) Fire(ctx context.Context, nonce string) error {
	lines := []messagingLine{{Type: "user", Message: &messagingMessage{Role: "user", Content: engine.WakeText(nonce)}}}
	if w.token != "" {
		lines = append([]messagingLine{{Type: "auth", Token: w.token}}, lines...)
	}
	var post bytes.Buffer
	enc := json.NewEncoder(&post)
	for _, l := range lines {
		// A line of plain strings has no encoding failure that could quote
		// the token back; the error is returned as the encoder gave it.
		if err := enc.Encode(l); err != nil {
			return fmt.Errorf("claude wake: encoding the post: %w", err)
		}
	}
	conn, err := dialMessaging(ctx, w.endpoint)
	if err != nil {
		return fmt.Errorf("claude wake: dialing %s: %w", w.endpoint, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	if _, err := conn.Write(post.Bytes()); err != nil {
		return fmt.Errorf("claude wake: writing to %s: %w", w.endpoint, err)
	}
	return nil
}
