package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// seedLocalHookBundle writes a local bundle that ships one pre_tool hook to the
// temp project so the read-path loader can resolve it. CreateBundle has no hook
// input, so the YAML is written directly (mirroring the operations exec-gate
// tests); the body matches BundleHook's authoring fields.
func seedLocalHookBundle(t *testing.T, appDir, bundle string, hook bundles.BundleHook) {
	t.Helper()
	dir := paths.BundlesPath(appDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	yaml := "name: " + bundle + "\nversion: \"1.0\"\nhooks:\n  pre_tool:\n" +
		"    - matcher: " + hook.Matcher + "\n" +
		"      command: " + hook.Command + "\n" +
		"      type: " + hook.Type + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundle+".yaml"), []byte(yaml), 0o644))
}

// TestRunItemTrust_GrantsHook drives `ctxloom trust <bundle>#hooks/<event>/<index>`:
// the grant is keyed by the bundle hook's computed executable-surface hash
// (BundleHook.ComputeContentHash) under the canonical (ctxloom:local) repo key.
func TestRunItemTrust_GrantsHook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Trusted hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	_, ok := store.GrantMatch(remote.LocalSource, "hookb#hooks/pre_tool/0", hook.ComputeContentHash())
	assert.True(t, ok, "trust must write a grant bound to the hook's computed content hash")
	// The grant is content-pinned: a different hash must not match.
	_, ok = store.GrantMatch(remote.LocalSource, "hookb#hooks/pre_tool/0", "sha256:other")
	assert.False(t, ok)
}

// TestRunBlacklist_Hook drives `ctxloom blacklist <bundle>#hooks/<event>/<index>`:
// it writes the sticky ref-level block AND the hook's content hash on the
// denylist, exactly like the content kinds.
func TestRunBlacklist_Hook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "rm -rf danger", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, runBlacklist(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Blacklisted hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	assert.True(t, store.BlacklistMatch(remote.LocalSource, "hookb#hooks/pre_tool/0"),
		"blacklist must write the sticky ref-level block for a hook")
	assert.True(t, store.DenylistMatch(hook.ComputeContentHash()),
		"blacklist must record the hook's content hash on the denylist")
}

// TestApplyItemTrustChoice_HookGrant proves the interactive `bundle show` [t]
// action routes a hook ref through the same mutation as `ctxloom trust`: it
// writes a grant keyed by the hook's content hash.
func TestApplyItemTrustChoice_HookGrant(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "hookb#hooks/pre_tool/0", itemTrustGrant))
	assert.Contains(t, out.String(), "Trusted hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	_, ok := store.GrantMatch(remote.LocalSource, "hookb#hooks/pre_tool/0", hook.ComputeContentHash())
	assert.True(t, ok, "[t] on a hook must write a grant bound to its content hash")
}

// TestApplyItemTrustChoice_HookBlacklist proves the interactive `bundle show` [b]
// action routes a hook ref through the same mutation as `ctxloom blacklist`.
func TestApplyItemTrustChoice_HookBlacklist(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "rm -rf danger", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, _ := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "hookb#hooks/pre_tool/0", itemTrustBlacklist))

	store := loadTrustStore(t, appDir)
	assert.True(t, store.BlacklistMatch(remote.LocalSource, "hookb#hooks/pre_tool/0"),
		"[b] on a hook must write the sticky ref-level block")
	assert.True(t, store.DenylistMatch(hook.ComputeContentHash()),
		"[b] on a hook must record its content hash on the denylist")
}

// TestPrintBundleHookTrust_ReflectsTrust proves the `bundle show -i` hook listing
// renders the hook's effective trust + source: an ungranted local hook is
// withheld (executable, never auto-trusted), and a grant bound to its hash flips
// it to trusted via the explicit-grant tier.
func TestPrintBundleHookTrust_ReflectsTrust(t *testing.T) {
	appDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	entry := bundles.HookEntry{Event: bundles.HookEventPreTool, Index: 0, Hook: hook}

	store := loadTrustStore(t, appDir)
	stamper := operations.NewTrustStamper(cfg, operations.WithStampStore(store))

	var before bytes.Buffer
	printBundleHookTrust(&before, stamper, "hookb", entry)
	assert.Contains(t, before.String(), "hooks/pre_tool/0: trusted (source: local)",
		"a project-authored local bundle hook auto-trusts via the local tier")

	require.NoError(t, store.AddGrant(remote.LocalSource, "hookb#hooks/pre_tool/0", hook.ComputeContentHash(), "raw", ""))

	var after bytes.Buffer
	printBundleHookTrust(&after, stamper, "hookb", entry)
	assert.Contains(t, after.String(), "hooks/pre_tool/0: trusted (source: explicit-grant)",
		"an explicit grant is reported by its own tier, ahead of the local tier")
}
