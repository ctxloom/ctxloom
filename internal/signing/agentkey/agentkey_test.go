package agentkey

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newTestIdentity generates a fresh ed25519 keypair, an ssh.Signer over it,
// and its authorized-keys-format public line (what git config
// user.signingkey or a --key path would contain).
func newTestIdentity(t *testing.T, comment string) (ssh.Signer, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	line := string(ssh.MarshalAuthorizedKey(sshPub))
	if comment != "" {
		line = line[:len(line)-1] + " " + comment + "\n"
	}
	return signer, line
}

// fakeAgent is an in-process agent.Agent that only implements what
// Discoverer actually calls (Signers, List), so tests don't need a real
// ssh-agent socket. Comments are tracked in parallel so
// candidatesFromSigners has something to report.
type fakeAgent struct {
	signers  []ssh.Signer
	comments []string
}

func (f *fakeAgent) List() ([]*agent.Key, error) {
	out := make([]*agent.Key, len(f.signers))
	for i, s := range f.signers {
		out[i] = &agent.Key{
			Format:  s.PublicKey().Type(),
			Blob:    s.PublicKey().Marshal(),
			Comment: f.comments[i],
		}
	}
	return out, nil
}
func (f *fakeAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	panic("not used by Discoverer")
}
func (f *fakeAgent) Add(k agent.AddedKey) error     { return errors.New("not used") }
func (f *fakeAgent) Remove(key ssh.PublicKey) error { return errors.New("not used") }
func (f *fakeAgent) RemoveAll() error               { return errors.New("not used") }
func (f *fakeAgent) Lock(passphrase []byte) error   { return errors.New("not used") }
func (f *fakeAgent) Unlock(passphrase []byte) error { return errors.New("not used") }
func (f *fakeAgent) Signers() ([]ssh.Signer, error) { return f.signers, nil }

func discovererWithAgent(ag agent.Agent, gitSigningKey string, gitErr error) *Discoverer {
	return &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) {
			if gitErr != nil {
				return "", false, gitErr
			}
			if key != "user.signingkey" {
				return "", false, nil
			}
			return gitSigningKey, gitSigningKey != "", nil
		},
		DialAgent: func() (agent.Agent, error) { return ag, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, fmt.Errorf("no such file: %s", path) },
	}
}

func TestDiscover_GitSigningKeyResolvesWithNoCtxloomConfig(t *testing.T) {
	signer, pubLine := newTestIdentity(t, "alice@laptop")
	other, otherLine := newTestIdentity(t, "other")
	ag := &fakeAgent{signers: []ssh.Signer{other, signer}, comments: []string{"other", "alice@laptop"}}
	_ = otherLine

	d := discovererWithAgent(ag, pubLine, nil)

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	assert.Equal(t, "git config user.signingkey", got.Source)
}

func TestDiscover_SoleAgentIdentity_NoGitConfig(t *testing.T) {
	signer, _ := newTestIdentity(t, "solo")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"solo"}}
	d := discovererWithAgent(ag, "", nil)

	got, err := d.Discover(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), got.Fingerprint)
	assert.Equal(t, "ssh-agent (sole identity)", got.Source)
}

func TestDiscover_Ambiguous_MultipleAgentIdentitiesNoGitConfig(t *testing.T) {
	s1, _ := newTestIdentity(t, "one")
	s2, _ := newTestIdentity(t, "two")
	ag := &fakeAgent{signers: []ssh.Signer{s1, s2}, comments: []string{"one", "two"}}
	d := discovererWithAgent(ag, "", nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var ambigErr *AmbiguousKeyError
	require.ErrorAs(t, err, &ambigErr)
	assert.Len(t, ambigErr.Candidates, 2)
	// Actionable: the error text names a concrete fix.
	assert.Contains(t, err.Error(), "config set sign.key")
	assert.Contains(t, err.Error(), "git config")
}

func TestDiscover_Empty_NoAgentNoGitConfig(t *testing.T) {
	d := &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return nil, errors.New("SSH_AUTH_SOCK is not set") },
		ReadFile:  func(path string) ([]byte, error) { return nil, errors.New("nope") },
	}

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, err.Error(), "no signing key found")
	assert.Contains(t, err.Error(), "ssh-add")
	assert.Contains(t, err.Error(), "--no-sign")
}

func TestDiscover_ExplicitKeyWinsOverGitConfigAndAgentAmbiguity(t *testing.T) {
	wanted, wantedLine := newTestIdentity(t, "wanted")
	decoy1, _ := newTestIdentity(t, "decoy1")
	decoy2, decoy2Line := newTestIdentity(t, "decoy2")
	ag := &fakeAgent{signers: []ssh.Signer{decoy1, wanted, decoy1}, comments: []string{"d1", "wanted", "d1"}}
	_ = decoy2Line
	_ = decoy2

	d := discovererWithAgent(ag, "", nil) // git config unset; agent has >1 identity (would be ambiguous)
	d.ReadFile = func(path string) ([]byte, error) {
		if path == "/keys/wanted.pub" {
			return []byte(wantedLine), nil
		}
		return nil, fmt.Errorf("no such file: %s", path)
	}

	got, err := d.Discover(context.Background(), "/keys/wanted.pub")
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(wanted.PublicKey()), got.Fingerprint)
	assert.Equal(t, "--key", got.Source)
}

func TestDiscover_GitNamesKeyNotLoadedInAgent_HardError(t *testing.T) {
	_, namedLine := newTestIdentity(t, "named-but-absent")
	other, _ := newTestIdentity(t, "present")
	ag := &fakeAgent{signers: []ssh.Signer{other}, comments: []string{"present"}}

	d := discovererWithAgent(ag, namedLine, nil)

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
	assert.Contains(t, err.Error(), "no signing key found")
}

func TestDiscover_NoAgentRunning_GitNamesKey_HardError(t *testing.T) {
	_, namedLine := newTestIdentity(t, "named")
	d := &Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) {
			return namedLine, true, nil
		},
		DialAgent: func() (agent.Agent, error) { return nil, errors.New("SSH_AUTH_SOCK is not set") },
		ReadFile:  func(path string) ([]byte, error) { return nil, errors.New("nope") },
	}

	_, err := d.Discover(context.Background(), "")
	require.Error(t, err)
	var noKeyErr *NoKeyError
	require.ErrorAs(t, err, &noKeyErr)
}

// TestDiscover_ExplicitFingerprintMatchesAgentIdentity exercises the
// SHA256:<fp> spelling of --key.
func TestDiscover_ExplicitFingerprintMatchesAgentIdentity(t *testing.T) {
	signer, _ := newTestIdentity(t, "fp-target")
	ag := &fakeAgent{signers: []ssh.Signer{signer}, comments: []string{"fp-target"}}
	d := discovererWithAgent(ag, "", nil)

	fp := ssh.FingerprintSHA256(signer.PublicKey())
	got, err := d.Discover(context.Background(), fp)
	require.NoError(t, err)
	assert.Equal(t, fp, got.Fingerprint)
}
