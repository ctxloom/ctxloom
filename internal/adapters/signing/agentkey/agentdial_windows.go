//go:build windows

package agentkey

import (
	"context"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// agentPipeDialTimeout bounds the wait for a free pipe instance:
// DialPipeContext retries a busy pipe until its context ends, so an unbounded
// context would let a wedged agent hang signing forever.
const agentPipeDialTimeout = 2 * time.Second

// dialAgentConn connects to the ssh-agent's named pipe, chosen by agentPipe.
func dialAgentConn(sock string) (net.Conn, error) {
	pipe := agentPipe(sock)
	ctx, cancel := context.WithTimeout(context.Background(), agentPipeDialTimeout)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, pipe)
	if err != nil {
		return nil, &AgentDialError{Endpoint: pipe, Err: err}
	}
	return conn, nil
}
