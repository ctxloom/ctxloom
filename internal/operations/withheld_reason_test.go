package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// gatePulledRef is a THIRD item, distinct from gatePostgresRef (pending) and
// gateHookRef (rejectable), used by the tests below to exercise the RETRACTED
// source specifically.
const gatePulledRef = acmeBundle + "tooling#mcp/pulled-server"

// TestContentGate_WithheldItems_ReportReason is the RED-before/GREEN-after
// proof for the visibility fix: a withheld item's ref must carry WHY it was
// withheld — rejected, retracted (by the publisher), or pending review —
// derived from the already-computed EffectiveTrust Source/Detail, never a
// generic "withheld" tally. It drives the REAL decision cascade (gate.allow)
// for all three denial reasons over one shared gate, then reads the per-ref
// detail back via withheldItems() — the exact seam both
// ExecutableTrustGate.WarnWithheld and the content-loader's warnWithheld
// consult (see trust_gate.go).
func TestContentGate_WithheldItems_ReportReason(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)

	// gateHookRef: a human explicitly rejected it.
	fx.rejectItem(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"},
		signing.FormRaw, toolingHookPayload())

	// gatePulledRef: the publisher retracted it (never rejected/approved by
	// this machine at all — retraction alone must withhold it).
	retraction := retractedFor("tooling", "pulled-server", "found a security vulnerability")

	// gatePostgresRef: never reviewed, unsigned — the terminal pending default.

	gate := &contentGate{cfg: cfg, records: fx.records(), retraction: retraction}

	assert.False(t, admitExec(t, gate, execRead(t, ""), gatePostgresRef, postgresPayload(), "raw"), "pending item must withhold")
	assert.False(t, admitExec(t, gate, execRead(t, ""), gateHookRef, toolingHookPayload(), "raw"), "rejected item must withhold")
	assert.False(t, admitExec(t, gate, execRead(t, ""), gatePulledRef, pbytes("pulled"), "raw"), "retracted item must withhold")

	items := gate.withheldItems()
	require.Len(t, items, 3, "all three denied refs must be recorded as withheld")

	reasons := make(map[string]string, len(items))
	sources := make(map[string]bundles.Reason, len(items))
	for _, it := range items {
		reasons[it.Ref] = it.Verdict.Reason.Explain(it.Verdict.Detail)
		sources[it.Ref] = it.Verdict.Reason
	}

	// Each item's reason must actually SAY why — not a generic placeholder.
	assert.Equal(t, "rejected", reasons[gateHookRef])
	assert.Contains(t, reasons[gatePulledRef], "retracted")
	assert.Contains(t, reasons[gatePulledRef], "found a security vulnerability",
		"the publisher's stated retraction reason must be visible, content-free")
	assert.Contains(t, reasons[gatePostgresRef], "awaiting review")
	assert.Contains(t, reasons[gatePostgresRef], "ctxloom review",
		"a pending item's reason must point at the action to take")

	// Not vacuous: the three reasons must be MUTUALLY DISTINCT. A regression
	// that reports the same generic string for every withheld item (the "N
	// item(s) awaiting review" tally this fixes) collapses these to equal
	// strings and fails here.
	assert.NotEqual(t, reasons[gateHookRef], reasons[gatePostgresRef])
	assert.NotEqual(t, reasons[gateHookRef], reasons[gatePulledRef])
	assert.NotEqual(t, reasons[gatePostgresRef], reasons[gatePulledRef])

	// Confirm the Reason itself (not just the rendered string) is right —
	// Explain must be deriving from the real decision, not a fixture.
	assert.Equal(t, bundles.ReasonRejected, sources[gateHookRef])
	assert.Equal(t, bundles.ReasonRetracted, sources[gatePulledRef])
	assert.Equal(t, bundles.ReasonUnsigned, sources[gatePostgresRef],
		"an unsigned REMOTE item now says so: the cascade's single SourcePending "+
			"could not distinguish it from a signature by a key we distrust")
}

// TestExecutableTrustGate_WarnWithheld_NamesReason proves the actual UX
// artifact — the stderr advisory line ExecutableTrustGate.WarnWithheld
// prints — names each withheld item's ref AND its reason, for all three
// dispositions, instead of the old undifferentiated "N bundle executable(s)
// awaiting review" tally.
func TestExecutableTrustGate_WarnWithheld_NamesReason(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	fx.rejectItem(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"},
		signing.FormRaw, toolingHookPayload())
	retraction := retractedFor("tooling", "pulled-server", "found a security vulnerability")

	g := &contentGate{cfg: cfg, records: fx.records(), retraction: retraction}
	e := &ExecutableTrustGate{gate: g}

	unsigned := execRead(t, "")
	assert.False(t, bundles.Decide(e.Authorizer(), unsigned, gatePostgresRef, postgresPayload(), bundles.FormRaw).Admit)
	assert.False(t, bundles.Decide(e.Authorizer(), unsigned, gateHookRef, toolingHookPayload(), bundles.FormRaw).Admit)
	assert.False(t, bundles.Decide(e.Authorizer(), unsigned, gatePulledRef, pbytes("pulled"), bundles.FormRaw).Admit)

	stderr := captureStderr(t, func() { e.WarnWithheld() })

	assert.Contains(t, stderr, gatePostgresRef+": awaiting review — run 'ctxloom review'")
	assert.Contains(t, stderr, gateHookRef+": rejected")
	assert.Contains(t, stderr, gatePulledRef+": retracted by the publisher (found a security vulnerability)")
}

// TestAssembleContext_WarnWithheld_NamesReason_FullPath drives the REAL
// exposure path (AssembleContext with NO injected loader — the exact shape
// every real `ctxloom run`/oneshot/agent_run call uses) and proves the
// assembly-time withheld advisory names WHY a rejected local fragment was
// withheld, not just that something was withheld. This is the content-loader
// counterpart to TestExecutableTrustGate_WarnWithheld_NamesReason above,
// exercising exposureLoaderGated end to end via context.go's default
// (non-injected) loader-building branch.
func TestAssembleContext_WarnWithheld_NamesReason_FullPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "") // no ssh-agent: SetBlacklist below degrades to the unsigned path
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bundlesDir, 0o755))
	bundleYAML := `version: "1.0"
description: local dev
fragments:
  keep:
    content: |
      KEEP-MARKER
  blocked:
    content: |
      BLOCKED-MARKER
`
	require.NoError(t, afero.WriteFile(fs, bundlesDir+"/dev.yaml", []byte(bundleYAML), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked", FS: fs}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	stderr := captureStderr(t, func() {
		_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Fragments: []string{"dev#fragments/keep", "dev#fragments/blocked"},
		})
		require.NoError(t, err)
	})

	assert.Contains(t, stderr, "dev#fragments/blocked: rejected",
		"the real (non-test-injected) assembly path must name WHY the rejected fragment was withheld, not just that it was")
}

// TestAssembleContext_WarnWithheld_InjectedLoaderIsSilentAboutWhy pins the
// current behavior: when the caller injects its own
// *bundles.Loader (a test seam — see gatedAcmeLoader — every real production
// call site always builds through exposureLoaderGated instead), warnWithheld
// has no *contentGate to consult and now emits NO advisory at all — it used
// to fall back to a reasonless "N item(s) awaiting review" tally, but that
// fallback (warnPendingTally) was itself dead in two of its three branches
// and was deleted along with it. This is not a production regression: rg
// confirms every real call site (context.go, hooks.go, tooling.go) always
// builds its loader through exposureLoaderGated, so gate is never nil outside
// a test that deliberately injects its own loader, as this one does.
func TestAssembleContext_WarnWithheld_InjectedLoaderIsSilentAboutWhy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fx := newTrustFixture(t)
	fx.rejectFragment("tooling", "evil", "evil body")
	loader, cfg := gatedAcmeLoader(t, fx.records())

	stderr := captureStderr(t, func() {
		_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Fragments: []string{evilRef},
			Pipeline:  loader,
		})
		require.NoError(t, err)
	})

	assert.Empty(t, stderr, "an injected-loader caller (test-only in production) gets no gate to report from")
}
