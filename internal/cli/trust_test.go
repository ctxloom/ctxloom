package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// neutralizeRefresh points the project root at an empty dir with no applied
// harness, so the post-mutation refreshManagedArtifacts is a no-op. These store-
// focused cases assert the countersignature store, not the on-disk managed
// artifacts.
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

// noAgentEnv isolates HOME (so the default user countersignature store lands
// in a controlled temp dir) and clears SSH_AUTH_SOCK (so `ctxloom
// trust`/`blacklist` deterministically take the UNSIGNED degraded path —
// spec §9.5 — rather than depending on whatever the host happens to have).
func noAgentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
}

// userApprovalsStore opens the REAL user countersignature store the CLI
// plumbing writes to by default (~/.ctxloom/approvals under the HOME noAgentEnv
// pointed at a temp dir).
func userApprovalsStore(t *testing.T) *countersign.Store {
	t.Helper()
	home, err := paths.HomeApprovalsPath()
	require.NoError(t, err)
	return countersign.NewStore(home, afero.NewOsFs())
}

// countersignRefFor delegates to the ONE implementation of the countersign
// item-ref. It is not a reimplementation: a copy of this rule drifts silently
// the moment the ref grammar changes, and the tests then assert against a key
// production never writes.
func countersignRefFor(ref trust.Ref) string {
	return operations.CountersignRef(ref)
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

// seedLocalCommand writes a local bundle with one command to the temp project
// so the read-path loader can resolve and hash it. The CLI list emits
// #commands/<name> refs (item-kind renamed prompt->skill->command), which
// resolve to trust.KindPrompt.
func seedLocalCommand(t *testing.T, cfg *config.Config, bundle, name, body string) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: bundle,
		Commands: map[string]operations.BundleCommandInput{
			name: {Content: body, NoDistill: true},
		},
	})
	require.NoError(t, err)
}

// TestRunItemTrust_AcceptsLocalFragment drives `ctxloom bundle trust <ref>`: the
// approval is countersigned (here: UNSIGNED, no agent in the test env) over
// the item's recomputed content bytes — never an author-supplied value — and
// lands in the real user store under the canonical (ctxloom:local) key.
func TestRunItemTrust_AcceptsLocalFragment(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "always-trusted body")

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "demo#fragments/x"))
	assert.Contains(t, out.String(), "Approved demo#fragments/x")
	assert.Contains(t, out.String(), "UNSIGNED")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(ref), signing.AttestFragmentRaw, []byte("always-trusted body")),
		"approval must be recorded for the canonical ctxloom:local key, over the exact fragment bytes")
}

// TestRunItemTrust_AcceptsLocalCommand drives `ctxloom trust <bundle>#commands/<name>`:
// the exact ref the list emits after the prompt->skill->command rename. It
// must not error "unknown item kind" and must record an approval keyed by the
// canonical (#prompts/) key bound to the command's recomputed content bytes.
func TestRunItemTrust_AcceptsLocalCommand(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalCommand(t, cfg, "demo", "review", "always-trusted command body")

	c, out := testCmd()
	// Accepting #commands/ is the fix; the echo reports the canonical #prompts/ key
	// (res.Ref == tRef.Key(), Kind.Dir()=="prompts") — the store address, not the
	// input spelling.
	require.NoError(t, runItemTrust(c, cfg, "demo#commands/review"))
	assert.Contains(t, out.String(), "Approved demo#prompts/review")

	// Stored under the canonical #prompts/ key (trust.KindPrompt.Dir()), so the
	// assembly gate and existing approvals resolve identically regardless of
	// spelling.
	ref := trust.Ref{Bundle: "demo", Kind: trust.KindPrompt, Name: "review", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(ref), signing.AttestCommandRaw, []byte("always-trusted command body")))
}

// TestRunBlacklist_WritesBothComponents drives `ctxloom blacklist <ref>`: it
// writes the ref-level rejected state AND a content-reject countersignature,
// so the content is blocked both by ref and (if renamed/moved) by content.
func TestRunBlacklist_WritesBothComponents(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")

	c, out := testCmd()
	require.NoError(t, runItemReject(c, cfg, "demo#fragments/curl-pipe-sh"))
	assert.Contains(t, out.String(), "Rejected demo#fragments/curl-pipe-sh")

	ref := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "curl-pipe-sh", IsLocal: true}
	store := userApprovalsStore(t)
	// Ref-level (sticky) component.
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(ref)),
		"ref-level rejected state must be recorded")
	// Content-reject companion.
	assert.True(t, store.HasUnsignedContentReject(signing.AttestFragmentRaw, []byte("rm -rf danger")),
		"the item's content must be recorded as a content-reject")
}

// TestRunBlacklist_RefRejectBindsOneAddress drives `ctxloom blacklist` against
// a remote ref and pins BOTH halves of what a ref-level rejection means.
//
// It binds the address it names, across every spelling that is the SAME URI:
// transport form (git@ vs https), scheme case, a trailing slash. It does NOT
// reach a merely non-preferred respelling — a ".git" suffix, a different
// repository-path case — because those are DIFFERENT identities, deliberately:
// whether two addresses reach one repository is host-specific knowledge this
// layer does not have, and folding on a guess merges two identities onto one
// trust key.
//
// That narrowing is not a gap left open. The cross-address case is carried by
// the CONTENT-reject countersignature, which is signed with the ref omitted
// and therefore follows identical bytes wherever they appear — see
// operations.TestDeclaredNameTrust and
// operations.TestSameNameRejectionIsolation. A ref-reject blocks one address,
// which is what it says.
//
// The content is unresolvable here (no bundle on disk), so only the durable
// ref-level state is written; that is exactly the path under test.
func TestRunBlacklist_RefRejectBindsOneAddress(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	c, _ := testCmd()
	require.NoError(t, runItemReject(c, cfg,
		"https://github.com/acme/repo@bundles/tooling#fragments/solid"))
	store := userApprovalsStore(t)

	t.Run("the same URI in another transport spelling IS bound", func(t *testing.T) {
		for _, repoURL := range []string{
			"git@github.com:acme/repo",
			"https://GitHub.com/acme/repo/",
			"http://github.com/acme/repo",
		} {
			ref := trust.Ref{RepoURL: repoURL, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
			assert.True(t, store.HasUnsignedRefReject(countersignRefFor(ref)),
				"%s is the same URI and must resolve to the same rejection key", repoURL)
		}
	})

	t.Run("a non-preferred respelling is a DIFFERENT address, and is NOT bound", func(t *testing.T) {
		for _, repoURL := range []string{
			"https://github.com/acme/repo.git",
			"https://github.com/Acme/Repo",
			"https://www.github.com/acme/repo",
		} {
			ref := trust.Ref{RepoURL: repoURL, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
			assert.False(t, store.HasUnsignedRefReject(countersignRefFor(ref)),
				"%s was folded onto the rejected address; two identities merged onto one trust key", repoURL)
		}
	})

}
