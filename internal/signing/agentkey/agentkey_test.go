package agentkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// startTestAgent stands up a real in-memory ssh-agent served over a UNIX
// socket (not net.Pipe: Resolve dials a socket path, matching a real
// SSH_AUTH_SOCK), preloaded with the given keys. Returns the socket path.
func startTestAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, k := range keys {
		require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: k}))
	}
	sockPath := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	return sockPath
}

func genKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

func TestResolve_SingleIdentity_Unambiguous(t *testing.T) {
	priv := genKey(t)
	sock := startTestAgent(t, priv)

	signer, err := Resolve(sock, "")
	require.NoError(t, err)
	assert.NotNil(t, signer)

	wantPub, err := ssh.NewPublicKey(priv.Public().(ed25519.PublicKey))
	require.NoError(t, err)
	assert.Equal(t, wantPub.Marshal(), signer.PublicKey().Marshal())
}

func TestResolve_NoAgent_ErrNoAgent(t *testing.T) {
	_, err := Resolve("", "")
	assert.ErrorIs(t, err, ErrNoAgent)

	_, err = Resolve(filepath.Join(t.TempDir(), "no-such-socket"), "")
	assert.ErrorIs(t, err, ErrNoAgent)
}

func TestResolve_NoIdentities_ErrNoIdentities(t *testing.T) {
	sock := startTestAgent(t) // empty keyring
	_, err := Resolve(sock, "")
	assert.ErrorIs(t, err, ErrNoIdentities)
}

func TestResolve_MultipleIdentities_Ambiguous(t *testing.T) {
	sock := startTestAgent(t, genKey(t), genKey(t))
	_, err := Resolve(sock, "")
	require.Error(t, err)
	var ambiguous *AmbiguousError
	require.ErrorAs(t, err, &ambiguous)
	assert.Len(t, ambiguous.Identities, 2)
}

func TestResolve_MultipleIdentities_PreferredFingerprintSelects(t *testing.T) {
	privA, privB := genKey(t), genKey(t)
	sock := startTestAgent(t, privA, privB)

	c, err := Dial(sock)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	ids, err := c.Identities()
	require.NoError(t, err)
	require.Len(t, ids, 2)
	want := ids[1].Fingerprint()

	signer, err := Resolve(sock, want)
	require.NoError(t, err)
	assert.Equal(t, want, ssh.FingerprintSHA256(signer.PublicKey()))
}

func TestResolve_PreferredFingerprintNotFound(t *testing.T) {
	sock := startTestAgent(t, genKey(t))
	_, err := Resolve(sock, "SHA256:does-not-exist")
	assert.Error(t, err)
}

func TestResolveFromEnv_UsesSSHAuthSock(t *testing.T) {
	priv := genKey(t)
	sock := startTestAgent(t, priv)
	t.Setenv("SSH_AUTH_SOCK", sock)

	signer, err := ResolveFromEnv("")
	require.NoError(t, err)
	assert.NotNil(t, signer)
}

func TestResolveFromEnv_Unset(t *testing.T) {
	require.NoError(t, os.Unsetenv("SSH_AUTH_SOCK"))
	_, err := ResolveFromEnv("")
	assert.ErrorIs(t, err, ErrNoAgent)
}

func TestIsHardwareBacked(t *testing.T) {
	priv := genKey(t)
	sock := startTestAgent(t, priv)
	signer, err := Resolve(sock, "")
	require.NoError(t, err)

	assert.False(t, IsHardwareBacked(signer.PublicKey()), "a plain ed25519 key is software, not hardware")
	assert.False(t, IsHardwareBacked(nil))
}
