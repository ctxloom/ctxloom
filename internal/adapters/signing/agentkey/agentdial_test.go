package agentkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// serveKeyring serves an in-memory ssh-agent holding one fresh key on every
// connection ln accepts, and returns that key — the far end of a real dial,
// whatever transport ln is.
func serveKeyring(t *testing.T, ln net.Listener) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: priv}))
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	return sshPub
}

// requireAgentHolds dials sock through the production dialer and asserts the
// agent answering is the one holding want.
func requireAgentHolds(t *testing.T, sock string, want ssh.PublicKey) {
	t.Helper()
	ag, err := dialAgentAt(sock)()
	require.NoError(t, err)
	defer func() { _ = ag.(*closingAgent).Close() }()
	keys, err := ag.List()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, want.Marshal(), keys[0].Marshal())
}

// agentPipe is the Windows arm's whole path policy, kept platform-neutral so
// it is exercised on every host, not only on the Windows CI leg.
func TestAgentPipe(t *testing.T) {
	for _, tc := range []struct {
		name, sock, want string
	}{
		{"a pipe in SSH_AUTH_SOCK is dialed as given", `\\.\pipe\my-agent`, `\\.\pipe\my-agent`},
		{"the pipe prefix matches case-insensitively", `\\.\PIPE\my-agent`, `\\.\PIPE\my-agent`},
		{"unset falls back to the OpenSSH default", "", defaultAgentPipe},
		{"a unix-style path falls back to the default", "/tmp/ssh-XXXX/agent.123", defaultAgentPipe},
		{"exactly the pipe namespace is dialed as given, not defaulted", `\\.\pipe\`, `\\.\pipe\`},
		{"a bare prefix fragment is not a pipe", `\\.\pip`, defaultAgentPipe},
		{"a remote pipe is not a local pipe", `\\host\pipe\agent`, defaultAgentPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agentPipe(tc.sock))
		})
	}
	assert.Equal(t, `\\.\pipe\openssh-ssh-agent`, defaultAgentPipe,
		"the default is the OpenSSH-for-Windows agent service's pipe")
}

// A dial failure is typed, names the endpoint, and keeps the cause reachable —
// including from behind the NoKeyError the discovery chain wraps it in.
func TestAgentDialError_NamesEndpointAndUnwraps(t *testing.T) {
	cause := errors.New("refused")
	err := error(&NoKeyError{Err: &AgentDialError{Endpoint: `\\.\pipe\x`, Err: cause}})

	var dialErr *AgentDialError
	require.ErrorAs(t, err, &dialErr)
	assert.Equal(t, `\\.\pipe\x`, dialErr.Endpoint)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, `connect to ssh-agent at \\.\pipe\x: refused`, dialErr.Error())
}
