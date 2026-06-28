package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
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

// TestApplyItemTrustChoice_Grant: the [t] action writes a content-pinned grant,
// identical to `ctxloom trust <ref>`.
func TestApplyItemTrustChoice_Grant(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "trust-me body")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustGrant))
	assert.Contains(t, out.String(), "Trusted demo#fragments/x")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "x")
	store := loadTrustStore(t, appDir)
	_, ok := store.GrantMatch(remote.LocalSource, "demo#fragments/x", wantHash)
	assert.True(t, ok, "[t] must write a grant bound to the effective-content hash")
}

// TestApplyItemTrustChoice_Blacklist: the [b] action writes BOTH the sticky
// ref-level block and the content-hash denylist entry, identical to
// `ctxloom blacklist <ref>`.
func TestApplyItemTrustChoice_Blacklist(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/curl-pipe-sh", itemTrustBlacklist))
	assert.Contains(t, out.String(), "Blacklisted demo#fragments/curl-pipe-sh")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "curl-pipe-sh")
	store := loadTrustStore(t, appDir)
	assert.True(t, store.BlacklistMatch(remote.LocalSource, "demo#fragments/curl-pipe-sh"),
		"[b] must write the sticky ref-level block")
	assert.True(t, store.DenylistMatch(wantHash),
		"[b] must record the item's content hash on the denylist")
}

// TestApplyItemTrustChoice_Skip: skip (and any non-t/b answer) writes nothing —
// viewing never trusts, so the store file is never even created.
func TestApplyItemTrustChoice_Skip(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "look-but-do-not-touch")

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "demo#fragments/x", itemTrustSkip))
	assert.Empty(t, out.String(), "skip must produce no action output")

	// No write happened: the store maps are empty and no trust.yaml exists.
	store := loadTrustStore(t, appDir)
	wantHash := effectiveFragmentHash(t, appDir, "demo", "x")
	_, ok := store.GrantMatch(remote.LocalSource, "demo#fragments/x", wantHash)
	assert.False(t, ok, "skip must not write a grant")
	assert.False(t, store.BlacklistMatch(remote.LocalSource, "demo#fragments/x"),
		"skip must not write a blacklist")

	_, statErr := os.Stat(paths.TrustPath(appDir))
	assert.True(t, os.IsNotExist(statErr), "skip must not create the trust store file")
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
