package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// neutralizeRefresh points the project root at an empty dir with no applied
// harness, so the post-mutation refreshManagedArtifacts is a no-op. These store-
// focused cases assert the trust store, not the on-disk managed artifacts.
func neutralizeRefresh(t *testing.T) {
	t.Helper()
	t.Setenv(projectroot.EnvVar, t.TempDir())
}

// testCmd returns a bare cobra command whose stdout is captured. With no
// --format flag registered, emit() takes the text branch (see format.go). A
// Background context is set so cmd.Context() is non-nil, matching what cobra's
// Execute installs in production (a bare command's Context() is otherwise nil).
func testCmd() (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	var buf bytes.Buffer
	c.SetOut(&buf)
	return c, &buf
}

// loadTrustStore reads the on-disk trust store written by the commands.
func loadTrustStore(t *testing.T, appDir string) *trust.Store {
	t.Helper()
	s, err := trust.New(paths.TrustPath(appDir))
	require.NoError(t, err)
	return s
}

// seedLocalFragment writes a local bundle with one fragment to the temp project
// so the read-path loader can resolve and hash it.
func seedLocalFragment(t *testing.T, cfg *config.Config, bundle, name, body string) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: bundle,
		Fragments: map[string]operations.BundleFragmentInput{
			name: {Content: body, NoDistill: true},
		},
	})
	require.NoError(t, err)
}

// effectiveFragmentHash recomputes the bytes-exact effective-content hash the
// grant must bind to, independently of the command path.
func effectiveFragmentHash(t *testing.T, appDir, bundle, name string) string {
	t.Helper()
	loader := bundles.NewLoader([]string{paths.BundlesPath(appDir)}, true)
	b, err := loader.Load(bundle)
	require.NoError(t, err)
	frag, ok := b.Fragments[name]
	require.True(t, ok, "seeded fragment %q missing", name)
	hash, _ := frag.EffectiveContentHash(true)
	return hash
}

// TestRunBundleTrust_RoundTrip drives `ctxloom bundle trust/untrust` through to
// the on-disk store: each direction sets the SHA-agnostic posture and is
// re-readable, and the human output names the new posture.
func TestRunBundleTrust_RoundTrip(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}

	c, out := testCmd()
	require.NoError(t, runBundleTrust(c, cfg, "experimental", false))
	assert.Contains(t, out.String(), "is now untrusted")

	dec, ok := loadTrustStore(t, appDir).BundlePosture("experimental")
	require.True(t, ok, "untrust must persist a posture")
	assert.Equal(t, trust.Deny, dec)

	c, out = testCmd()
	require.NoError(t, runBundleTrust(c, cfg, "experimental", true))
	assert.Contains(t, out.String(), "is now trusted")

	dec, ok = loadTrustStore(t, appDir).BundlePosture("experimental")
	require.True(t, ok)
	assert.Equal(t, trust.Allow, dec, "re-trust must flip the posture back to allow")
}

// TestRunItemTrust_GrantsLocalFragment drives `ctxloom trust <ref>`: the grant
// is written under the canonical (ctxloom:local) repo key, bound to the item's
// recomputed effective-content hash — never an author-supplied value.
func TestRunItemTrust_GrantsLocalFragment(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "always-trusted body")

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "demo#fragments/x"))
	assert.Contains(t, out.String(), "Trusted demo#fragments/x")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "x")

	store := loadTrustStore(t, appDir)
	g, ok := store.GrantMatch(remote.LocalSource, "demo#fragments/x", wantHash)
	require.True(t, ok, "grant must be keyed by canonical repo + effective hash")
	assert.Equal(t, remote.LocalSource, g.RepoURL)
	assert.Equal(t, wantHash, g.ContentHash)
	// A different hash must NOT match — the grant is content-pinned.
	_, ok = store.GrantMatch(remote.LocalSource, "demo#fragments/x", "sha256:other")
	assert.False(t, ok)
}

// TestRunBlacklist_WritesBothComponents drives `ctxloom blacklist <ref>`: it
// writes the sticky ref-level block AND the content-hash denylist entry, so the
// content is blocked both by ref and (if renamed/moved) by hash.
func TestRunBlacklist_WritesBothComponents(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, runBlacklist(c, cfg, "demo#fragments/curl-pipe-sh"))
	assert.Contains(t, out.String(), "Blacklisted demo#fragments/curl-pipe-sh")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "curl-pipe-sh")

	store := loadTrustStore(t, appDir)
	// Ref-level (sticky) component.
	assert.True(t, store.BlacklistMatch(remote.LocalSource, "demo#fragments/curl-pipe-sh"),
		"sticky ref-level blacklist must be recorded")
	// Content-hash (denylist) companion.
	assert.True(t, store.DenylistMatch(wantHash),
		"the item's content hash must be recorded on the denylist")
}

// TestRunBlacklist_CanonicalizedKeying drives `ctxloom blacklist` against a
// remote ref spelled one way and proves the on-disk block matches a *different*
// spelling of the same repo — URL variants cannot escape a blacklist. The
// content is unresolvable here (no bundle on disk), so only the durable sticky
// ref block is written; that is exactly the path that must canonicalize.
func TestRunBlacklist_CanonicalizedKeying(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}

	c, _ := testCmd()
	// Blacklist with a .git suffix + mixed case + trailing variant.
	require.NoError(t, runBlacklist(c, cfg,
		"https://github.com/Acme/Repo.git@bundles/tooling#fragments/solid"))

	store := loadTrustStore(t, appDir)
	// Query with an entirely different spelling of the same repo (git@ form).
	assert.True(t, store.BlacklistMatch("git@github.com:acme/repo", "tooling#fragments/solid"),
		"a URL variant of the same remote must resolve to the same blacklist key")
}
