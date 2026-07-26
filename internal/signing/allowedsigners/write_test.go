package allowedsigners

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

// --- the writer must not emit a line the reader will reject or misread ------

// U136-F01. A principal containing whitespace makes ssh.ParseAuthorizedKey
// tokenize the tail differently, so Parse drops the entry on every subsequent
// read — while `ctxloom signer add` printed a success line and a fingerprint.
// Zero trust delivered, reported as trust granted.
func TestFormatEntry_PrincipalWithWhitespace_IsRefused(t *testing.T) {
	pub := testPublicKey(t)
	for _, principal := range []string{"alice smith", "alice\tsmith", "alice\nsmith", "alice\rsmith", ""} {
		_, err := FormatEntry(Entry{Principals: []string{principal}, PublicKey: pub})
		assert.Error(t, err, "principal %q must be refused, not written as a line Parse will drop", principal)
	}
}

// A comma inside a single principal is the field separator, so it silently
// becomes TWO principals on read — a different (larger) grant than the one
// the confirmation prompt showed.
func TestFormatEntry_PrincipalWithComma_IsRefused(t *testing.T) {
	pub := testPublicKey(t)
	_, err := FormatEntry(Entry{Principals: []string{"alice,bob"}, PublicKey: pub})
	assert.Error(t, err, "a comma inside one principal must be refused; pass two principals instead")
}

// Two separate principals remain legal — the guard must not outlaw the format.
func TestFormatEntry_MultiplePrincipals_StillWork(t *testing.T) {
	pub := testPublicKey(t)
	line, err := FormatEntry(Entry{Principals: []string{"alice@example.com", "bob@example.com"}, PublicKey: pub})
	require.NoError(t, err)

	store, perrs, err := Parse(strings.NewReader(line + "\n"))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, store.Entries()[0].Principals)
}

// U136-F02. FormatEntry documents itself as rendering "one allowed_signers
// line". A newline in Comment made it emit TWO — the second one a complete,
// fully-trusted entry that the CLI's confirmation prompt never displayed.
// Comment reaches here from the unvalidated --comment flag.
func TestFormatEntry_CommentWithNewline_IsRefused(t *testing.T) {
	pub := testPublicKey(t)
	sshPub := pub
	injected := "harmless\nattacker@evil.example ssh-ed25519 " +
		base64.StdEncoding.EncodeToString(sshPub.Marshal())

	_, err := FormatEntry(Entry{Principals: []string{"alice@example.com"}, PublicKey: pub, Comment: injected})
	require.Error(t, err, "a newline in the comment must be refused: it appends a second trusted signer")

	_, err = FormatEntry(Entry{Principals: []string{"alice@example.com"}, PublicKey: pub, Comment: "trailing\r"})
	assert.Error(t, err, "a carriage return in the comment must be refused for the same reason")
}
