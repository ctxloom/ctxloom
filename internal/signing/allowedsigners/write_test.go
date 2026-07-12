package allowedsigners

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshPub
}

func TestFormatEntry_RoundTripsThroughParse(t *testing.T) {
	pub := testPublicKey(t)
	before := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	e := Entry{
		Principals:  []string{"releases@ctxloom.dev"},
		Namespaces:  []string{"publish.v1.ctxloom.dev"},
		ValidBefore: &before,
		KeyType:     "ssh-ed25519",
		PublicKey:   pub,
		Comment:     "ctxloom release key",
	}

	line, err := FormatEntry(e)
	require.NoError(t, err)

	store, perrs, err := Parse(strings.NewReader(line + "\n"))
	require.NoError(t, err)
	require.Empty(t, perrs, "formatted line must parse cleanly: %q", line)

	entries := store.Entries()
	require.Len(t, entries, 1)
	got := entries[0]
	assert.Equal(t, e.Principals, got.Principals)
	assert.Equal(t, e.Namespaces, got.Namespaces)
	assert.True(t, got.ValidBefore.Equal(before))
	assert.Equal(t, e.Comment, got.Comment)
	assert.True(t, keysEqual(e.PublicKey, got.PublicKey))
}

func TestFormatEntry_UnrestrictedWhenNamespacesNil(t *testing.T) {
	pub := testPublicKey(t)
	e := Entry{
		Principals: []string{"ben@abbitt.me"},
		PublicKey:  pub,
	}
	line, err := FormatEntry(e)
	require.NoError(t, err)
	assert.NotContains(t, line, "namespaces=")

	store, perrs, err := Parse(strings.NewReader(line + "\n"))
	require.NoError(t, err)
	require.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Nil(t, store.Entries()[0].Namespaces)
}

func TestFormatEntry_RequiresPrincipalAndKey(t *testing.T) {
	_, err := FormatEntry(Entry{PublicKey: testPublicKey(t)})
	assert.Error(t, err)

	_, err = FormatEntry(Entry{Principals: []string{"x@example.com"}})
	assert.Error(t, err)
}
