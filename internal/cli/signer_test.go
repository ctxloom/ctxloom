package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// TestSignerConsequenceText_PublishWinsInAMixedGrant completes the pin on the
// publish-conditional both signerRoleWord and signerConsequenceText open-code
// (U042-F13's sibling, U042-F12): a grant that includes publish alongside
// approve/reject is a PUBLISH grant, and must be described with the broader,
// more dangerous consequence rather than the delegated-review one. Order in
// the namespace slice must not change the answer.
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

// TestDeprecatedAliasFlagsMatchTheirRealHome is U042-F09's pin. The row's
// observation is right — seven package-global flag vars are each bound into two
// different cobra commands (the deprecated top-level alias and its real home) —
// but the consequence it names does not follow: one process parses flags for
// exactly one matched command, so the shared address is never written twice in
// a single run. The live hazard is DRIFT: the alias and its real home declare
// their flags at two separate call sites, so a flag added, renamed, defaulted
// differently, or marked required on one silently diverges from the other and
// the deprecated command starts behaving differently from the one users are
// being told to switch to. That is what this pins.
func TestDeprecatedAliasFlagsMatchTheirRealHome(t *testing.T) {
	// Only the command's OWN flags: cobra merges a parent's persistent flags
	// into Flags() lazily, the first time the command is executed, so an
	// unfiltered comparison would depend on which tests ran earlier in the
	// process. "help" is cobra's own injected flag for the same reason.
	describe := func(c *cobra.Command) map[string]string {
		inherited := c.InheritedFlags()
		out := map[string]string{}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" || inherited.Lookup(f.Name) != nil {
				return
			}
			out[f.Name] = fmt.Sprintf("shorthand=%q default=%q usage=%q annotations=%v",
				f.Shorthand, f.DefValue, f.Usage, f.Annotations)
		})
		return out
	}
	for _, p := range []struct {
		name        string
		alias, home *cobra.Command
	}{
		{"sign", signCmd, bundleSignCmd},
		{"signer add", signerAddCmd, trustSignerAddCmd},
		{"signer list", signerListCmd, trustSignerListCmd},
		{"signer show", signerShowCmd, trustSignerShowCmd},
		{"signer remove", signerRemoveCmd, trustSignerRemoveCmd},
	} {
		assert.Equal(t, describe(p.home), describe(p.alias),
			"%s: the deprecated alias must accept exactly the flags its real home does", p.name)
		assert.NotEmpty(t, p.alias.Deprecated, "%s: the alias must still point at its new home", p.name)
		assert.Empty(t, p.home.Deprecated, "%s: the real home is not deprecated", p.name)
	}
}
