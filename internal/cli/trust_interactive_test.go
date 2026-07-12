package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestParseItemTrustChoice covers the menu parse: only an explicit t/b acts;
// everything else (skip, blank, garbage, uppercase whitespace) is a no-op, so
// merely viewing an item can never mutate trust.
func TestParseItemTrustChoice(t *testing.T) {
	cases := map[string]itemTrustChoice{
		"t":       itemTrustGrant,
		"  T  ":   itemTrustGrant,
		"b":       itemTrustBlacklist,
		"B":       itemTrustBlacklist,
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
// approval, identical to `ctxloom trust <ref>`.
func TestApplyItemTrustChoice_Grant(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "trust-me body")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustGrant))
	assert.Contains(t, out.String(), "Approved demo#fragments/x")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(signing.KindFragments, countersignRefFor(ref), signing.FormRaw, []byte("trust-me body")),
		"[t] must record an approval bound to the content bytes")
}

// TestApplyItemTrustChoice_Blacklist: the [b] action writes BOTH the ref-level
// rejected state and a content-reject countersignature, identical to
// `ctxloom blacklist <ref>`.
func TestApplyItemTrustChoice_Blacklist(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/curl-pipe-sh", itemTrustBlacklist))
	assert.Contains(t, out.String(), "Rejected demo#fragments/curl-pipe-sh")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "curl-pipe-sh", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedRefReject(signing.KindFragments, countersignRefFor(ref)),
		"[b] must record a ref-level rejected state")
	assert.True(t, store.HasUnsignedContentReject(signing.KindFragments, signing.FormRaw, []byte("rm -rf danger")),
		"[b] must record a content-reject over the item's bytes")
}

// TestApplyItemTrustChoice_Skip: skip (and any non-t/b answer) writes nothing —
// viewing never trusts, so the store never records anything for this ref.
func TestApplyItemTrustChoice_Skip(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "look-but-do-not-touch")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustSkip))
	assert.Empty(t, out.String(), "skip must produce no action output")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	store := userApprovalsStore(t)
	assert.False(t, store.HasUnsignedApprove(signing.KindFragments, countersignRefFor(ref), signing.FormRaw, []byte("look-but-do-not-touch")),
		"skip must not record any review state")
	assert.False(t, store.HasUnsignedRefReject(signing.KindFragments, countersignRefFor(ref)))
}

// TestShowItem_NonInteractiveStdoutUnchanged proves the TR4 guarantee: in a
// non-interactive environment (go test stdout is not a TTY), `show -i` is
// byte-for-byte identical to `show` and emits no trust UI on stdout. The
// content path is exercised end-to-end through GetConfig/loader via a chdir'd
// temp project.
func TestShowItem_NonInteractiveStdoutUnchanged(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "the fragment body")
	t.Chdir(root) // GetConfig() (config.Load) resolves <root>/.ctxloom

	plain, outPlain := testCmd()
	require.NoError(t, showItem(plain, "demo#fragments/x", ItemTypeFragment, false, false))

	inter, outInter := testCmd()
	require.NoError(t, showItem(inter, "demo#fragments/x", ItemTypeFragment, false, true))

	assert.Equal(t, outPlain.String(), outInter.String(),
		"-i must not change stdout in a non-interactive terminal")
	assert.Contains(t, outPlain.String(), "the fragment body", "content must still be shown")
	for _, leak := range []string{"Effective trust", "[t]rust", "[b]lacklist"} {
		assert.NotContainsf(t, outInter.String(), leak, "trust UI %q must not leak into piped stdout", leak)
	}
}
