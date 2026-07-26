package allowedsigners

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real key blobs, generated with the actual ssh-keygen binary (see
// testdata/*.pub and the package-level interop tests in
// allowedsigners_interop_test.go for the exact commands run).
const (
	testEd25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGO+4UzAG5fNzbf+DqeceZ4ZtCIXMIStJzWMI6PG/CVJ publisher@example.com"

	// sk-ssh-ed25519@openssh.com blob. This host has no FIDO2 hardware
	// token, so it cannot be generated live with `ssh-keygen -t
	// ed25519-sk` here. This is a known-good sample taken verbatim from
	// golang.org/x/crypto/ssh's own test vectors
	// (ssh/testdata/keys.go:374-375, module cache path
	// golang.org/x/crypto@v0.51.0/ssh/testdata/keys.go), which the same
	// library we depend on (x/crypto/ssh) parses and verifies against in
	// its own test suite. Not ssh-keygen-generated; see package doc /
	// task report for this caveat.
	testSKEd25519Key = "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIJjzc2a20RjCvN/0ibH6UpGuN9F9hDvD7x182bOesNhHAAAABHNzaDo= user@host"

	// sk-ecdsa-sha2-nistp256@openssh.com blob, same provenance as above
	// (ssh/testdata/keys.go:368-369).
	testSKECDSAKey = "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAIbmlzdHAyNTYAAABBBGRNqlFgED/pf4zXz8IzqA6CALNwYcwgd4MQDmIS1GOtn1SySFObiuyJaOlpqkV5FeEifhxfIC2ejKKtNyO4CysAAAAEc3NoOg== user@host"
)

// --- usage-demonstrating ---

func TestParse_SimplePrincipalKeyTypeAndBlob(t *testing.T) {
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me " + testEd25519Key + "\n"))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)

	e := store.Entries()[0]
	assert.Equal(t, []string{"ben@abbitt.me"}, e.Principals)
	assert.Equal(t, "ssh-ed25519", e.KeyType)
	assert.Nil(t, e.Namespaces, "no namespaces= option present")
	assert.False(t, e.CertAuthority)
	assert.Equal(t, 1, e.Line)
}

func TestParse_NamespacesOption(t *testing.T) {
	line := `releases@ctxloom.dev namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"publish.v1.ctxloom.dev"}, store.Entries()[0].Namespaces)
}

func TestParse_MultipleNamespacesCommaSeparatedInsideQuotes(t *testing.T) {
	line := `ben@abbitt.me namespaces="approve.v1.ctxloom.dev,reject.v1.ctxloom.dev,publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{
		"approve.v1.ctxloom.dev", "reject.v1.ctxloom.dev", "publish.v1.ctxloom.dev",
	}, store.Entries()[0].Namespaces)
}

func TestParse_CommentsAndBlankLinesIgnored(t *testing.T) {
	src := "# a leading comment\n\nben@abbitt.me " + testEd25519Key + "\n\n# trailing comment\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	assert.Len(t, store.Entries(), 1)
}

func TestParse_MultiplePrincipalsCommaSeparated(t *testing.T) {
	line := "alice@example.com,bob@example.com " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, store.Entries()[0].Principals)
}

func TestParse_ValidAfterAndValidBefore(t *testing.T) {
	line := `ben@abbitt.me namespaces="publish.v1.ctxloom.dev",valid-after="20200101",valid-before="20991231" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	e := store.Entries()[0]
	require.NotNil(t, e.ValidAfter)
	require.NotNil(t, e.ValidBefore)
	assert.Equal(t, 2020, e.ValidAfter.Year())
	assert.Equal(t, 2099, e.ValidBefore.Year())
}

func TestParse_CertAuthorityOption(t *testing.T) {
	line := "ca@example.com cert-authority " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.True(t, store.Entries()[0].CertAuthority)
}

func TestParse_HardwareKeyTypes(t *testing.T) {
	src := "sk1@example.com " + testSKEd25519Key + "\n" +
		"sk2@example.com " + testSKECDSAKey + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 2)
	assert.Equal(t, "sk-ssh-ed25519@openssh.com", store.Entries()[0].KeyType)
	assert.Equal(t, "sk-ecdsa-sha2-nistp256@openssh.com", store.Entries()[1].KeyType)
}

func TestParse_TrailingCommentField(t *testing.T) {
	line := "ben@abbitt.me " + testEd25519Key + " some free text comment\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Contains(t, store.Entries()[0].Comment, "some free text comment")
}

// --- edge cases ---

func TestParse_CaseInsensitiveOptionKeyword(t *testing.T) {
	// Verified against real ssh-keygen: NAMESPACES="..." behaves
	// identically to namespaces="...".
	line := `ben@abbitt.me NAMESPACES="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"publish.v1.ctxloom.dev"}, store.Entries()[0].Namespaces)
}

func TestParse_LeadingWhitespaceIndentedLineTolerated(t *testing.T) {
	// Verified against real ssh-keygen.
	line := "   ben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	assert.Len(t, store.Entries(), 1)
}

func TestParse_IndentedCommentStillTreatedAsComment(t *testing.T) {
	// Verified against real ssh-keygen.
	src := "   # indented comment\nben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	assert.Len(t, store.Entries(), 1)
}

func TestParse_CRLFLineEndingsTolerated(t *testing.T) {
	// Verified against real ssh-keygen.
	src := "ben@abbitt.me " + testEd25519Key + "\r\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.NotContains(t, store.Entries()[0].Comment, "\r")
}

// --- fail-closed: malformed lines never produce an Entry ---

func TestParse_GarbageLineIsSkippedNotFatal(t *testing.T) {
	// Verified against real ssh-keygen: it tolerates a garbage line and
	// keeps using the rest of the file. We match this.
	src := "this is not a valid allowed signers line at all\n" +
		"ben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err, "Parse itself never fails for malformed content")
	require.Len(t, perrs, 1)
	assert.Equal(t, 1, perrs[0].Line)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"ben@abbitt.me"}, store.Entries()[0].Principals)
}

func TestParse_UnquotedNamespacesValueIsMalformed(t *testing.T) {
	// Verified against real ssh-keygen: unquoted namespaces=value is
	// rejected outright ("bad options: missing start quote"), not
	// silently treated as unrestricted or as a single-item list.
	line := "ben@abbitt.me namespaces=publish.v1.ctxloom.dev " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_UnquotedValidAfterValueIsMalformed(t *testing.T) {
	// Verified against real ssh-keygen.
	line := `ben@abbitt.me namespaces="publish.v1.ctxloom.dev",valid-after=20200101 ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_UnknownOptionIsMalformed(t *testing.T) {
	// Verified against real ssh-keygen ("bad options: unknown key
	// option"): unrecognized options invalidate the whole entry rather
	// than being silently ignored. This is the stricter, fail-closed
	// choice and it matches real ssh-keygen exactly, so a file that
	// verifies for ssh-keygen never disagrees with us here.
	line := `ben@abbitt.me totally-made-up-option,namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_DuplicateOptionIsMalformed(t *testing.T) {
	// Verified against real ssh-keygen ("bad options: multiple
	// \"namespaces\" clauses").
	line := `ben@abbitt.me namespaces="approve.v1.ctxloom.dev",namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_NoKeyFieldIsMalformed(t *testing.T) {
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_UnrecognizedKeyTypeIsMalformed(t *testing.T) {
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me not-a-real-keytype AAAA==\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_CorruptBase64KeyBlobIsMalformed(t *testing.T) {
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me ssh-ed25519 not-valid-base64!!!\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_EmptyPrincipalInListIsMalformed(t *testing.T) {
	// A dangling comma leaves an empty pattern, which can never mean
	// anything safe — fail closed rather than silently drop it.
	store, perrs, err := Parse(strings.NewReader("alice@example.com,, " + testEd25519Key + "\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries())
}

func TestParse_EntireFileGarbageYieldsEmptyStoreNotError(t *testing.T) {
	src := "garbage one\ngarbage two\ngarbage three\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Len(t, perrs, 3)
	assert.Empty(t, store.Entries(), "an entirely malformed file trusts nothing, fail-closed")
}

func TestParse_EmptyInputYieldsEmptyStore(t *testing.T) {
	store, perrs, err := Parse(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	assert.Empty(t, store.Entries())
}

func TestParseFile_MissingFileReturnsError(t *testing.T) {
	_, _, err := ParseFile("/nonexistent/path/does-not-exist/allowed_signers")
	assert.Error(t, err)
}

// --- the declared key type must match the key blob --------------------------

// U136-F05. golang.org/x/crypto's parseAuthorizedKey base64-decodes the field
// after the first space WITHOUT ever reading the type token, so this package
// granted trust from lines real ssh-keygen calls "invalid key" and refuses to
// verify against. Measured against the ssh-keygen binary: `ssh-rsa <ed25519
// blob>` and `not-a-key-type <ed25519 blob>` both fail there.
//
// The package doc's central invariant is that any divergence from ssh-keygen
// yields STRICTLY LESS trust, never more. This was the one case that went the
// other way.
func TestParse_DeclaredKeyTypeMustMatchTheBlob(t *testing.T) {
	blob := strings.Fields(testEd25519Key)[1]

	for _, declared := range []string{"ssh-rsa", "not-a-key-type", "ecdsa-sha2-nistp256"} {
		line := "ben@abbitt.me " + declared + " " + blob + "\n"
		store, perrs, err := Parse(strings.NewReader(line))
		require.NoError(t, err)
		assert.Empty(t, store.Entries(),
			"a line declaring %q over an ed25519 blob must grant no trust — real ssh-keygen calls it invalid", declared)
		assert.Len(t, perrs, 1, "and it must be REPORTED as dropped, not vanish")
	}
}

// The matching case must keep working, including with options present.
func TestParse_DeclaredKeyTypeMatching_StillParses(t *testing.T) {
	line := `releases@ctxloom.dev namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, "ssh-ed25519", store.Entries()[0].KeyType)
}
