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
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// seedLocalHookBundle writes a local bundle that ships one pre_tool hook to the
// temp project so the read-path loader can resolve it. CreateBundle has no hook
// input, so the YAML is written directly (mirroring the operations exec-gate
// tests); the body matches BundleHook's authoring fields.
func seedLocalHookBundle(t *testing.T, appDir, bundle string, hook bundles.BundleHook) {
	t.Helper()
	dir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	yaml := "version: \"1.0\"\nhooks:\n  pre_tool:\n" +
		"    - matcher: " + hook.Matcher + "\n" +
		"      command: " + hook.Command + "\n" +
		"      type: " + hook.Type + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundle+".yaml"), []byte(yaml), 0o644))
}

// hookRefFor addresses a local hook exactly as the loader's own read stamps it.
func hookRefFor(bundle, id string) trust.Ref {
	return trust.Ref{Bundle: bundle, Kind: trust.KindHook, Name: id, IsLocal: true}
}

// TestRunItemTrust_AcceptsHook drives `ctxloom trust <bundle>#hooks/<event>/<index>`:
// the approval is countersigned (here: UNSIGNED, no agent in the test env)
// over the bundle hook's computed executable surface, under the canonical
// (ctxloom:local) repo key.
func TestRunItemTrust_AcceptsHook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)
	hookPayload, err := hook.ContentPayload()
	require.NoError(t, err)

	c, out := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Approved hookb#hooks/pre_tool/0")

	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(hookRefFor("hookb", "pre_tool/0")), signing.AttestExecHook, hookPayload),
		"trust must record an approval for the hook's executable surface")
}

// TestRunBlacklist_Hook drives `ctxloom blacklist <bundle>#hooks/<event>/<index>`:
// it writes the ref-level rejected state AND a content-reject over the hook's
// executable surface, exactly like the content kinds.
func TestRunBlacklist_Hook(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	hook := bundles.BundleHook{Matcher: "Bash", Command: "rm -rf danger", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)
	hookPayload, err := hook.ContentPayload()
	require.NoError(t, err)

	c, out := testCmd()
	require.NoError(t, runItemReject(c, cfg, "hookb#hooks/pre_tool/0"))
	assert.Contains(t, out.String(), "Rejected hookb#hooks/pre_tool/0")

	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(hookRefFor("hookb", "pre_tool/0"))),
		"blacklist must record a ref-level rejected state for the hook")
	assert.True(t, store.HasUnsignedContentReject(signing.AttestExecHook, hookPayload),
		"blacklist must record a content-reject over the hook's executable surface")
}

// TestApplyItemTrustChoice_HookGrant proves the interactive `bundle show` [t]
// action routes a hook ref through the same mutation as `ctxloom trust`: it
// records an approval over the hook's content bytes.
func TestApplyItemTrustChoice_HookGrant(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)
	hookPayload, err := hook.ContentPayload()
	require.NoError(t, err)

	c, out := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "hookb#hooks/pre_tool/0", itemTrustGrant))
	assert.Contains(t, out.String(), "Approved hookb#hooks/pre_tool/0")

	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(hookRefFor("hookb", "pre_tool/0")), signing.AttestExecHook, hookPayload),
		"[t] on a hook must record an approval")
}

// TestApplyItemTrustChoice_HookBlacklist proves the interactive `bundle show` [b]
// action routes a hook ref through the same mutation as `ctxloom blacklist`.
func TestApplyItemTrustChoice_HookBlacklist(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	hook := bundles.BundleHook{Matcher: "Bash", Command: "rm -rf danger", Type: "command"}
	seedLocalHookBundle(t, appDir, "hookb", hook)
	hookPayload, err := hook.ContentPayload()
	require.NoError(t, err)

	c, _ := testCmd()
	require.NoError(t, applyItemTrustChoice(c, cfg, "hookb#hooks/pre_tool/0", itemTrustReject))

	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(hookRefFor("hookb", "pre_tool/0"))),
		"[b] on a hook must record a ref-level rejected state")
	assert.True(t, store.HasUnsignedContentReject(signing.AttestExecHook, hookPayload),
		"[b] on a hook must record a content-reject over its executable surface")
}

// toggleRejectRecords is a minimal operations.ReviewRecords fake (duck-typed
// against the exported interface — the concrete countersignRecords is
// unexported to package operations and unreachable from here) whose Rejected
// answer flips on command. Nothing is ever Approved.
type toggleRejectRecords struct{ rejected bool }

func (r *toggleRejectRecords) Rejected(trust.Ref, []byte) bool         { return r.rejected }
func (r *toggleRejectRecords) Approved(trust.Ref, []byte, string) bool { return false }

// TestPrintBundleHookTrust_ReflectsTrust proves the `bundle show -i` hook listing
// renders the hook's effective trust + source: a project-authored local hook is
// first-party (local exemption), and a rejection flips it to withheld via the
// rejected step — rejection beats the exemption.
func TestPrintBundleHookTrust_ReflectsTrust(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	hook := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	entry := bundles.HookEntry{Event: bundles.HookEventPreTool, Index: 0, Hook: hook}
	// The bundle has to EXIST for the stamp to be first-party: the local
	// exemption keys on the posture the reader established, not on the shape of
	// the name. A bundle nothing read establishes no posture, and an unclaimed
	// posture withholds.
	seedLocalHookBundle(t, appDir, "hookb", hook)

	records := &toggleRejectRecords{}
	stamper := operations.NewTrustStamper(cfg, operations.WithStampRecords(records))

	var before bytes.Buffer
	printBundleHookTrust(&before, stamper, "hookb", entry)
	assert.Contains(t, before.String(), "hooks/pre_tool/0: trusted (source: local)",
		"a project-authored local bundle hook is first-party via the local exemption")

	records.rejected = true

	var after bytes.Buffer
	printBundleHookTrust(&after, stamper, "hookb", entry)
	assert.Contains(t, after.String(), "hooks/pre_tool/0: withheld (source: rejected)",
		"a rejection is reported by its own step, beating the local exemption")
}
