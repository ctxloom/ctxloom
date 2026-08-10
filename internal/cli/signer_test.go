package cli

import (
	"bufio"
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

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
)

func testSignerKeyLine(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// TestRunSignerAdd_YesFlagSkipsPromptAndWrites drives the full CLI-layer
// `signer add` path (confirmation → operations.AddSigner) with --yes so no
// TTY is needed, then verifies the entry actually landed via
// operations.ShowSigner — the same round trip operations/signer_test.go
// already proves cryptographically; this test is about the CLI wiring
// (flag plumbing, confirmation gate, output).
func TestRunSignerAdd_YesFlagSkipsPromptAndWrites(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, out := testCmd()
	err := runSignerAdd(cmd, cfg, "team@example.com", line, nil, "", true, true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Trusted team@example.com")

	found, err := operations.ShowSigner(cfg, "team@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, []string{signing.NamespacePublish}, found[0].Entry.Namespaces)
}

func TestRunSignerAdd_NonInteractiveProceedsWithoutYesFlag(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	// project=false below writes to the USER store (~/.ctxloom/allowed_signers)
	// — redirect HOME to a throwaway dir so this test can NEVER touch the
	// real developer's home directory, regardless of who runs it or where.
	t.Setenv("HOME", t.TempDir())
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	// isInteractiveTerminal() is false in the test process (no TTY), so this
	// must proceed even without --yes — the confirmation is TTY-gated the
	// same way the trust-review menus are.
	err := runSignerAdd(cmd, cfg, "ci@example.com", line, []string{"approve"}, "", false, false)
	require.NoError(t, err)

	found, err := operations.ShowSigner(cfg, "ci@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, []string{signing.NamespaceApprove}, found[0].Entry.Namespaces)
}

func TestRunSignerAdd_ProjectFlagWritesProjectStore(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	require.NoError(t, runSignerAdd(cmd, cfg, "org@example.com", line, nil, "", true, true))

	found, err := operations.ShowSigner(cfg, "org@example.com", nil)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "project", found[0].Source)
}

func TestRunSignerAdd_BadNamespaceIsUsageError(t *testing.T) {
	_, cfg := setupSignTestDir(t)
	line := testSignerKeyLine(t)

	cmd, _ := testCmd()
	err := runSignerAdd(cmd, cfg, "x@example.com", line, []string{"bogus"}, "", true, true)
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

// --- the trust-consequence prompt -----------------------------------------

// feedPromptStdin points the CLI's shared stdin reader (prompt.go's stdinReader,
// the one primitive every interactive prompt funnels through) at a canned
// answer for the duration of one test, restoring the real one afterwards. It
// is the existing seam — no production code changes to make prompts readable —
// and the reason these tests must not run in parallel with each other.
func feedPromptStdin(t *testing.T, answer string) {
	t.Helper()
	saved := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(answer))
	t.Cleanup(func() { stdinReader = saved })
}

// testSignerKeyInfo builds a real parsed SignerKeyInfo (real ssh key, real
// SHA256 fingerprint) so the prompt assertions below are about bytes the
// command actually renders, not a hand-written placeholder.
func testSignerKeyInfo(t *testing.T) operations.SignerKeyInfo {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(testSignerKeyLine(t)))
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

// TestPromptSignerAdd_ShowsFingerprintRoleAndConsequence pins the prompt.
// The prompt this renders is the most consequential confirmation in the
// product — it is the moment a user grants a key the right to reach their
// agent unreviewed, forever — and until this test nothing asserted it is
// actually shown: it wrote to os.Stderr directly, and every existing test took
// either the --yes or the non-interactive path, both of which skip it
// entirely. Deleting the whole Fprintf would have kept the suite green.
//
// It now writes to cmd.ErrOrStderr(), so this asserts the three things a user
// needs in order to make the decision at all: the FINGERPRINT they are told to
// verify out of band, the ROLE word naming how broad the grant is, and the
// CONSEQUENCE sentence naming what it lets through.
func TestPromptSignerAdd_ShowsFingerprintRoleAndConsequence(t *testing.T) {
	key := testSignerKeyInfo(t)

	t.Run("a publish grant is named as a PUBLISHER grant", func(t *testing.T) {
		feedPromptStdin(t, "y\n")
		cmd, _ := testCmd()
		var errBuf bytes.Buffer
		cmd.SetErr(&errBuf)

		assert.True(t, promptSignerAdd(cmd, "context@acme.com", key, []string{signing.NamespacePublish}),
			`an explicit "y" is a yes`)

		lines := promptLines(errBuf.String())
		assert.Contains(t, lines, "Trust context@acme.com as a PUBLISHER?",
			"the header must name the principal and the role")
		assert.Contains(t, lines, fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()),
			"the fingerprint the user is told to verify, and its key type, must actually be shown")
		assert.Contains(t, lines, signerPromptVerifyLine,
			"the instruction that makes the fingerprint useful must be shown with it")
		assert.Contains(t, lines, signerPublishConsequenceLine1,
			"the publish consequence must be shown, not merely computed — and it must say executables")
		assert.Contains(t, lines, signerPublishConsequenceLine2,
			"including the WITHOUT REVIEW clause, which is the whole point of asking")
		assert.NotContains(t, lines, signerApproveConsequenceLine2,
			"a publish grant must not be described with the narrower review-delegation wording")
	})

	t.Run("an approve-only grant is named as a REVIEWER grant", func(t *testing.T) {
		feedPromptStdin(t, "y\n")
		cmd, _ := testCmd()
		var errBuf bytes.Buffer
		cmd.SetErr(&errBuf)

		assert.True(t, promptSignerAdd(cmd, "lead@team.example", key, []string{signing.NamespaceApprove}))

		lines := promptLines(errBuf.String())
		assert.Contains(t, lines, "Trust lead@team.example as a REVIEWER?")
		assert.Contains(t, lines, fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()))
		assert.Contains(t, lines, signerPromptVerifyLine)
		assert.Contains(t, lines, signerApproveConsequenceLine1)
		assert.Contains(t, lines, signerApproveConsequenceLine2)
		assert.NotContains(t, lines, signerPublishConsequenceLine2,
			"a review-delegation grant must not borrow the broader publish consequence")
	})

	// Nothing but an explicit yes may add a signer: a bare newline, an
	// unrecognised answer, and a closed stdin all leave the key untrusted.
	for name, answer := range map[string]string{
		"an explicit no":       "n\n",
		"a bare newline":       "\n",
		"an unrecognised word": "maybe\n",
		"a closed stdin":       "",
	} {
		t.Run(name+" is not consent", func(t *testing.T) {
			feedPromptStdin(t, answer)
			cmd, _ := testCmd()
			var errBuf bytes.Buffer
			cmd.SetErr(&errBuf)

			assert.False(t, promptSignerAdd(cmd, "context@acme.com", key, []string{signing.NamespacePublish}))
			assert.Contains(t, promptLines(errBuf.String()),
				fmt.Sprintf("%s  (%s)", key.Fingerprint, key.PublicKey.Type()),
				"the prompt is still shown before the answer is read")
		})
	}
}

// TestConfirmSignerAdd_YesFlagAsksNothing keeps the gate promptSignerAdd was
// split out from pinned: --yes must not render the prompt at all (and must not
// consume the answer stdin is holding for something else).
func TestConfirmSignerAdd_YesFlagAsksNothing(t *testing.T) {
	cmd, _ := testCmd()
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	assert.True(t, confirmSignerAdd(cmd, "context@acme.com", testSignerKeyInfo(t), []string{signing.NamespacePublish}, true))
	assert.Empty(t, errBuf.String(), "--yes skips the confirmation entirely")
}

// --- printSignerListings --------------------------------------------------

func TestPrintSignerListings_EmptyReportsNone(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, printSignerListings(&buf, nil))
	assert.Contains(t, buf.String(), "no trusted signers")
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
