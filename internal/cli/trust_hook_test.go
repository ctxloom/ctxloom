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
	"github.com/ctxloom/ctxloom/internal/trust"
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

// TestRunItemTrust_AcceptsHook drives `ctxloom trust <bundle>#hooks/<event>/<index>`:
// the acceptance is bound to the bundle hook's computed executable-surface hash
// (BundleHook.ComputeContentHash) under the canonical (ctxloom:local) repo key.
func TestRunItemTrust_AcceptsHook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Accepted hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	item, ok := store.Lookup(remote.LocalSource, "hookb#hooks/pre_tool/0")
	require.True(t, ok, "trust must record an accepted state for the hook")
	assert.Equal(t, trust.StateAccepted, item.State)
	// The acceptance is content-pinned to the hook's computed hash (raw form —
	// executables have no distilled variant).
	assert.Equal(t, hook.ComputeContentHash(), item.RawHash)
	assert.Empty(t, item.DistilledHash)
}

// TestRunBlacklist_Hook drives `ctxloom blacklist <bundle>#hooks/<event>/<index>`:
// it writes the ref-level rejected state AND the hook's content hash on the
// denylist, exactly like the content kinds.
func TestRunBlacklist_Hook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "rm -rf danger", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, runBlacklist(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Rejected hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	item, ok := store.Lookup(remote.LocalSource, "hookb#hooks/pre_tool/0")
	require.True(t, ok, "blacklist must record a rejected state for the hook")
	assert.Equal(t, trust.StateRejected, item.State)
	assert.True(t, store.DeniedHash(hook.ComputeContentHash()),
		"blacklist must record the hook's content hash on the denylist")
}

// TestApplyItemTrustChoice_HookGrant proves the interactive `bundle show` [t]
// action routes a hook ref through the same mutation as `ctxloom trust`: it
// records an acceptance bound to the hook's content hash.
func TestApplyItemTrustChoice_HookGrant(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "hookb#hooks/pre_tool/0", itemTrustGrant))
	assert.Contains(t, out.String(), "Accepted hookb#hooks/pre_tool/0")

	store := loadTrustStore(t, appDir)
	item, ok := store.Lookup(remote.LocalSource, "hookb#hooks/pre_tool/0")
	require.True(t, ok, "[t] on a hook must record an acceptance")
	assert.Equal(t, trust.StateAccepted, item.State)
	assert.Equal(t, hook.ComputeContentHash(), item.RawHash)
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
	item, ok := store.Lookup(remote.LocalSource, "hookb#hooks/pre_tool/0")
	require.True(t, ok, "[b] on a hook must record a rejected state")
	assert.Equal(t, trust.StateRejected, item.State)
	assert.True(t, store.DeniedHash(hook.ComputeContentHash()),
		"[b] on a hook must record its content hash on the denylist")
}

// TestPrintBundleHookTrust_ReflectsTrust proves the `bundle show -i` hook listing
// renders the hook's effective trust + source: a project-authored local hook is
// first-party (local exemption), and a rejection flips it to withheld via the
// rejected step — rejection beats the exemption.
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
		"a project-authored local bundle hook is first-party via the local exemption")

	require.NoError(t, store.SetRejected(remote.LocalSource, "hookb#hooks/pre_tool/0", hook.ComputeContentHash()))

	var after bytes.Buffer
	printBundleHookTrust(&after, stamper, "hookb", entry)
	assert.Contains(t, after.String(), "hooks/pre_tool/0: withheld (source: rejected)",
		"a rejection is reported by its own step, beating the local exemption")
}
