package operations_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
)

func keyringWith(t *testing.T, comments ...string) agent.Agent {
	t.Helper()
	kr := agent.NewKeyring()
	for _, c := range comments {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv, Comment: c}))
	}
	return kr
}

func discovererOver(ag agent.Agent, dialErr error) *agentkey.Discoverer {
	return &agentkey.Discoverer{
		GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return ag, dialErr },
		ReadFile:  func(string) ([]byte, error) { return nil, assert.AnError },
	}
}

// ResolveLocalSigner is the one signing-key decision `review`, `sign`, the
// trust writers and `bundle push` share: a sole agent identity signs.
func TestResolveLocalSigner_SoleIdentitySigns(t *testing.T) {
	got, err := operations.ResolveLocalSigner(context.Background(), discovererOver(keyringWith(t, "me@example"), nil), "", false)
	require.NoError(t, err)
	require.NotNil(t, got.Signer)
	assert.False(t, got.Unsigned)
	assert.Nil(t, got.Ambiguous)
}

// Several identities and no explicit key: the decision is "unsigned", and the
// candidates ride the outcome for the frontend to render — the service prints
// nothing.
func TestResolveLocalSigner_AmbiguousDegradesToUnsignedAndCarriesTheCandidates(t *testing.T) {
	got, err := operations.ResolveLocalSigner(context.Background(), discovererOver(keyringWith(t, "a@example", "b@example"), nil), "", false)
	require.NoError(t, err)
	assert.Nil(t, got.Signer)
	assert.True(t, got.Unsigned)
	require.NotNil(t, got.Ambiguous)
	assert.Len(t, got.Ambiguous.Candidates, 2)
}

// No key anywhere: unsigned for a personal-store write, refused for a
// project one — the refusal is typed so each frontend can add its remedy.
func TestResolveLocalSigner_NoKey(t *testing.T) {
	d := discovererOver(nil, assert.AnError)
	got, err := operations.ResolveLocalSigner(context.Background(), d, "", false)
	require.NoError(t, err)
	assert.True(t, got.Unsigned)
	assert.Nil(t, got.Ambiguous)

	_, err = operations.ResolveLocalSigner(context.Background(), d, "", true)
	var refused *operations.NoSigningKeyError
	require.ErrorAs(t, err, &refused, "a project write with no key is refused, not degraded")
	assert.ErrorIs(t, refused.Cause, assert.AnError)
}

// The explicit key (sign.key / --key) disambiguates a multi-identity agent,
// exactly as it does for `ctxloom sign`.
func TestResolveLocalSigner_ExplicitKeyDisambiguates(t *testing.T) {
	kr := keyringWith(t, "other@example", "wanted@example")
	got, err := operations.ResolveLocalSigner(context.Background(), discovererOver(kr, nil), "wanted@example", false)
	require.NoError(t, err)
	require.NotNil(t, got.Signer)
	keys, err := kr.List()
	require.NoError(t, err)
	var wanted string
	for _, k := range keys {
		if k.Comment == "wanted@example" {
			wanted = ssh.FingerprintSHA256(k)
		}
	}
	assert.Equal(t, wanted, ssh.FingerprintSHA256(got.Signer.PublicKey()))
}
