package operations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Exposure-withholding characterization.
//
// WHY THIS FILE EXISTS: the trust gate is FAIL-CLOSED by contract — a ref the
// gate cannot address (resolve error) and an approvals store it cannot read
// (store error) must WITHHOLD, never default-allow. Those two arms are the
// ones a relocation of the gate breaks SILENTLY: every happy-path assertion
// still passes when an error path quietly becomes an allow path, because the
// happy path never takes it. Only a test that drives the error arms and
// asserts on the WITHHELD SET can catch that.
//
// So this file pins, per trust state, exactly which refs are exposed and which
// are withheld, at the exposure surfaces (single-item resource fetch, context
// assembly, the tooling sweep, and the real no-injection production path).
//
// Every assertion goes through exposureProbe (seeded states) or the
// realExposure* helpers (production wiring). Those are the ONLY parts of this
// file that know WHERE gating is wired. Relocating the gate — loader
// construction → a process stage → wherever it lands next — changes those
// helpers and nothing else. If a relocation forces an edit anywhere BELOW
// them, the relocation changed behavior.

// Fixture bodies, kept as constants so an assertion names the byte string it
// expects rather than a marker that could match by accident.
const (
	charGateApprovedBody = "chargate approved fragment body"
	charGateRejectedBody = "chargate rejected fragment body"
	charGatePendingBody  = "chargate pending fragment body"
	charGateToolingBody  = "chargate tooling command body"
	charGateRejCmdBody   = "chargate rejected command body"
)

// charGateBundle is a REMOTE bundle ref (canonical "<url>@bundles/<path>"), so
// its content is NOT first-party and every exposure has to be positively
// justified — the state space this file characterizes.
const charGateBundle = acmeBundle + "chargate"

const (
	charGateApprovedRef = charGateBundle + "#fragments/approved"
	charGateRejectedRef = charGateBundle + "#fragments/rejected"
	charGatePendingRef  = charGateBundle + "#fragments/pending"
	// Commands gate under the "prompts" kind segment, never "commands".
	charGateToolingGateRef = charGateBundle + "#prompts/tooling"
	charGateRejCmdGateRef  = charGateBundle + "#prompts/rejected"
)

// charGateSeed builds the fixture bundle, stamped with signer (pass "" for
// unsigned). StampSigner is the ONLY way a verified publisher identity reaches
// a bundle, so stamping it here is how a test reaches the trusted-signer and
// builtin-laundering steps of the cascade.
func charGateSeed(signer string) map[string]*bundles.Bundle {
	b := &bundles.Bundle{
		Name: charGateBundle,
		Fragments: map[string]bundles.BundleFragment{
			"approved": {Content: charGateApprovedBody},
			"rejected": {Content: charGateRejectedBody},
			"pending":  {Content: charGatePendingBody},
		},
		Commands: map[string]bundles.BundleCommand{
			"tooling":  {Content: charGateToolingBody},
			"rejected": {Content: charGateRejCmdBody},
		},
	}
	b.StampSigner(signer)
	return map[string]*bundles.Bundle{charGateBundle: b}
}

// exposureProbe is the seam. It wires the trust gate into the read path
// exactly the way the exposure surfaces do (exposureLoaderGated's own two
// lines: build a contentGate over cfg+records, attach it to the bundle
// reader), and exposes the outcomes the assertions below are written against:
// what body a caller got, and which refs the gate withheld.
//
// withheld() deliberately reads the GATE's tally rather than any reader-side
// one: the gate is what makes the decision, so its record survives wherever
// the decision is applied from.
type exposureProbe struct {
	cfg    *config.Config
	loader *bundles.Loader
	gate   *contentGate
}

func newExposureProbe(t *testing.T, cfg *config.Config, records ReviewRecords, seed map[string]*bundles.Bundle) *exposureProbe {
	t.Helper()
	if cfg == nil {
		cfg = config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	}
	gate := &contentGate{cfg: cfg, records: records}
	return &exposureProbe{
		cfg:    cfg,
		loader: bundles.NewLoader(nil, bundles.WithSeededBundles(seed), bundles.WithTrustGate(gate.allow)),
		gate:   gate,
	}
}

// fragment drives the ctxloom://fragments/ resource surface.
func (p *exposureProbe) fragment(ref string) (string, error) {
	res, err := GetFragment(context.Background(), p.cfg, GetFragmentRequest{Name: ref, Loader: p.loader})
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// command drives the ctxloom://prompts/ resource surface.
func (p *exposureProbe) command(ref string) (string, error) {
	res, err := GetCommand(context.Background(), p.cfg, GetCommandRequest{Name: ref, Loader: p.loader})
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// assemble drives context assembly (the bulk exposure surface).
func (p *exposureProbe) assemble(t *testing.T, refs ...string) *AssembleContextResult {
	t.Helper()
	res, err := AssembleContext(context.Background(), p.cfg, AssembleContextRequest{
		Fragments: refs,
		Loader:    p.loader,
	})
	require.NoError(t, err)
	return res
}

// tooling drives operations.CollectTooling — bundle-supplied text that drives
// Containerfile edits, so its withholding is load-bearing.
func (p *exposureProbe) tooling() []ToolingDeclaration {
	return CollectTooling(p.cfg, p.loader)
}

// withheld returns the refs the gate denied, deduplicated and sorted.
func (p *exposureProbe) withheld() []string { return p.gate.withheldRefs() }

// --- assertions (must survive any relocation of the gate UNEDITED) ---------

// TestExposureWithheld_Characterization_ReviewStates pins the three ordinary
// review states of unsigned remote content: an approval of exactly these bytes
// exposes, a rejection withholds, and never-reviewed withholds. The withheld
// SET is asserted, not just the exposed one — "which refs were denied" is the
// fact a relocation must preserve.
func TestExposureWithheld_Characterization_ReviewStates(t *testing.T) {
	fx := newTrustFixture(t)
	fx.approveFragment("chargate", "approved", charGateApprovedBody)
	fx.rejectFragment("chargate", "rejected", charGateRejectedBody)
	fx.rejectPrompt("chargate", "rejected", charGateRejCmdBody)
	p := newExposureProbe(t, nil, fx.records(), charGateSeed(""))

	got, err := p.fragment(charGateApprovedRef)
	require.NoError(t, err, "an approval of exactly these bytes must expose")
	assert.Equal(t, charGateApprovedBody, got)

	_, err = p.fragment(charGateRejectedRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "a rejected fragment must be withheld, got %v", err)

	_, err = p.fragment(charGatePendingRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "a never-reviewed fragment must be withheld, got %v", err)

	_, err = p.command(charGateBundle + "#commands/rejected")
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "a rejected command must be withheld, got %v", err)

	assert.ElementsMatch(t, []string{charGateRejCmdGateRef, charGateRejectedRef, charGatePendingRef}, p.withheld(),
		"exactly the denied refs are tallied, content-free, under their gate refs")
}

// TestExposureWithheld_Characterization_AssemblyAndTooling pins the same
// review states at the BULK surfaces: assembled context carries the approved
// body and neither denied one, and the tooling sweep drops a command it cannot
// justify. Bundle-authored tooling text edits a Containerfile, so a withheld
// tooling command that leaked would be a code-execution path.
func TestExposureWithheld_Characterization_AssemblyAndTooling(t *testing.T) {
	fx := newTrustFixture(t)
	fx.approveFragment("chargate", "approved", charGateApprovedBody)
	fx.rejectFragment("chargate", "rejected", charGateRejectedBody)
	p := newExposureProbe(t, nil, fx.records(), charGateSeed(""))

	res := p.assemble(t, charGateApprovedRef, charGateRejectedRef, charGatePendingRef)
	assert.Contains(t, res.Context, charGateApprovedBody)
	assert.NotContains(t, res.Context, charGateRejectedBody, "a rejected fragment must not reach assembled context")
	assert.NotContains(t, res.Context, charGatePendingBody, "a never-reviewed fragment must not reach assembled context")
	assert.Contains(t, res.FragmentsLoaded, charGateApprovedRef)
	assert.NotContains(t, res.FragmentsLoaded, charGateRejectedRef)
	assert.NotContains(t, res.FragmentsLoaded, charGatePendingRef)

	assert.Empty(t, p.tooling(), "an unreviewed tooling command must not be collected")
	assert.Contains(t, p.withheld(), charGateToolingGateRef,
		"the withheld tooling command is tallied under its prompts-kind gate ref")
}

// TestExposureWithheld_Characterization_TrustedSignerExposes pins the signer
// step: a bundle carrying a VERIFIED publisher identity exposes with no review
// record at all. It is the control for the laundering test below — without it,
// that test could pass because everything is withheld for an unrelated reason.
func TestExposureWithheld_Characterization_TrustedSignerExposes(t *testing.T) {
	p := newExposureProbe(t, nil, newTrustFixture(t).records(), charGateSeed("publisher@example.com"))

	for _, ref := range []string{charGateApprovedRef, charGatePendingRef} {
		got, err := p.fragment(ref)
		require.NoError(t, err, "a verified publisher exposes without review (%s)", ref)
		assert.NotEmpty(t, got)
	}
	assert.Empty(t, p.withheld(), "nothing is withheld when the publisher is trusted")
}

// TestExposureWithheld_Characterization_BuiltinSignerNeverLaunders pins the
// explicit carve-out: the SYNTHETIC builtin identity stamped onto a REMOTE
// bundle must never be read as a trusted publisher. Nothing about a builtin is
// cryptographically verified, so laundering the token would turn "shipped in
// the binary" into "signed by someone we trust".
func TestExposureWithheld_Characterization_BuiltinSignerNeverLaunders(t *testing.T) {
	p := newExposureProbe(t, nil, newTrustFixture(t).records(), charGateSeed(trust.BuiltinSigner))

	_, err := p.fragment(charGateApprovedRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"the synthetic builtin signer on a remote bundle must not launder into a trusted publisher, got %v", err)
	assert.Contains(t, p.withheld(), charGateApprovedRef)
}

// TestExposureWithheld_Characterization_ResolveErrorWithholds is the first of
// the two FAIL-CLOSED arms. A bundle seeded under a source ref that cannot be
// addressed as a trust ref (here: a canonical URL missing its "@bundles/<path>"
// suffix) reaches a gate that can make no decision about it. "Could not
// evaluate" must render as WITHHELD.
//
// This is the arm a refactor turns into an allow path without noticing: the
// resolve error never fires on any happy path, so nothing else fails when it
// starts returning true.
func TestExposureWithheld_Characterization_ResolveErrorWithholds(t *testing.T) {
	// Missing "@bundles/<path>": unmistakably an ATTEMPTED canonical ref, so it
	// must not be downgraded to a first-party local bundle name either.
	const unaddressable = "https://github.com/acme/repo"
	seed := map[string]*bundles.Bundle{
		unaddressable: {
			Name:      unaddressable,
			Fragments: map[string]bundles.BundleFragment{"frag": {Content: "unaddressable body"}},
			Commands:  map[string]bundles.BundleCommand{"tooling": {Content: "unaddressable tooling"}},
		},
	}
	p := newExposureProbe(t, nil, newTrustFixture(t).records(), seed)

	_, err := p.fragment(unaddressable + "#fragments/frag")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"an unaddressable ref must be WITHHELD, never exposed by default, got %v", err)

	res := p.assemble(t, unaddressable+"#fragments/frag")
	assert.NotContains(t, res.Context, "unaddressable body",
		"an unaddressable ref must not reach assembled context")

	assert.Empty(t, p.tooling(), "an unaddressable tooling command must not be collected")

	assert.ElementsMatch(t,
		[]string{unaddressable + "#fragments/frag", unaddressable + "#prompts/tooling"},
		p.withheld(),
		"every unaddressable ref is tallied — a withhold nothing recorded is a withhold nothing can report")
}

// TestExposureWithheld_Characterization_StoreErrorWithholdsEverything is the
// second FAIL-CLOSED arm, and the sharpest edge in the system. An approvals
// store that EXISTS but cannot be read does not say "nothing is rejected"; it
// says nothing at all. So it denies EVERY item — including a LOCAL one, which
// the first-party exemption would otherwise allow without any review record.
//
// The local case is the whole point: if the store fault merely fell through to
// the ordinary cascade, the local fragment would be allowed at the locality
// step and the fault would be invisible.
func TestExposureWithheld_Characterization_StoreErrorWithholdsEverything(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(t.TempDir(), paths.AppDirName)
	approvalsDir := paths.ApprovalsPath(projectDir)
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))
	unreadable := denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectDir}})
	seed := charGateSeed("")
	// A genuinely LOCAL bundle: a bare name, no scheme marker. Trusted by
	// locality on every healthy path.
	seed["localdev"] = &bundles.Bundle{
		Name:      "localdev",
		Fragments: map[string]bundles.BundleFragment{"keep": {Content: "localdev keep body"}},
	}
	p := newExposureProbe(t, cfg, newCountersignRecords(cfg, unreadable), seed)

	_, err := p.fragment("localdev#fragments/keep")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"an unreadable approvals store must deny even a LOCAL fragment, got %v", err)

	_, err = p.fragment(charGateApprovedRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"an unreadable approvals store must deny remote content too, got %v", err)

	res := p.assemble(t, "localdev#fragments/keep", charGateApprovedRef)
	assert.NotContains(t, res.Context, "localdev keep body")
	assert.NotContains(t, res.Context, charGateApprovedBody)
	assert.Empty(t, res.FragmentsLoaded, "an unreadable store exposes nothing at all")

	assert.Empty(t, p.tooling())
	assert.ElementsMatch(t,
		// The always-on BUILTIN fragment is in this set too: builtin is its own
		// allow step, and the store fault sits above every allow step.
		[]string{"localdev#fragments/keep", charGateApprovedRef, charGateToolingGateRef, builtinIsolationFragmentRef},
		p.withheld(),
		"every item consulted under a broken store is tallied as withheld — including the builtin, whose own allow step the fault outranks")
}

// TestExposureWithheld_Characterization_RealPath_LocalExemptAndRejection
// drives the REAL production wiring — no injected reader, no injected records:
// AssembleContext builds its own gated exposure from cfg alone. Local content
// is exempt from REVIEW but not from REJECTION, so the blacklisted sibling is
// still withheld.
func TestExposureWithheld_Characterization_RealPath_LocalExemptAndRejection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	cfg, _ := realExposureProject(t, afero.NewMemMapFs())

	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked", FS: cfg.FS()}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/keep", "dev#fragments/blocked"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "KEEP-MARKER", "local content is exempt from review")
	assert.NotContains(t, res.Context, "BLOCKED-MARKER", "local content is NOT exempt from rejection")
}

// TestExposureWithheld_Characterization_RealPath_StoreErrorWithholds is the
// store-fault arm at the REAL production seam, with no records injection at
// all: the fault has to be discovered by the gate the exposure path builds for
// itself. Nothing is exposed, including the local fragment that the previous
// test proves is exposed on a healthy store.
func TestExposureWithheld_Characterization_RealPath_StoreErrorWithholds(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")

	fs := afero.NewMemMapFs()
	cfg, appDir := realExposureProject(t, fs)
	approvalsDir := paths.ApprovalsPath(appDir)
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))

	// Rebuild cfg over an fs that refuses to open the approvals directory, so
	// the gate the exposure path constructs for itself hits the fault.
	broken, err := config.Load(
		config.WithFS(denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}),
		config.WithAppDir(appDir))
	require.NoError(t, err)
	_ = cfg

	res, err := AssembleContext(context.Background(), broken, AssembleContextRequest{
		Fragments: []string{"dev#fragments/keep", "dev#fragments/blocked"},
	})
	require.NoError(t, err, "a broken store degrades to withholding, never to a hard error")
	assert.NotContains(t, res.Context, "KEEP-MARKER",
		"an unreadable approvals store must withhold even local content on the real exposure path")
	assert.NotContains(t, res.Context, "BLOCKED-MARKER")
}

// realExposureProject materializes a project with one local bundle (two
// fragments) on fs and returns a config.Load'ed cfg over it — the production
// construction path, so AssembleContext builds its own reader and its own
// gate from cfg alone.
func realExposureProject(t *testing.T, fs afero.Fs) (*config.Config, string) {
	t.Helper()
	appDir := "/proj/" + paths.AppDirName
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bundlesDir, 0o755))
	const bundleYAML = `version: "1.0"
description: local dev
fragments:
  keep:
    content: |
      KEEP-MARKER
  blocked:
    content: |
      BLOCKED-MARKER
`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(bundlesDir, "dev.yaml"), []byte(bundleYAML), 0o644))
	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	return cfg, appDir
}
