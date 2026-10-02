//go:build !windows

package agentkey

import "net"

// dialAgentConn connects to the ssh-agent listening on the unix socket sock.
func dialAgentConn(sock string) (net.Conn, error) {
	if sock == "" {
		return nil, errAgentSocketUnset
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, &AgentDialError{Endpoint: sock, Err: err}
	}
	return conn, nil
}
