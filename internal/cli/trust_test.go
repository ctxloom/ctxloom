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

// seedLocalSkill writes a local bundle with one skill to the temp project so the
// read-path loader can resolve and hash it. The CLI list emits #skills/<name>
// refs (item-kind renamed prompt->skill), which resolve to trust.KindPrompt.
func seedLocalSkill(t *testing.T, cfg *config.Config, bundle, name, body string) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: bundle,
		Skills: map[string]operations.BundleSkillInput{
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

// effectiveSkillHash recomputes the bytes-exact effective-content hash a skill
// grant must bind to, independently of the command path.
func effectiveSkillHash(t *testing.T, appDir, bundle, name string) string {
	t.Helper()
	loader := bundles.NewLoader([]string{paths.BundlesPath(appDir)}, true)
	b, err := loader.Load(bundle)
	require.NoError(t, err)
	skill, ok := b.Skills[name]
	require.True(t, ok, "seeded skill %q missing", name)
	hash, _ := skill.EffectiveContentHash(true)
	return hash
}

// TestRunItemTrust_AcceptsLocalFragment drives `ctxloom trust <ref>`: the
// acceptance is written under the canonical (ctxloom:local) repo key, bound to
// the item's recomputed content hash — never an author-supplied value.
func TestRunItemTrust_AcceptsLocalFragment(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "x", "always-trusted body")

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "demo#fragments/x"))
	assert.Contains(t, out.String(), "Accepted demo#fragments/x")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "x")

	store := loadTrustStore(t, appDir)
	item, ok := store.Lookup(remote.LocalSource, "demo#fragments/x")
	require.True(t, ok, "acceptance must be keyed by the canonical repo + ref")
	assert.Equal(t, remote.LocalSource, item.RepoURL)
	assert.Equal(t, trust.StateAccepted, item.State)
	// Content-pinned: the recorded raw hash is exactly the recomputed one (the
	// seeded fragment is NoDistill, so there is no distilled slot).
	assert.Equal(t, wantHash, item.RawHash)
	assert.Empty(t, item.DistilledHash)
}

// TestRunItemTrust_AcceptsLocalSkill drives `ctxloom trust <bundle>#skills/<name>`:
// the exact ref the list emits after the prompt->skill rename. It must not
// error "unknown item kind" and must record an acceptance keyed by the
// canonical (#prompts/) key bound to the skill's recomputed content hash.
func TestRunItemTrust_AcceptsLocalSkill(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalSkill(t, cfg, "demo", "review", "always-trusted skill body")

	c, out := testCmd()
	// Accepting #skills/ is the fix; the echo reports the canonical #prompts/ key
	// (res.Ref == tRef.Key(), Kind.Dir()=="prompts") — the store address, not the
	// input spelling.
	require.NoError(t, runItemTrust(c, cfg, "demo#skills/review"))
	assert.Contains(t, out.String(), "Accepted demo#prompts/review")

	wantHash := effectiveSkillHash(t, appDir, "demo", "review")

	// Stored under the canonical #prompts/ key (trust.KindPrompt.Dir()), so the
	// assembly gate and existing acceptances resolve identically regardless of
	// spelling.
	store := loadTrustStore(t, appDir)
	item, ok := store.Lookup(remote.LocalSource, "demo#prompts/review")
	require.True(t, ok, "acceptance must be keyed by the canonical repo + ref")
	assert.Equal(t, trust.StateAccepted, item.State)
	assert.Equal(t, wantHash, item.RawHash)
}

// TestRunBlacklist_WritesBothComponents drives `ctxloom blacklist <ref>`: it
// writes the ref-level rejected state AND the content-hash denylist entry, so
// the content is blocked both by ref and (if renamed/moved) by hash.
func TestRunBlacklist_WritesBothComponents(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, runBlacklist(c, cfg, "demo#fragments/curl-pipe-sh"))
	assert.Contains(t, out.String(), "Rejected demo#fragments/curl-pipe-sh")

	wantHash := effectiveFragmentHash(t, appDir, "demo", "curl-pipe-sh")

	store := loadTrustStore(t, appDir)
	// Ref-level (sticky) component.
	item, ok := store.Lookup(remote.LocalSource, "demo#fragments/curl-pipe-sh")
	require.True(t, ok, "ref-level rejected state must be recorded")
	assert.Equal(t, trust.StateRejected, item.State)
	// Content-hash (denylist) companion.
	assert.True(t, store.DeniedHash(wantHash),
		"the item's content hash must be recorded on the denylist")
}

// TestRunBlacklist_CanonicalizedKeying drives `ctxloom blacklist` against a
// remote ref spelled one way and proves the on-disk rejection matches a
// *different* spelling of the same repo — URL variants cannot escape a
// rejection. The content is unresolvable here (no bundle on disk), so only the
// durable ref-level state is written; that is exactly the path that must
// canonicalize.
func TestRunBlacklist_CanonicalizedKeying(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}

	c, _ := testCmd()
	// Reject with a .git suffix + mixed case + trailing variant.
	require.NoError(t, runBlacklist(c, cfg,
		"https://github.com/Acme/Repo.git@bundles/tooling#fragments/solid"))

	store := loadTrustStore(t, appDir)
	// Query with an entirely different spelling of the same repo (git@ form).
	item, ok := store.Lookup("git@github.com:acme/repo", "tooling#fragments/solid")
	require.True(t, ok, "a URL variant of the same remote must resolve to the same rejection key")
	assert.Equal(t, trust.StateRejected, item.State)
}
