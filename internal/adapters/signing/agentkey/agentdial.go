package agentkey

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh/agent"
)

// errAgentSocketUnset is the unix arm's answer when the composition handed in
// no SSH_AUTH_SOCK: there is no agent to dial, as opposed to one that failed.
var errAgentSocketUnset = errors.New("SSH_AUTH_SOCK is not set — no ssh-agent to sign with")

// AgentDialError reports that the ssh-agent at Endpoint could not be reached.
// Endpoint is the address actually dialed, which on Windows is not
// necessarily SSH_AUTH_SOCK's value (see agentPipe) — so it is carried here
// rather than left for a caller to reconstruct.
type AgentDialError struct {
	Endpoint string
	Err      error
}

func (e *AgentDialError) Error() string {
	return fmt.Sprintf("connect to ssh-agent at %s: %v", e.Endpoint, e.Err)
}

// Unwrap exposes the transport's own error to errors.Is/errors.As.
func (e *AgentDialError) Unwrap() error { return e.Err }

// defaultAgentPipe is where Windows' bundled OpenSSH agent service listens
// when nothing says otherwise. INFERRED from OpenSSH-for-Windows' documented
// default, not yet verified on a Windows host.
const defaultAgentPipe = `\\.\pipe\openssh-ssh-agent`

// localPipePrefix is the namespace of a local named pipe. Windows path
// prefixes compare case-insensitively.
const localPipePrefix = `\\.\pipe\`

// agentPipe chooses the named pipe the Windows arm dials: SSH_AUTH_SOCK's
// value when it names a local pipe, otherwise the OpenSSH default. A value
// that is not a pipe (unset, or a unix-style path exported by an MSYS/Cygwin
// agent) is nothing a native pipe dial can reach. It lives outside the
// platform twin so it is tested on every host.
func agentPipe(sock string) string {
	if len(sock) >= len(localPipePrefix) && strings.EqualFold(sock[:len(localPipePrefix)], localPipePrefix) {
		return sock
	}
	return defaultAgentPipe
}

// dialAgentAt dials the ssh-agent at sock — the composition's value for
// SSH_AUTH_SOCK, which this adapter does not read for itself. How sock is
// reached is the platform twin's dialAgentConn.
func dialAgentAt(sock string) func() (agent.Agent, error) {
	return func() (agent.Agent, error) {
		conn, err := dialAgentConn(sock)
		if err != nil {
			return nil, err
		}
		return &closingAgent{ExtendedAgent: agent.NewClient(conn), conn: conn}, nil
	}
}
