package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

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

// --- printSignerListings --------------------------------------------------

func TestPrintSignerListings_EmptyReportsNone(t *testing.T) {
	var buf strings.Builder
	require.NoError(t, printSignerListings(&buf, nil))
	assert.Contains(t, buf.String(), "no trusted signers")
}
