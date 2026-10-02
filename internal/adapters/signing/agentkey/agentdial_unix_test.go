//go:build !windows

package agentkey

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func listenAgentSocket(t *testing.T) (string, net.Listener) {
	t.Helper()
	sock := filepath.Join(testsupport.SocketDir(t, "agent.sock"), "agent.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	return sock, ln
}

// The unix arm dials SSH_AUTH_SOCK as a unix socket and speaks the agent
// protocol over it — the behaviour the Windows twin must not disturb.
func TestDialAgentAt_UnixSocketReachesTheAgent(t *testing.T) {
	sock, ln := listenAgentSocket(t)
	requireAgentHolds(t, sock, serveKeyring(t, ln))
}

// TestDialAgentAt_ReturnsAClosableAgent proves the mechanism is live on the
// PRODUCTION path, not merely on a fake that opted in. If the real dialer's
// agent does not implement io.Closer, everything in close_test.go passes while
// the actual socket still leaks.
func TestDialAgentAt_ReturnsAClosableAgent(t *testing.T) {
	sock, ln := listenAgentSocket(t)
	defer func() { _ = ln.Close() }()

	ag, err := dialAgentAt(sock)()
	require.NoError(t, err)

	closer, ok := ag.(io.Closer)
	require.True(t, ok, "the production dialer must hand back an agent that owns its connection")
	assert.NoError(t, closer.Close())
}

// The discoverer dials the ssh-agent socket its composition handed it, never
// one it read from its own environment; no socket means no agent.
func TestNewDiscoverer_DialsTheHandedSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	_, err := NewDiscoverer(Env{AgentSocket: sock}).dialAgent()
	var dialErr *AgentDialError
	require.ErrorAs(t, err, &dialErr, "an unreachable socket is a typed dial failure")
	assert.Equal(t, sock, dialErr.Endpoint, "the unix arm dials SSH_AUTH_SOCK verbatim")
	assert.ErrorIs(t, err, syscall.ENOENT)

	_, err = NewDiscoverer(Env{}).dialAgent()
	assert.ErrorIs(t, err, errAgentSocketUnset, "no socket handed in means no ssh-agent to sign with")
}

// The typed dial failure survives the discovery chain's NoKeyError wrapping.
func TestDiscover_AgentDialFailureIsTyped(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	d := NewDiscoverer(Env{AgentSocket: sock})
	d.GitConfig = func(context.Context, string, string) (string, bool, error) { return "", false, nil }

	_, err := d.Discover(context.Background(), "")
	var noKey *NoKeyError
	require.ErrorAs(t, err, &noKey)
	var dialErr *AgentDialError
	require.ErrorAs(t, err, &dialErr)
	assert.Equal(t, sock, dialErr.Endpoint)
}
