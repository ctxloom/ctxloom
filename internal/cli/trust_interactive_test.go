package cli

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestParseItemTrustChoice covers the menu parse: only an explicit t/r acts —
// the same two verbs `ctxloom bundle trust` and `ctxloom bundle reject` are
// spelled with — and everything else (skip, blank, garbage, uppercase
// whitespace) is a no-op, so merely viewing an item can never mutate trust.
func TestParseItemTrustChoice(t *testing.T) {
	cases := map[string]itemTrustChoice{
		"t":       itemTrustGrant,
		"  T  ":   itemTrustGrant,
		"r":       itemTrustReject,
		"R":       itemTrustReject,
		"s":       itemTrustSkip,
		"skip":    itemTrustSkip,
		"":        itemTrustSkip,
		"trust":   itemTrustSkip, // only the single-letter shortcut acts
		"yes":     itemTrustSkip,
		"\n":      itemTrustSkip,
		"garbage": itemTrustSkip,
	}
	for in, want := range cases {
		assert.Equalf(t, want, parseItemTrustChoice(in), "parseItemTrustChoice(%q)", in)
	}
}

// TestApplyItemTrustChoice_Grant: the [t] action records a content-pinned
// approval, identical to `ctxloom bundle trust <ref>`.
func TestApplyItemTrustChoice_Grant(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "trust-me body")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustGrant))
	assert.Contains(t, out.String(), "Approved demo#fragments/x")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(ref), signing.AttestFragmentRaw, []byte("trust-me body")),
		"[t] must record an approval bound to the content bytes")
}

// TestApplyItemTrustChoice_Reject: the [r] action writes BOTH the ref-level
// rejected state and a content-reject countersignature, identical to
// `ctxloom bundle reject <ref>`.
func TestApplyItemTrustChoice_Reject(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/curl-pipe-sh", itemTrustReject))
	assert.Contains(t, out.String(), "Rejected demo#fragments/curl-pipe-sh")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "curl-pipe-sh", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(ref)),
		"[b] must record a ref-level rejected state")
	assert.True(t, store.HasUnsignedContentReject(signing.AttestFragmentRaw, []byte("rm -rf danger")),
		"[b] must record a content-reject over the item's bytes")
}

// TestApplyItemTrustChoice_Skip: skip (and any non-t/b answer) writes nothing —
// viewing never trusts, so the store never records anything for this ref.
func TestApplyItemTrustChoice_Skip(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "look-but-do-not-touch")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustSkip))
	assert.Empty(t, out.String(), "skip must produce no action output")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	store := userApprovalsStore(t)
	assert.False(t, store.HasUnsignedApprove(countersignRefFor(ref), signing.AttestFragmentRaw, []byte("look-but-do-not-touch")),
		"skip must not record any review state")
	assert.False(t, store.HasUnsignedRefReject(countersignRefFor(ref)))
}

// TestShowItem_NonInteractiveStdoutUnchanged proves the TR4 guarantee: in a
// non-interactive environment (go test stdout is not a TTY), `show -i` is
// byte-for-byte identical to `show` and emits no trust UI on stdout. The
// content path is exercised end-to-end through GetConfig/loader via a chdir'd
// temp project.
func TestShowItem_NonInteractiveStdoutUnchanged(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "the fragment body")
	t.Chdir(root) // GetConfig() (config.Load) resolves <root>/.ctxloom

	plain, outPlain := testCmd()
	require.NoError(t, showItem(plain, "demo#fragments/x", ItemTypeFragment, false, false))

	inter, outInter := testCmd()
	require.NoError(t, showItem(inter, "demo#fragments/x", ItemTypeFragment, false, true))

	assert.Equal(t, outPlain.String(), outInter.String(),
		"-i must not change stdout in a non-interactive terminal")
	assert.Contains(t, outPlain.String(), "the fragment body", "content must still be shown")
	for _, leak := range []string{"Effective trust", "[t]rust", "[r]eject"} {
		assert.NotContainsf(t, outInter.String(), leak, "trust UI %q must not leak into piped stdout", leak)
	}
}

// withEmptyStdin points the shared prompt reader at an exhausted stream, so a
// prompt-driving test returns immediately on EOF instead of blocking on a
// terminal that will never answer.
func withEmptyStdin(t *testing.T) {
	t.Helper()
	orig := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(""))
	t.Cleanup(func() { stdinReader = orig })
}

// errCapturingCmd is a command whose stdout AND stderr are both buffers, the
// shape a cobra harness uses to assert on what a command emitted.
func errCapturingCmd() (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.SetOut(&bytes.Buffer{})
	errBuf := &bytes.Buffer{}
	c.SetErr(errBuf)
	return c, errBuf
}

// The interactive trust surface writes to the process's os.Stderr directly
// rather than the writer cobra hands it, so nothing a command harness injects
// can observe it: the ONE surface whose whole job is telling a user what they
// are about to trust cannot be asserted from a command test. Routing it
// through cmd.ErrOrStderr() changes no text and no decision — in production
// ErrOrStderr IS os.Stderr — it only makes the surface observable.
func TestOfferBundleTrust_RendersThroughTheCommandsErrWriter(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	withEmptyStdin(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerBundleTrust(cmd, cfg, "demo", &bundles.Bundle{}))

	assert.Contains(t, errBuf.String(), `Per-item effective trust for bundle "demo"`,
		"the review header must reach the writer the command owns")
}

func TestOfferItemTrust_RendersThroughTheCommandsErrWriter(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	withEmptyStdin(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "body")

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerItemTrust(cmd, cfg, "demo#fragments/x"))

	assert.Contains(t, errBuf.String(), "Effective trust:",
		"the effective-trust stamp must reach the writer the command owns")
}

func TestReviewLocalMCPTrust_RendersThroughTheCommandsErrWriter(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	cmd, errBuf := errCapturingCmd()
	reviewLocalMCPTrust(cmd, cfg, []operations.MCPServerEntry{
		{Name: "local-one", Backend: "claude", Command: "/usr/bin/true"},
	})

	out := errBuf.String()
	assert.Contains(t, out, "Effective trust (claude scope):")
	assert.Contains(t, out, "ctxloom bundle trust|reject <bundle>#mcp/<name>")
}

// withFailingStdin points the shared prompt reader at a terminal whose reads
// FAULT rather than end (failingReader, run_oneshot_prompt_test.go, returns
// assert.AnError, never io.EOF). It is precisely the case a user pressing
// Ctrl-D is not.
func withFailingStdin(t *testing.T) {
	t.Helper()
	orig := stdinReader
	stdinReader = bufio.NewReader(failingReader{})
	t.Cleanup(func() { stdinReader = orig })
}

func hookedBundle() *bundles.Bundle {
	return &bundles.Bundle{Hooks: bundles.BundleHooks{
		PreTool: []bundles.BundleHook{
			{Command: "echo one", Type: "command"},
			{Command: "echo two", Type: "command"},
			{Command: "echo three", Type: "command"},
		},
	}}
}

// A terminal read that FAULTS and a user pressing Ctrl-D produced the exact
// same outcome: return nil, caller reports success, not one word said. The
// trust posture is right either way — viewing never trusts — but the user is
// entitled to know their answer was never read. Ctrl-D is deliberate and stays
// silent; an I/O fault is not, and must say so.
func TestOfferItemTrust_ReadFaultIsReportedButStillTrustsNothing(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "body")
	withFailingStdin(t)

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerItemTrust(cmd, cfg, "demo#fragments/x"),
		"a read fault must not become an error: viewing never trusts, and it never fails either")
	assert.Contains(t, errBuf.String(), assert.AnError.Error(),
		"a read that faulted must not be silently reported as a skip the user chose")

	store := userApprovalsStore(t)
	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	assert.False(t, store.HasUnsignedApprove(countersignRefFor(ref), signing.AttestFragmentRaw, []byte("body")),
		"a faulted prompt must never be read as consent")
}

// Ctrl-D is an answer — "stop asking me" — so it must NOT be dressed up as a
// fault. This is the guard that stops the fix above becoming noise.
func TestOfferItemTrust_EOFStaysSilent(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "body")
	withEmptyStdin(t)

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerItemTrust(cmd, cfg, "demo#fragments/x"))
	assert.NotContains(t, errBuf.String(), "warning",
		"an intentional Ctrl-D is not a fault and must not warn")
}

// The hook walk is where the silence costs most: a read fault at hook 1 of 3
// abandons hooks 2 and 3 and returns nil, so a user who asked to review every
// executable surface in a bundle reviewed one and was told the review
// completed. Every unreviewed hook stays withheld — the posture is right — but
// the count must be said out loud.
func TestOfferBundleHookTrust_ReadFaultNamesTheHooksItNeverAsked(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	withFailingStdin(t)

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerBundleHookTrust(cmd, cfg, "demo", hookedBundle()))

	out := errBuf.String()
	assert.Contains(t, out, assert.AnError.Error())
	assert.Contains(t, out, "3 hook(s) not reviewed",
		"the abandoned remainder must be counted, not implied by silence")
}

// Ctrl-D partway through the hook walk is deliberate, so it does not warn —
// but the user still has to be told how much of the bundle went unreviewed.
func TestOfferBundleHookTrust_EOFStillReportsTheUnreviewedRemainder(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	withEmptyStdin(t)

	cmd, errBuf := errCapturingCmd()
	require.NoError(t, offerBundleHookTrust(cmd, cfg, "demo", hookedBundle()))

	out := errBuf.String()
	assert.NotContains(t, out, "warning")
	assert.Contains(t, out, "3 hook(s) not reviewed")
}
