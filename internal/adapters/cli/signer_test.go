package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

func testSignerKeyLine(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// TestRunSignerTrust_YesFlagWrites drives the full CLI-layer `signer trust`
// path (--yes → operations.AddSigner), then verifies the entry actually landed via
// operations.ShowSigner — the same round trip operations/signer_test.go
// already proves cryptographically; this test is about the CLI wiring
// (flag plumbing, the --yes gate, output).
func TestRunSignerTrust_YesFlagWrites(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, out := testCmd()
	err := runSignerTrust(cmd, cfg, "team@example.com", line, nil, "", true, true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Trusted team@example.com")

	found, err := operations.ShowSigner(cfg, "team@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, []string{signing.NamespacePublish}, found[0].Entry.Namespaces)
}

// TestRunSignerTrust_WithoutYesShowsTheKeyAndTrustsNothing: there is no
// confirmation prompt. Without --yes the command DISCLOSES what trusting
// would mean — the fingerprint to verify and the consequence — then fails
// naming --yes, and writes nothing; it never assumes an answer, on a terminal
// or off one.
func TestRunSignerTrust_WithoutYesShowsTheKeyAndTrustsNothing(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	t.Setenv("HOME", t.TempDir())
	line := testSignerKeyLine(t)
	key := testSignerKeyInfo(t, line)

	cmd, _ := testCmd()
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	err := runSignerTrust(cmd, cfg, "ci@example.com", line, []string{"approve"}, "", false, false)
	require.ErrorIs(t, err, errSignerTrustNeedsYes)
	assert.Contains(t, err.Error(), "--yes", "the failure names the flag that applies it")
	assert.Contains(t, promptLines(errBuf.String()), fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()))

	found, err := operations.ShowSigner(cfg, "ci@example.com", nil)
	require.NoError(t, err)
	assert.Empty(t, found, "without --yes nothing is trusted")
}

func TestRunSignerTrust_ProjectFlagWritesProjectStore(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	require.NoError(t, runSignerTrust(cmd, cfg, "org@example.com", line, nil, "", true, true))

	found, err := operations.ShowSigner(cfg, "org@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "project", found[0].Source)
}

// TestRunSignerTrust_OutsideProjectFallsBackAndSaysSo is the edge `signer
// trust`'s project-by-default posture must handle: run outside a project
// (cfg carries no .ctxloom directory), the write must not fail. It falls
// back to the user store and the output says so — WHICH store it used and
// WHY — rather than silently landing somewhere the user did not expect.
func TestRunSignerTrust_OutsideProjectFallsBackAndSaysSo(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{}) // no AppPaths: outside a project
	t.Setenv("HOME", t.TempDir())
	line := testSignerKeyLine(t)

	cmd, out := testCmd()
	require.NoError(t, runSignerTrust(cmd, cfg, "solo@example.com", line, nil, "", true, true))

	output := out.String()
	assert.Contains(t, output, "no project", "the output must say WHY it fell back")
	assert.Contains(t, output, "user store", "the output must name WHICH store it used")
	assert.Contains(t, output, "Trusted solo@example.com")

	found, err := operations.ShowSigner(cfg, "solo@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "user", found[0].Source, "outside a project, the entry must land in the user store")
}

// TestRunSignerTrust_InsideProjectNeverFallsBack is the fallback's negative
// case: WITH a project configured, the default write must land in the
// project store and print no fallback explanation at all — a message that
// only belongs on the edge case must not leak into the common path.
func TestRunSignerTrust_InsideProjectNeverFallsBack(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	t.Setenv("HOME", t.TempDir())
	line := testSignerKeyLine(t)

	cmd, out := testCmd()
	require.NoError(t, runSignerTrust(cmd, cfg, "team@example.com", line, nil, "", true, true))

	assert.NotContains(t, out.String(), "no project", "a configured project must never print the fallback explanation")

	found, err := operations.ShowSigner(cfg, "team@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "project", found[0].Source)
}

// TestRunSignerUntrust_ProjectFlagRemovesFromProjectStore proves `signer
// untrust`'s CLI-layer wiring (effectiveSignerProject -> RemoveSigner) — the
// same round trip operations/signer_test.go already proves cryptographically;
// this test is about the CLI wiring, mirroring
// TestRunSignerTrust_ProjectFlagWritesProjectStore for the untrust verb.
func TestRunSignerUntrust_ProjectFlagRemovesFromProjectStore(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	require.NoError(t, runSignerTrust(cmd, cfg, "team@example.com", line, nil, "", true, true))
	found, err := operations.ShowSigner(cfg, "team@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "project", found[0].Source, "sanity: the entry must be seeded into the project store")

	cmd2, out := testCmd()
	require.NoError(t, runSignerUntrust(cmd2, cfg, "team@example.com", true))
	assert.Contains(t, out.String(), "removed 1 entry for team@example.com")

	found, err = operations.ShowSigner(cfg, "team@example.com", nil)
	require.NoError(t, err)
	assert.Empty(t, found, "the project-store entry must actually be gone")
}

// TestRunSignerUntrust_OutsideProjectFallsBackAndSaysSo is untrust's mirror of
// TestRunSignerTrust_OutsideProjectFallsBackAndSaysSo — the CLI-level pin for
// the ruling this change implements: `signer untrust` run outside a project
// must fall back to the user store and say so, exactly like `signer trust`.
func TestRunSignerUntrust_OutsideProjectFallsBackAndSaysSo(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{}) // no AppPaths: outside a project
	t.Setenv("HOME", t.TempDir())
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	require.NoError(t, runSignerTrust(cmd, cfg, "solo@example.com", line, nil, "", true, true))

	cmd2, out := testCmd()
	require.NoError(t, runSignerUntrust(cmd2, cfg, "solo@example.com", true))

	output := out.String()
	assert.Contains(t, output, "no project", "the output must say WHY it fell back")
	assert.Contains(t, output, "user store", "the output must name WHICH store it used")
}

// --- signer trust/untrust's shared --project/--user flag wiring ------------
//
// effectiveSignerProject is the pure resolution function both runSignerTrustCmd
// and runSignerUntrustCmd call to turn the two flags into the single
// `project` bool AddSigner/RemoveSigner take; tested directly, independent
// of cobra flag parsing. The flags' DEFAULT VALUES are tested against the
// live cobra.Command below — that is the one property a pure-function test
// cannot see.

func TestEffectiveSignerProject_DefaultIsProjectStore(t *testing.T) {
	assert.True(t, effectiveSignerProject(true, false), "project defaults true, user defaults false: the default posture is the project store")
}

func TestEffectiveSignerProject_UserFlagOverridesProject(t *testing.T) {
	assert.False(t, effectiveSignerProject(true, true), "--user must win even though --project is still true by default")
}

func TestEffectiveSignerProject_UserAlone(t *testing.T) {
	assert.False(t, effectiveSignerProject(false, true))
}

func TestSignerTrustCmd_ProjectFlagDefaultsTrue(t *testing.T) {
	f := signerTrustCmd.Flags().Lookup("project")
	require.NotNil(t, f, "`signer trust` must still carry --project")
	assert.Equal(t, "true", f.DefValue, "the project store is now the default write target")
}

func TestSignerTrustCmd_HasUserFlagDefaultingFalse(t *testing.T) {
	f := signerTrustCmd.Flags().Lookup("user")
	require.NotNil(t, f, "`signer trust` needs the inverse of --project for the per-machine case")
	assert.Equal(t, "false", f.DefValue)
}

// TestSignerUntrustCmd_ProjectFlagDefaultsTrue is the decisive pin for the
// scope-symmetry fix: `signer untrust` used to default --project to false
// (the user store) while `signer trust` defaulted it to true (the project
// store) — the destructive verb had the wider, more permanent blast radius
// by default. Both must now agree.
func TestSignerUntrustCmd_ProjectFlagDefaultsTrue(t *testing.T) {
	f := signerUntrustCmd.Flags().Lookup("project")
	require.NotNil(t, f, "`signer untrust` must still carry --project")
	assert.Equal(t, "true", f.DefValue, "the project store is now the default write target, matching `signer trust`")
}

func TestSignerUntrustCmd_HasUserFlagDefaultingFalse(t *testing.T) {
	f := signerUntrustCmd.Flags().Lookup("user")
	require.NotNil(t, f, "`signer untrust` needs the inverse of --project for the per-machine case, mirroring `signer trust`")
	assert.Equal(t, "false", f.DefValue)
}

func TestRunSignerTrust_BadNamespaceIsUsageError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	err := runSignerTrust(cmd, cfg, "x@example.com", line, []string{"bogus"}, "", true, true)
	require.Error(t, err)
}

// --- consequence text / role word --------------------------------------

func TestSignerRoleWord_PublishIsPublisher(t *testing.T) {
	assert.Equal(t, "PUBLISHER", signerRoleWord([]string{signing.NamespacePublish}))
	assert.Equal(t, "PUBLISHER", signerRoleWord([]string{signing.NamespaceApprove, signing.NamespacePublish}))
}

func TestSignerRoleWord_ApproveOnlyIsReviewer(t *testing.T) {
	assert.Equal(t, "REVIEWER", signerRoleWord([]string{signing.NamespaceApprove}))
	assert.Equal(t, "REVIEWER", signerRoleWord([]string{signing.NamespaceReject}))
}

func TestSignerConsequenceText_NamesConcreteConsequence(t *testing.T) {
	publish := signerConsequenceText([]string{signing.NamespacePublish})
	assert.Contains(t, publish, "WITHOUT REVIEW")
	assert.Contains(t, publish, "executables")

	approve := signerConsequenceText([]string{signing.NamespaceApprove})
	assert.Contains(t, approve, "delegating your review decisions")
}

// --- the trust-consequence disclosure ------------------------------------

// testSignerKeyInfo builds a real parsed SignerKeyInfo (real ssh key, real
// SHA256 fingerprint) so the prompt assertions below are about bytes the
// command actually renders, not a hand-written placeholder.
func testSignerKeyInfo(t *testing.T, line string) operations.SignerKeyInfo {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)
	return operations.SignerKeyInfo{PublicKey: pub, Fingerprint: ssh.FingerprintSHA256(pub)}
}

// promptLines splits rendered prompt output into its non-blank lines with the
// indentation stripped, so the assertions below are MEMBERSHIP tests over a
// parsed slice — exact line equality — rather than substring checks against a
// whole buffer. A substring check on a dump passes on accidental containment:
// a sentence softened but still carrying the fragment being matched, a role
// word that is a substring of its replacement. This prompt is the one place in
// the product where that kind of false green is least acceptable.
func promptLines(rendered string) []string {
	var lines []string
	for _, line := range strings.Split(rendered, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// The exact lines the confirmation must show, as named constants so every
// assertion below matches a whole line rather than a fragment of one.
// TestSignerPromptPins_AreTheProductionSentences ties them back to the
// production text so they cannot rot into a stale copy of a changed prompt.
const (
	signerPromptVerifyLine = "Verify this fingerprint out of band before you continue."

	signerPublishConsequenceLine1 = "Everything this signer ever publishes — text AND executables (MCP servers,"
	signerPublishConsequenceLine2 = "hooks), now and in every future update — will reach your agent WITHOUT REVIEW."

	signerApproveConsequenceLine1 = "Everything this signer ever approves reaches your agent unreviewed —"
	signerApproveConsequenceLine2 = "you are delegating your review decisions to them, forever."
)

// TestSignerPromptPins_AreTheProductionSentences keeps the constants above
// honest: they are signerConsequenceText's own output, line for line. Without
// this, a reworded consequence would only fail the membership assertions with
// no indication that the expectations themselves were the stale side.
func TestSignerPromptPins_AreTheProductionSentences(t *testing.T) {
	assert.Equal(t,
		[]string{signerPublishConsequenceLine1, signerPublishConsequenceLine2},
		promptLines(signerConsequenceText([]string{signing.NamespacePublish})))
	assert.Equal(t,
		[]string{signerApproveConsequenceLine1, signerApproveConsequenceLine2},
		promptLines(signerConsequenceText([]string{signing.NamespaceApprove})))
}

// disclosedSignerTrust runs `signer trust` WITHOUT --yes and returns the
// block it disclosed on stderr, as parsed lines, and the key it was given.
func disclosedSignerTrust(t *testing.T, principal string, namespaces []string) ([]string, operations.SignerKeyInfo) {
	t.Helper()
	_, cfg := setupSignTestDir(t)
	t.Setenv("HOME", t.TempDir())
	cmd, _ := testCmd()
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	line := testSignerKeyLine(t)
	require.ErrorIs(t, runSignerTrust(cmd, cfg, principal, line, namespaces, "", true, false), errSignerTrustNeedsYes)
	return promptLines(errBuf.String()), testSignerKeyInfo(t, line)
}

// TestSignerTrustDisclosure_ShowsFingerprintRoleAndConsequence pins the
// disclosure. It is the most consequential text in the product — the moment
// a user grants a key the right to reach their agent unreviewed, forever —
// so it asserts the three things a user needs in order to decide at all:
// the FINGERPRINT to verify out of band, the ROLE word naming how broad the
// grant is, and the CONSEQUENCE sentence naming what it lets through.
func TestSignerTrustDisclosure_ShowsFingerprintRoleAndConsequence(t *testing.T) {
	t.Run("a publish grant is named as a PUBLISHER grant", func(t *testing.T) {
		lines, key := disclosedSignerTrust(t, "context@acme.com", nil)
		assert.Contains(t, lines, "Trust context@acme.com as a PUBLISHER",
			"the header must name the principal and the role")
		assert.Contains(t, lines, fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()),
			"the fingerprint the user is told to verify, and its key type, must actually be shown")
		assert.Contains(t, lines, signerPromptVerifyLine,
			"the instruction that makes the fingerprint useful must be shown with it")
		assert.Contains(t, lines, signerPublishConsequenceLine1,
			"the publish consequence must be shown, not merely computed — and it must say executables")
		assert.Contains(t, lines, signerPublishConsequenceLine2,
			"including the WITHOUT REVIEW clause")
		assert.NotContains(t, lines, signerApproveConsequenceLine2,
			"a publish grant must not be described with the narrower review-delegation wording")
	})

	t.Run("an approve-only grant is named as a REVIEWER grant", func(t *testing.T) {
		lines, key := disclosedSignerTrust(t, "lead@team.example", []string{"approve"})
		assert.Contains(t, lines, "Trust lead@team.example as a REVIEWER")
		assert.Contains(t, lines, fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()))
		assert.Contains(t, lines, signerPromptVerifyLine)
		assert.Contains(t, lines, signerApproveConsequenceLine1)
		assert.Contains(t, lines, signerApproveConsequenceLine2)
		assert.NotContains(t, lines, signerPublishConsequenceLine2,
			"a review-delegation grant must not borrow the broader publish consequence")
	})
}

// TestRunSignerTrust_YesTrustsWithoutTheBlock: --yes applies; the block is
// the preview, so the applying run does not repeat it (its result line
// carries the fingerprint).
func TestRunSignerTrust_YesTrustsWithoutTheBlock(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	cmd, _ := testCmd()
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	require.NoError(t, runSignerTrust(cmd, cfg, "context@acme.com", testSignerKeyLine(t), nil, "", true, true))
	assert.Empty(t, errBuf.String())
}

// --- printSignerListings --------------------------------------------------

func TestPrintSignerListings_EmptyReportsNone(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, printSignerListings(&buf, nil))
	assert.Contains(t, buf.String(), "no trusted signers")
}

// hostilePrincipal is a signer principal carrying the bytes a terminal would
// obey: a cursor-up + erase-line pair (rewrites the line above), a carriage
// return (overwrites the current line from column 0) and a backspace. The
// escaped form is what the reader must see instead — caret notation, so no
// byte is lost and the forgery attempt is itself visible.
const (
	hostilePrincipal        = "evil@example.com\x1b[1A\x1b[2K\rtrusted@acme.com\x08"
	hostilePrincipalEscaped = "evil@example.com^[[1A^[[2K^Mtrusted@acme.com^H"
)

// TestSignerTrustDisclosure_PrincipalControlBytesAreEscaped covers the
// sharpest display path in the product: the "Trust X as a PUBLISHER" line,
// where X is supplied by the entity seeking trust. Control bytes reaching the
// terminal here can rewrite the very line the operator is reading to decide.
func TestSignerTrustDisclosure_PrincipalControlBytesAreEscaped(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	t.Setenv("HOME", t.TempDir())
	cmd, _ := testCmd()
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	_ = runSignerTrust(cmd, cfg, hostilePrincipal, testSignerKeyLine(t), nil, "", true, false)

	out := errBuf.String()
	assert.Contains(t, out, "Trust "+hostilePrincipalEscaped+" as a PUBLISHER",
		"the principal must render in caret form, on its own line, losing no bytes")
	assert.NotContains(t, out, "\x1b", "no raw ESC may reach the terminal")
	assert.NotContains(t, out, "\r", "no raw CR may reach the terminal")
	assert.NotContains(t, out, "\x08", "no raw backspace may reach the terminal")
}

// TestPrintSignerListings_PublisherFieldsAreEscaped covers the listing: the
// principal and namespaces come from an allowed_signers file that a pulled
// remote or an embedded root can have authored, and an unreadable line is
// echoed verbatim by design. All three must reach the terminal inert.
func TestPrintSignerListings_PublisherFieldsAreEscaped(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, printSignerListings(&buf, []operations.SignerListing{
		{
			Entry: allowedsigners.Entry{
				Principals: []string{hostilePrincipal},
				Namespaces: []string{"ctxloom-publish\x1b[2K"},
			},
			Source:      "user",
			Fingerprint: "SHA256:abc",
		},
		{Unreadable: "line 3: bad key\x1b[1A", Source: "project"},
	}))

	out := buf.String()
	assert.Contains(t, out, hostilePrincipalEscaped)
	assert.Contains(t, out, "ctxloom-publish^[[2K")
	assert.Contains(t, out, "line 3: bad key^[[1A")
	assert.NotContains(t, out, "\x1b", "no raw ESC may reach the terminal")
	assert.NotContains(t, out, "\r")
	assert.NotContains(t, out, "\x08")
}

// TestSignerConsequenceText_PublishWinsInAMixedGrant completes the pin on the
// publish-conditional both signerRoleWord and signerConsequenceText open-code:
// a grant that includes publish alongside approve/reject is a PUBLISH grant,
// and must be described with the broader, more dangerous consequence rather
// than the delegated-review one. Order in the namespace slice must not
// change the answer.
func TestSignerConsequenceText_PublishWinsInAMixedGrant(t *testing.T) {
	for _, ns := range [][]string{
		{signing.NamespacePublish, signing.NamespaceApprove},
		{signing.NamespaceApprove, signing.NamespacePublish},
		{signing.NamespaceReject, signing.NamespacePublish, signing.NamespaceApprove},
	} {
		assert.Contains(t, signerConsequenceText(ns), "executables",
			"a grant including publish must carry the publish consequence: %v", ns)
		assert.Equal(t, "PUBLISHER", signerRoleWord(ns),
			"role word and consequence text must agree on the same scan: %v", ns)
	}
}

// The deprecated top-level `sign`/`signer *` aliases are DELETED (verb-spine
// reorg §6), so TestDeprecatedAliasFlagsMatchTheirRealHome — which pinned the
// alias/real-home flag sets against drift — has no subject left. What replaces
// it is the one remaining invariant: the canonical trust-signer leaves carry
// the flags themselves, and nothing in the tree is deprecated any more.
func TestTrustSignerLeavesCarryTheirOwnFlags(t *testing.T) {
	require.NotNil(t, signerTrustCmd.Flags().Lookup("key"),
		"`signer trust` owns --key; it used to be registered twice, once per spelling")
	require.NotNil(t, signerUntrustCmd.Flags().Lookup("project"),
		"`signer untrust` owns --project")
	for _, c := range []*cobra.Command{signerTrustCmd, signerListCmd, signerShowCmd, signerUntrustCmd, bundleSignCmd} {
		assert.Empty(t, c.Deprecated, "%s: no leaf in the reorged tree is deprecated", c.Name())
	}
}
