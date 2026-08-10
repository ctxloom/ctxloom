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

// TestFormatEntry_RoundTripsValuesNeedingEscapes pins the write and read
// halves onto ONE escape alphabet.
//
// FormatEntry quoted option values with Go's %q, whose alphabet includes \n,
// \t, \x.. and \u...., while unescapeQuoted decodes only \" and \\ and passes
// every other backslash through literally. So a namespace containing a tab was
// written as namespaces="a\tb" and read back as the five-character literal
// a\tb: an entry that grants trust for a namespace nobody asked for and
// refuses the one the operator was shown, with no error on either side.
//
// Reachability, measured: the CLI cannot get here — ResolveSignerNamespaces
// maps to a closed three-value vocabulary, none of which needs an escape. The
// exposure is AddSignerRequest.Namespaces, which bypasses that resolver, and
// FormatEntry itself, whose doc states the round-trip as a guarantee.
func TestFormatEntry_RoundTripsValuesNeedingEscapes(t *testing.T) {
	pub := testPublicKey(t)
	for name, ns := range map[string]string{
		"tab":       "a\tb",
		"space":     "a b",
		"backslash": `a\b`,
		"quote":     `a"b`,
		"non-ascii": "café.example",
	} {
		t.Run(name, func(t *testing.T) {
			e := Entry{
				Principals: []string{"releases@ctxloom.dev"},
				Namespaces: []string{ns},
				KeyType:    "ssh-ed25519",
				PublicKey:  pub,
			}
			line, err := FormatEntry(e)
			require.NoError(t, err)

			store, perrs, err := Parse(strings.NewReader(line + "\n"))
			require.NoError(t, err)
			require.Empty(t, perrs, "formatted line must parse cleanly: %q", line)
			require.Len(t, store.Entries(), 1)
			assert.Equal(t, []string{ns}, store.Entries()[0].Namespaces,
				"read back a different namespace than was written: %q", line)
		})
	}
}

// TestFormatEntry_RejectsNamespaceTheGrammarCannotCarry is the fail-loud half,
// covering the two characters the quoted-value grammar has no escape for.
// A newline would be emitted verbatim and split the entry across two lines (the
// hazard validComment already refuses for the comment field); a comma is the
// pattern-list SEPARATOR, so one namespace containing it silently becomes two
// — a WIDER grant than the caller asked for, which is the same widening
// validPrincipal refuses for the principals field.
func TestFormatEntry_RejectsNamespaceTheGrammarCannotCarry(t *testing.T) {
	pub := testPublicKey(t)
	for _, ns := range []string{"a\nb", "a\rb", "a,b"} {
		_, err := FormatEntry(Entry{
			Principals: []string{"releases@ctxloom.dev"},
			Namespaces: []string{ns},
			KeyType:    "ssh-ed25519",
			PublicKey:  pub,
		})
		require.Error(t, err, "namespace %q must be refused", ns)
	}
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

// A principal containing whitespace makes ssh.ParseAuthorizedKey
// tokenize the tail differently, so Parse drops the entry on every subsequent
// read — while `ctxloom signer trust` printed a success line and a
// fingerprint. Zero trust delivered, reported as trust granted.
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

// FormatEntry documents itself as rendering "one allowed_signers
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
