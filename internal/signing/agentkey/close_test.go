package agentkey

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// countingAgent is a fakeAgent that also owns a connection, exactly as the
// production dialer's agent does.
type countingAgent struct {
	*fakeAgent
	closes int
}

func (c *countingAgent) Close() error {
	c.closes++
	return nil
}

// TestDiscover_AgentConnectionLifetime pins who releases the ssh-agent
// connection and when.
//
// The defect this pins (U135-F05): dialEnvAgent opened a unix socket and
// returned only the agent speaking over it, so the connection had no owner.
// Discovered exposed no Close, and nothing closed it on the FAILURE paths
// either — which is where it matters most, because `ctxloom doctor` and init
// PRIME each run the whole chain (twice, per doctor_cmd.go's two
// NewDiscoverer() calls) and usually end on an error arm.
//
// The two directions are asymmetric and both must hold: on failure the
// connection is released before the error is returned, and on SUCCESS it must
// survive — Discovered.Signer signs over it, so closing early would break
// signing rather than leak.
func TestDiscover_AgentConnectionLifetime(t *testing.T) {
	newAgent := func(signers []ssh.Signer, comments []string) *countingAgent {
		return &countingAgent{fakeAgent: &fakeAgent{signers: signers, comments: comments}}
	}

	t.Run("failure releases the connection", func(t *testing.T) {
		a, _ := newTestIdentity(t, "one@example.com")
		b, _ := newTestIdentity(t, "two@example.com")
		ag := newAgent([]ssh.Signer{a, b}, []string{"one@example.com", "two@example.com"})

		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return ag, nil },
		}

		_, err := d.Discover(context.Background(), "")

		var ambiguous *AmbiguousKeyError
		require.ErrorAs(t, err, &ambiguous)
		assert.Equal(t, 1, ag.closes,
			"an error arm must not walk away from the socket it opened")
	})

	t.Run("explicit-key failure releases the connection", func(t *testing.T) {
		a, _ := newTestIdentity(t, "one@example.com")
		ag := newAgent([]ssh.Signer{a}, []string{"one@example.com"})

		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return ag, nil },
			ReadFile:  func(string) ([]byte, error) { return nil, errors.New("no such file") },
		}

		_, err := d.Discover(context.Background(), "nobody@example.org")

		require.Error(t, err)
		assert.Equal(t, 1, ag.closes)
	})

	t.Run("success keeps the connection until the caller closes it", func(t *testing.T) {
		signer, _ := newTestIdentity(t, "sole@example.com")
		ag := newAgent([]ssh.Signer{signer}, []string{"sole@example.com"})

		d := &Discoverer{
			GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
			DialAgent: func() (agent.Agent, error) { return ag, nil },
		}

		got, err := d.Discover(context.Background(), "")
		require.NoError(t, err)
		require.Equal(t, 0, ag.closes,
			"Signer signs over this connection — closing it here would break signing, not fix a leak")

		require.NoError(t, got.Close())
		assert.Equal(t, 1, ag.closes, "Close must reach the connection")

		require.NoError(t, got.Close(), "Close is safe to call more than once")
	})

	t.Run("Close is a no-op when nothing was opened", func(t *testing.T) {
		assert.NoError(t, (&Discovered{}).Close(),
			"a caller must be able to defer Close unconditionally")
		var nilDiscovered *Discovered
		assert.NoError(t, nilDiscovered.Close())
	})
}

// TestDialEnvAgent_ReturnsAClosableAgent proves the mechanism is live on the
// PRODUCTION path, not merely on a fake that opted in. If the real dialer's
// agent does not implement io.Closer, everything above passes while the actual
// socket still leaks.
func TestDialEnvAgent_ReturnsAClosableAgent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	t.Setenv("SSH_AUTH_SOCK", sock)

	ag, err := dialEnvAgent()
	require.NoError(t, err)

	closer, ok := ag.(io.Closer)
	require.True(t, ok, "the production dialer must hand back an agent that owns its connection")
	assert.NoError(t, closer.Close())
}
