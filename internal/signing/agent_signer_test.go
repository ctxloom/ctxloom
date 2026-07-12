package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// TestSign_ViaSSHAgentClient proves the spec's dependency claim (§11A,
// implementer brief): "golang.org/x/crypto/ssh/agent's client already
// satisfies ssh.Signer ... so agent signing needs NO second dependency."
//
// It stands up a real in-memory ssh-agent (agent.NewKeyring, served over a
// net.Pipe — no real SSH_AUTH_SOCK needed for the test), adds a key to it,
// and signs through the resulting agent.ExtendedAgent exactly as
// package signing's Sign would be called by a caller whose signer came from
// ssh-agent rather than an on-disk key.
func TestSign_ViaSSHAgentClient(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: priv}))

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	go func() { _ = agent.ServeAgent(keyring, serverConn) }()

	agentClient := agent.NewClient(clientConn)

	signers, err := agentClient.Signers()
	require.NoError(t, err)
	require.Len(t, signers, 1)

	// agentClient.Signers()[0] IS an ssh.Signer — the exact type Sign()
	// takes. No adapter, no second library.
	agentSigner := signers[0]

	payload := []byte("bundle bytes signed via ssh-agent")
	armored, err := Sign(payload, agentSigner, "publish.v1.ctxloom.dev")
	require.NoError(t, err)

	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)

	err = Verify(payload, armored, sshPub, "publish.v1.ctxloom.dev")
	require.NoError(t, err, "a signature produced via ssh-agent must verify identically to one produced with an in-memory key")
}
