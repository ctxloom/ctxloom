package allowedsigners

import (
	"errors"
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

// TestParse_LeadingByteOrderMarkIsMalformed pins the fail-closed handling of a
// UTF-8 BOM, which several editors prepend when they save a file as "UTF-8".
//
// A BOM is not Unicode whitespace, so TrimSpace leaves it in place and it
// becomes the first byte of the first PRINCIPAL. Nothing downstream survives
// that: MatchesPrincipal stops matching the identity the operator wrote, so
// TrustedAs refuses; `signer untrust <principal>` compares principals
// literally, so the entry cannot be revoked; and `signer show` cannot find it.
// Meanwhile TrustedForNamespace matches on the KEY and keeps granting trust —
// an entry that is live, unnamed and unrevokable.
//
// The remedy is a ParseError, not a silent strip. Stripping would make this
// package match a principal that real `ssh-keygen -Y verify -I <identity>`
// refuses to match, and the package doc promises every divergence from
// ssh-keygen yields strictly LESS trust, never more.
func TestParse_LeadingByteOrderMarkIsMalformed(t *testing.T) {
	src := "\ufeffben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, errByteOrderMark)
	assert.Equal(t, 1, perrs[0].Line)
	// The whole point: no entry, so no invisible unrevokable grant.
	assert.Empty(t, store.Entries())
}

// TestParse_ByteOrderMarkBeforeACommentIsHarmless keeps the fix narrow: a BOM
// in front of a comment or a blank first line contaminates no principal, so it
// must not be reported. Only a BOM that would be absorbed into an entry is one.
func TestParse_ByteOrderMarkBeforeACommentIsHarmless(t *testing.T) {
	src := "\ufeff# my trust root\nben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"ben@abbitt.me"}, store.Entries()[0].Principals)
}

// TestParse_OverlongLineIsSkippedNotFatal pins the line-oriented contract
// against the one input that used to break it wholesale.
//
// Parse's doc promises three times that a line it cannot use is SKIPPED and
// reported, so the file degrades toward less trust one line at a time, and
// that a non-nil error means reading r itself failed. A single line over the
// scanner's 1 MiB buffer broke all three: bufio.Scanner surfaced
// bufio.ErrTooLong as Parse's error return, Parse returned a nil *Store, and
// every caller then discarded the WHOLE location — one oversized junk line
// anywhere in the file revoked every signer in it.
//
// Measured against real ssh-keygen (OpenSSH_10.0p2, this host): an
// allowed_signers whose FIRST line is 2,000,004 bytes of garbage still
// verifies a signature against the good entry on line 2, exit 0. Disarming
// the file is therefore a divergence from ssh-keygen, not a stricter reading
// of it.
func TestParse_OverlongLineIsSkippedNotFatal(t *testing.T) {
	src := "junk" + strings.Repeat("x", 2<<20) + "\nben@abbitt.me " + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err, "an over-long line is a CONTENT error, not an I/O error")
	require.NotNil(t, store)

	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, errLineTooLong)
	assert.Equal(t, 1, perrs[0].Line)
	// ParseError.Text reaches the user through pe.Error() on `signer list`
	// and `signer untrust`; echoing a megabyte of it back is a diagnostic
	// that destroys the terminal it is trying to inform.
	assert.Less(t, len(perrs[0].Text), 1024, "the reported text must be truncated, not the whole line")

	// The rest of the file still counts, and line numbering — which
	// `signer untrust` uses to decide which physical lines to DELETE — is
	// unaffected by the skip.
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"ben@abbitt.me"}, store.Entries()[0].Principals)
	assert.Equal(t, 2, store.Entries()[0].Line)
}

// TestParse_OverlongLineGrantsNoTrust is the fail-closed half: skipping the
// line must not be a way to smuggle one in.
func TestParse_OverlongLineGrantsNoTrust(t *testing.T) {
	_, blob, _ := strings.Cut(testEd25519Key, " ")
	// A line that WOULD have been a perfectly good entry, padded past the
	// limit by its comment field.
	src := "ben@abbitt.me ssh-ed25519 " + blob + " " + strings.Repeat("c", 2<<20) + "\n"
	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, errLineTooLong)
	assert.Empty(t, store.Entries())
}

// TestParseError_EveryCauseIsOneOfThePackageSentinels pins the invariant the
// sentinel block's doc now asserts.
//
// It used to tell CALLERS to use errors.Is against these "rather than matching
// ParseError.Error()'s text" — advice no caller outside the package can take,
// because every sentinel is unexported. The doc now says the opposite: the
// causes are internal, every one of them means "this line grants no trust",
// and the only public facts are the line number and a cause that renders.
//
// This test is what keeps that true. A new failure mode that returns a bare
// fmt.Errorf instead of wrapping a sentinel would make the block's claim false
// without touching it, and would be the first step toward callers matching on
// text.
func TestParseError_EveryCauseIsOneOfThePackageSentinels(t *testing.T) {
	sentinels := []error{
		errNoPrincipals, errNoKey, errUnknownOption, errDuplicateOption,
		errUnquotedValue, errBadTimestamp, errKeyTypeMismatch,
		errByteOrderMark, errLineTooLong, errUnterminatedPrincipalsQuote,
	}
	_, blob, _ := strings.Cut(testEd25519Key, " ")

	src := strings.Join([]string{
		"nokeyhere",                                           // errNoKey
		"a@x ssh-ed25519 not-valid-base64!!!",                 // errNoKey (wrapped)
		",bad@example.com " + testEd25519Key,                  // errNoPrincipals
		"a@x nosuchoption " + testEd25519Key,                  // errUnknownOption
		`a@x namespaces="p",namespaces="q" ` + testEd25519Key, // errDuplicateOption
		"a@x namespaces=unquoted " + testEd25519Key,           // errUnquotedValue
		`a@x valid-after="notadate" ` + testEd25519Key,        // errBadTimestamp
		"a@x not-a-real-keytype " + blob,                      // errKeyTypeMismatch
		"\ufeffa@x " + testEd25519Key,                         // errByteOrderMark
		"junk" + strings.Repeat("x", 2<<20),                   // errLineTooLong
		`"unterminated@x.com ` + testEd25519Key,               // errUnterminatedPrincipalsQuote
	}, "\n") + "\n"

	store, perrs, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	require.Len(t, perrs, 11, "every seeded line must be reported")
	assert.Empty(t, store.Entries(), "no reported line may contribute an entry")

	for _, pe := range perrs {
		matched := 0
		for _, s := range sentinels {
			if errors.Is(pe.Err, s) {
				matched++
			}
		}
		assert.Equal(t, 1, matched,
			"line %d's cause must unwrap to exactly one package sentinel, got %v", pe.Line, pe.Err)
		assert.NotEmpty(t, pe.Err.Error(), "line %d: a cause must render", pe.Line)
	}
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

// TestParse_UnrecognizedKeyTypeIsMalformed pins the key-type/blob agreement
// check, and it must do so with a blob that is otherwise PERFECTLY GOOD.
//
// The blob this used to carry ("AAAA==") is not valid base64, so
// ssh.ParseAuthorizedKey rejected the line before the declared type was ever
// looked at: the test passed for the same reason
// TestParse_CorruptBase64KeyBlobIsMalformed passes, and it would have passed
// against a parser that ignored the key-type token entirely — which is exactly
// the parser this package used to have. A real ed25519 blob under a bogus type
// token is the only input that reaches the check.
func TestParse_UnrecognizedKeyTypeIsMalformed(t *testing.T) {
	_, blob, _ := strings.Cut(testEd25519Key, " ")
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me not-a-real-keytype " + blob + "\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	// The line is rejected for the RIGHT reason: the blob parses, the
	// declared token does not describe it.
	assert.ErrorIs(t, perrs[0].Err, errKeyTypeMismatch)
	assert.Empty(t, store.Entries())
}

// TestParse_MislabelledKeyTypeIsMalformed is the same check with a token that
// is a REAL ssh key type, just not this blob's — the shape an attacker would
// actually use, since golang.org/x/crypto never reads the token and would
// happily hand back a trusted ed25519 key from a line labelled ssh-rsa.
func TestParse_MislabelledKeyTypeIsMalformed(t *testing.T) {
	_, blob, _ := strings.Cut(testEd25519Key, " ")
	store, perrs, err := Parse(strings.NewReader("ben@abbitt.me ssh-rsa " + blob + "\n"))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, errKeyTypeMismatch)
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

// golang.org/x/crypto's parseAuthorizedKey base64-decodes the field
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

// --- quote handling in the principals field ---------------------------------

// TestParse_QuotedPrincipalsField_ExactBugLineSplitsIntoTwoBarePrincipals pins
// the reported bug's exact line, verified against real ssh-keygen
// (OpenSSH_10.0p2) via `ssh-keygen -Y match-principals -I alice@x.com -f
// <file>` and `-I bob@x.com`: both match, exit 0.
//
// Before this fix, this package parsed the line CLEANLY (zero ParseErrors)
// into principals []string{"\"alice@x.com", "bob@x.com\""} — two strings no
// human could type and `signer remove` could never match — while still
// granting publish trust through TrustedForNamespace (which matches on the
// KEY, not the principal). That combination is the bug: this test pins the
// principals a correct parse must produce.
func TestParse_QuotedPrincipalsField_ExactBugLineSplitsIntoTwoBarePrincipals(t *testing.T) {
	line := `"alice@x.com,bob@x.com" namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"alice@x.com", "bob@x.com"}, store.Entries()[0].Principals)
}

// TestParse_QuotedPrincipalsField_EndToEnd_OriginalBugIsDead is the
// outcome-level assertion: not just that principals parse to the right
// strings, but that the trust decision itself now matches ssh-keygen's, and
// that the OLD mangled identity — the one the pre-fix parser produced and
// that nothing could name or revoke — is granted nothing.
func TestParse_QuotedPrincipalsField_EndToEnd_OriginalBugIsDead(t *testing.T) {
	line := `"alice@x.com,bob@x.com" namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)

	key := mustParseKey(t, testEd25519Key)
	now := fixedNow()

	// The real, typeable identities are trusted, as real ssh-keygen verifies
	// them (`-Y verify -I alice@x.com`/`-I bob@x.com` against this exact
	// line, both rc=0).
	assert.True(t, store.TrustedAs("alice@x.com", key, "publish.v1.ctxloom.dev", now).Trusted,
		"real ssh-keygen verifies this identity against this exact line")
	assert.True(t, store.TrustedAs("bob@x.com", key, "publish.v1.ctxloom.dev", now).Trusted,
		"real ssh-keygen verifies this identity against this exact line")

	// The mangled string the OLD parser produced as a "principal" must be
	// trusted as NOTHING — there must be no identity left that grants
	// publish trust and cannot be typed or revoked.
	assert.False(t, store.TrustedAs(`"alice@x.com`, key, "publish.v1.ctxloom.dev", now).Trusted,
		"the pre-fix mangled principal must not be a live identity")
	assert.False(t, store.TrustedAs(`bob@x.com"`, key, "publish.v1.ctxloom.dev", now).Trusted,
		"the pre-fix mangled principal must not be a live identity")
}

// TestParse_QuotedPrincipalWithInternalWhitespace pins the case a plain
// comma-split can never handle: a single quoted principal that itself
// contains whitespace. Verified against real ssh-keygen: `-Y
// match-principals -I "alice smith@x.com"` against a file containing this
// exact quoted field matches it.
func TestParse_QuotedPrincipalWithInternalWhitespace(t *testing.T) {
	line := `"alice smith@x.com" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)
	assert.Equal(t, []string{"alice smith@x.com"}, store.Entries()[0].Principals)
}

// TestParse_UnterminatedPrincipalsQuoteIsMalformed pins the fail-closed
// posture for a quote that never closes: this must be a hard ParseError, NOT
// a silent fallback to splitting the raw, still-quoted text (which would
// reintroduce this package's original bug for a rarer input — a mangled,
// unrevocable "principal" carrying a literal quote character).
//
// Verified against real ssh-keygen: misc.c's strdelim_internal itself
// returns NULL for exactly this ("no matching quote"), which
// parse_principals_key_and_options (sshsig.c) reports as "invalid line" —
// ssh-keygen already refuses this outright rather than falling back to
// anything.
func TestParse_UnterminatedPrincipalsQuoteIsMalformed(t *testing.T) {
	line := `"alice@x.com ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.ErrorIs(t, perrs[0].Err, errUnterminatedPrincipalsQuote)
	assert.Empty(t, store.Entries(), "an unterminated quote must grant no trust at all")
}

// TestParse_QuoteInsideUnquotedFieldIsMalformed pins a stray double quote
// appearing before any principals-list-terminating whitespace, in a field
// that was never meant to be quoted at all. Verified against real ssh-keygen
// (`-Y find-principals`): this exact construction reports "invalid options"
// and matches no principal — the stray quote is consumed as an opening
// quote, its matching close lands on the namespaces= option's own opening
// quote, and everything downstream is garbled into something that fails to
// parse as a key. ctxloom reproduces the same outcome (rejection) by the
// same mechanism (cutPrincipalsField consuming the same quote pair), not by
// a dedicated "reject any quote here" special case.
func TestParse_QuoteInsideUnquotedFieldIsMalformed(t *testing.T) {
	line := `ali"ce@x.com namespaces="publish.v1.ctxloom.dev" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	require.Len(t, perrs, 1)
	assert.Empty(t, store.Entries(), "a stray quote must never garble its way into a granted identity")
}

// TestParse_QuotedPrincipalIsRevocableBySignerRemove proves the round-trip
// this whole fix exists for, at the level this package can prove it without
// depending on internal/operations (this package is a dependency-free leaf
// being extracted as a standalone library — see the package doc — so it
// cannot import the CLI operations package that implements `ctxloom signer
// remove` without creating a cycle).
//
// operations.RemoveSigner's entire mechanism (removeFromAllowedSignersFile in
// internal/operations/signer.go) is: parse the file, then for each entry,
// slices.Contains(entry.Principals, principal) — an exact, literal string
// match against Entry.Principals. So the property that makes a principal
// "revocable by signer remove" is exactly this: the string a human typed
// appears, byte-for-byte, in Principals. That is what this test asserts.
func TestParse_QuotedPrincipalIsRevocableBySignerRemove(t *testing.T) {
	line := `"alice@x.com,bob@x.com" ` + testEd25519Key + "\n"
	store, perrs, err := Parse(strings.NewReader(line))
	require.NoError(t, err)
	assert.Empty(t, perrs)
	require.Len(t, store.Entries(), 1)

	principals := store.Entries()[0].Principals
	// This is literally `slices.Contains(principals, "alice@x.com")` —
	// operations.RemoveSigner's own revocation test, reproduced here.
	assert.Contains(t, principals, "alice@x.com",
		"the exact string an operator would pass to `ctxloom signer remove` must be present")
	assert.Contains(t, principals, "bob@x.com")

	// The mangled strings the pre-fix parser produced must be gone: a
	// `signer remove alice@x.com` against the OLD output would find neither.
	assert.NotContains(t, principals, `"alice@x.com`)
	assert.NotContains(t, principals, `bob@x.com"`)
}

// TestParse_LineNumbersIndexTheSourceStream pins the invariant Entry.Line's
// doc now states, and that operations.RemoveSigner silently depends on.
//
// Line used to be documented "for diagnostics". It is not: RemoveSigner
// re-parses the file it just read and deletes the PHYSICAL lines the matching
// entries name, so an off-by-one here revokes the wrong signer and leaves the
// intended one trusted — and both outcomes are a well-formed file, so nothing
// downstream can notice.
//
// Every line of the input must therefore be counted: blanks, comments, and
// lines that produced a ParseError alike. Counting only entries, or skipping
// unusable lines, would still look right in a file with no gaps in it.
func TestParse_LineNumbersIndexTheSourceStream(t *testing.T) {
	lines := []string{
		"# a comment",                            // 1
		"",                                       // 2
		"first@example.com " + testEd25519Key,    // 3
		"   ",                                    // 4
		"this line is garbage",                   // 5
		"# another comment",                      // 6
		"second@example.com " + testSKEd25519Key, // 7
	}
	store, perrs, err := Parse(strings.NewReader(strings.Join(lines, "\n")))
	require.NoError(t, err)

	entries := store.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, 3, entries[0].Line)
	assert.Equal(t, 7, entries[1].Line, "the last line counts even with no trailing newline")

	require.Len(t, perrs, 1)
	assert.Equal(t, 5, perrs[0].Line)

	// The property RemoveSigner actually relies on, stated directly: for every
	// entry, lines[Line-1] is the line it came from.
	for _, e := range entries {
		require.GreaterOrEqual(t, e.Line, 1)
		require.LessOrEqual(t, e.Line, len(lines))
		assert.Contains(t, lines[e.Line-1], e.Principals[0],
			"Line must index the caller's own lines slice")
	}
}
