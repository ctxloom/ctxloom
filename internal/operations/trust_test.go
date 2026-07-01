package operations

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

const trustRepo = "https://github.com/acme/repo"

func newTrustStore(t *testing.T) *trust.Store {
	t.Helper()
	s, err := trust.New("", trust.WithFS(afero.NewMemMapFs()))
	if err != nil {
		t.Fatalf("trust.New: %v", err)
	}
	return s
}

type remoteSpec struct {
	name  string
	url   string
	trust bool
}

func newRegistry(t *testing.T, specs ...remoteSpec) *remote.Registry {
	t.Helper()
	reg, err := remote.NewRegistry("", remote.WithRegistryFS(afero.NewMemMapFs()))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, sp := range specs {
		if err := reg.Add(sp.name, sp.url); err != nil {
			t.Fatalf("registry.Add(%q,%q): %v", sp.name, sp.url, err)
		}
		if sp.trust {
			if err := reg.SetTrustBundles(sp.name, true); err != nil {
				t.Fatalf("SetTrustBundles: %v", err)
			}
		}
	}
	return reg
}

// TestEffectiveTrust_Cascade table-drives the full precedence and every tier.
func TestEffectiveTrust_Cascade(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*trust.Store)
		ref     trust.Ref
		hash    string
		remotes []remoteSpec
		want    trust.Decision
		source  trust.Source
	}{
		// --- precedence: denylist > blacklist > grant (shared setup) ---
		{
			name: "denylist beats blacklist (and grant)",
			setup: func(s *trust.Store) {
				_ = s.Blacklist(trustRepo, "tooling#mcp/postgres", "sha256:H1")
				_ = s.AddGrant(trustRepo, "tooling#mcp/postgres", "sha256:H2", "raw", "")
			},
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			hash:   "sha256:H1",
			want:   trust.Deny,
			source: trust.SourceDenylist,
		},
		{
			name: "blacklist beats grant",
			setup: func(s *trust.Store) {
				_ = s.Blacklist(trustRepo, "tooling#mcp/postgres", "sha256:H1")
				_ = s.AddGrant(trustRepo, "tooling#mcp/postgres", "sha256:H2", "raw", "")
			},
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			hash:   "sha256:H2", // a granted hash, but the ref is blacklisted
			want:   trust.Deny,
			source: trust.SourceBlacklist,
		},
		{
			name: "blacklist survives a content change (new hash still denied)",
			setup: func(s *trust.Store) {
				_ = s.Blacklist(trustRepo, "tooling#mcp/postgres", "sha256:H1")
				_ = s.AddGrant(trustRepo, "tooling#mcp/postgres", "sha256:H2", "raw", "")
			},
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			hash:   "sha256:H3-brand-new", // never seen; sticky ref blacklist still wins
			want:   trust.Deny,
			source: trust.SourceBlacklist,
		},

		// --- grants: exact-hash binding, invalidation, multi-version ---
		{
			name:   "grant allows the exact granted hash",
			setup:  func(s *trust.Store) { _ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:GA", "distilled", "") },
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			hash:   "sha256:GA",
			want:   trust.Allow,
			source: trust.SourceExplicitGrant,
		},
		{
			name:   "grant invalidated when content hash changes",
			setup:  func(s *trust.Store) { _ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:GA", "distilled", "") },
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			hash:   "sha256:CHANGED", // grant was for GA → no match → default DENY
			want:   trust.Deny,
			source: trust.SourceDefault,
		},
		{
			name: "multiple coexisting trusted versions (v1)",
			setup: func(s *trust.Store) {
				_ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:V1", "distilled", "9f3c1d7")
				_ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:V2", "distilled", "c40b8e2")
			},
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			hash:   "sha256:V1",
			want:   trust.Allow,
			source: trust.SourceExplicitGrant,
		},
		{
			name: "multiple coexisting trusted versions (v2)",
			setup: func(s *trust.Store) {
				_ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:V1", "distilled", "9f3c1d7")
				_ = s.AddGrant(trustRepo, "lib#fragments/solid", "sha256:V2", "distilled", "c40b8e2")
			},
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			hash:   "sha256:V2",
			want:   trust.Allow,
			source: trust.SourceExplicitGrant,
		},

		// --- bundle posture (SHA-agnostic) ---
		{
			name:   "bundle posture untrusted denies a grant-less item",
			setup:  func(s *trust.Store) { _ = s.SetBundle("experimental", trust.Deny) },
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "experimental", Kind: trust.KindFragment, Name: "f"},
			hash:   "sha256:anything",
			want:   trust.Deny,
			source: trust.SourceBundle,
		},
		{
			name:   "bundle posture trusted allows a grant-less item",
			setup:  func(s *trust.Store) { _ = s.SetBundle("blessed", trust.Allow) },
			ref:    trust.Ref{RepoURL: trustRepo, Bundle: "blessed", Kind: trust.KindMCP, Name: "m"},
			hash:   "sha256:anything",
			want:   trust.Allow,
			source: trust.SourceBundle,
		},

		// --- local content vs local executable ---
		{
			name:   "local fragment auto-allowed",
			ref:    trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"},
			hash:   "sha256:local",
			want:   trust.Allow,
			source: trust.SourceLocal,
		},
		{
			name:   "local prompt auto-allowed",
			ref:    trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindPrompt, Name: "y"},
			hash:   "sha256:local",
			want:   trust.Allow,
			source: trust.SourceLocal,
		},
		{
			name:   "local mcp auto-allowed (project-authored executable)",
			ref:    trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindMCP, Name: "z"},
			hash:   "sha256:local",
			want:   trust.Allow,
			source: trust.SourceLocal,
		},

		// --- renamed/moved identical content stays denied via denylist ---
		{
			name: "renamed/moved identical content stays denied via denylist",
			setup: func(s *trust.Store) {
				// Blacklisting old#fragments/foo records its content hash in the denylist.
				_ = s.Blacklist(trustRepo, "old#fragments/foo", "sha256:DEAD")
			},
			// A different (renamed) ref with the SAME content, on a TRUSTED remote.
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "new", Kind: trust.KindFragment, Name: "foo-v2"},
			hash:    "sha256:DEAD",
			remotes: []remoteSpec{{name: "acme", url: trustRepo, trust: true}},
			want:    trust.Deny,
			source:  trust.SourceDenylist,
		},

		// --- URL-variant blacklist match ---
		{
			name:   "URL-variant blacklist match (git@ vs https, case, .git)",
			setup:  func(s *trust.Store) { _ = s.Blacklist("https://github.com/Acme/Repo.git", "tooling#fragments/x", "") },
			ref:    trust.Ref{RepoURL: "git@github.com:acme/repo", Bundle: "tooling", Kind: trust.KindFragment, Name: "x"},
			hash:   "sha256:whatever",
			want:   trust.Deny,
			source: trust.SourceBlacklist,
		},

		// --- remote tier ---
		{
			name:    "trusted remote allows a grant-less item",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			hash:    "sha256:x",
			remotes: []remoteSpec{{name: "acme", url: trustRepo, trust: true}},
			want:    trust.Allow,
			source:  trust.SourceRemote,
		},
		{
			name:    "untrusted remote denies a grant-less item",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			hash:    "sha256:x",
			remotes: []remoteSpec{{name: "acme", url: trustRepo, trust: false}},
			want:    trust.Deny,
			source:  trust.SourceRemote,
		},
		{
			name:   "unknown remote falls to default deny",
			ref:    trust.Ref{RepoURL: "https://github.com/nobody/unknown", Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			hash:   "sha256:x",
			want:   trust.Deny,
			source: trust.SourceDefault,
		},

		// --- D4b safety boundary: a CLONED executable (RepoURL set, IsLocal false)
		//     follows its remote's trust, NEVER the local auto-allow tier. This is
		//     what keeps `local content is trusted` from leaking to clones. ---
		{
			name:    "trusted remote allows a cloned executable (mcp) — e.g. ctxloom-default",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "pg"},
			hash:    "sha256:x",
			remotes: []remoteSpec{{name: "acme", url: trustRepo, trust: true}},
			want:    trust.Allow,
			source:  trust.SourceRemote,
		},
		{
			name:    "untrusted remote denies a cloned executable (mcp) — no local leak",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "pg"},
			hash:    "sha256:x",
			remotes: []remoteSpec{{name: "acme", url: trustRepo, trust: false}},
			want:    trust.Deny,
			source:  trust.SourceRemote,
		},
		{
			name:   "cloned executable from an unknown remote fails closed (default deny)",
			ref:    trust.Ref{RepoURL: "https://github.com/nobody/unknown", Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"},
			hash:   "sha256:x",
			want:   trust.Deny,
			source: trust.SourceDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTrustStore(t)
			if tt.setup != nil {
				tt.setup(store)
			}
			reg := newRegistry(t, tt.remotes...)
			res, err := EffectiveTrust(nil, EffectiveTrustRequest{
				Ref:         tt.ref,
				ContentHash: tt.hash,
				Store:       store,
				Registry:    reg,
			})
			if err != nil {
				t.Fatalf("EffectiveTrust: %v", err)
			}
			if res.Decision != tt.want || res.Source != tt.source {
				t.Errorf("got {%s, %s}, want {%s, %s}", res.Decision, res.Source, tt.want, tt.source)
			}
		})
	}
}

// TestEffectiveTrust_CorruptStoreFailsClosed pins the security-critical rule:
// a corrupt/unreadable trust.yaml must DENY everything (fail closed), never
// degrade to allow-by-default like the registry does at startup.
func TestEffectiveTrust_CorruptStoreFailsClosed(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, ".ctxloom/trust.yaml", []byte("not: : valid: ["), 0o644); err != nil {
		t.Fatalf("seed corrupt trust.yaml: %v", err)
	}
	// No injected Store/Registry: getTrustStore loads from fs and must error.
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:         trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"}, // would normally ALLOW
		ContentHash: "sha256:x",
		FS:          fs,
	})
	if err != nil {
		t.Fatalf("EffectiveTrust should swallow the load error and deny, got err: %v", err)
	}
	if res.Decision != trust.Deny {
		t.Errorf("corrupt store resolve = %s, want deny (fail closed)", res.Decision)
	}
}

// --- mutation ops, end-to-end through the resolver ---------------------------

func seededLoader() (*bundles.Loader, string) {
	const seedKey = "https://github.com/acme/repo@bundles/tooling"
	b := &bundles.Bundle{
		Name:    seedKey,
		Version: "1.0",
		Fragments: map[string]bundles.BundleFragment{
			"solid": {Content: "always raw fragment body"},
		},
		MCP: map[string]bundles.BundleMCP{
			"postgres": {Command: "pg-mcp", Args: []string{"--port", "5432"}},
		},
	}
	loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{seedKey: b}))
	mcp := b.MCP["postgres"]
	return loader, mcp.ComputeContentHash()
}

func TestSetItemTrust_GrantsCurrentVersion(t *testing.T) {
	loader, mcpHash := seededLoader()
	store := newTrustStore(t)
	ref := "https://github.com/acme/repo@bundles/tooling#mcp/postgres"

	res, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, Store: store, Loader: loader})
	if err != nil {
		t.Fatalf("SetItemTrust: %v", err)
	}
	if res.ContentHash != mcpHash {
		t.Errorf("granted hash = %q, want %q", res.ContentHash, mcpHash)
	}
	if res.Ref != "tooling#mcp/postgres" || res.RepoURL != trustRepo {
		t.Errorf("grant key = {%q,%q}, want {tooling#mcp/postgres, %q}", res.Ref, res.RepoURL, trustRepo)
	}

	// The grant must make the local-untrusted executable resolve ALLOW for the
	// exact granted hash, and only that hash.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"}
	reg := newRegistry(t)
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, ContentHash: mcpHash, Store: store, Registry: reg})
	if err != nil {
		t.Fatalf("EffectiveTrust(granted): %v", err)
	}
	if got.Decision != trust.Allow || got.Source != trust.SourceExplicitGrant {
		t.Errorf("granted resolve = {%s,%s}, want {allow, explicit-grant}", got.Decision, got.Source)
	}
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, ContentHash: "sha256:other", Store: store, Registry: reg})
	if got.Decision != trust.Deny {
		t.Errorf("non-granted hash resolve = %s, want deny", got.Decision)
	}
}

func TestSetBlacklist_WritesBothComponents(t *testing.T) {
	loader, _ := seededLoader()
	store := newTrustStore(t)
	ref := "https://github.com/acme/repo@bundles/tooling#fragments/solid"

	res, err := SetBlacklist(nil, SetBlacklistRequest{Ref: ref, Store: store, Loader: loader})
	if err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}
	if res.ContentHash == "" {
		t.Fatal("blacklist should have recorded the item's current content hash")
	}

	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: true})
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}

	// Same content hash → denied via denylist (checked first).
	got, _ := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, ContentHash: res.ContentHash, Store: store, Registry: reg})
	if got.Decision != trust.Deny || got.Source != trust.SourceDenylist {
		t.Errorf("blacklisted-hash resolve = {%s,%s}, want {deny, denylist}", got.Decision, got.Source)
	}
	// A changed content hash → still denied, now via the sticky ref blacklist.
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, ContentHash: "sha256:changed", Store: store, Registry: reg})
	if got.Decision != trust.Deny || got.Source != trust.SourceBlacklist {
		t.Errorf("changed-hash resolve = {%s,%s}, want {deny, blacklist}", got.Decision, got.Source)
	}
}

func TestSetBundleTrust_RoundTrip(t *testing.T) {
	store := newTrustStore(t)
	if _, err := SetBundleTrust(nil, SetBundleTrustRequest{Bundle: "experimental", Trust: false, Store: store}); err != nil {
		t.Fatalf("SetBundleTrust(untrust): %v", err)
	}
	if dec, ok := store.BundlePosture("experimental"); !ok || dec != trust.Deny {
		t.Errorf("posture = %v,%v; want deny,true", dec, ok)
	}
	if _, err := SetBundleTrust(nil, SetBundleTrustRequest{Bundle: "experimental", Trust: true, Store: store}); err != nil {
		t.Fatalf("SetBundleTrust(trust): %v", err)
	}
	if dec, ok := store.BundlePosture("experimental"); !ok || dec != trust.Allow {
		t.Errorf("posture after re-trust = %v,%v; want allow,true", dec, ok)
	}
}

func TestParseTrustItemRef(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantRepo   string
		wantBundle string
		wantKind   trust.ItemKind
		wantName   string
		wantLocal  bool
		wantErr    bool
	}{
		{
			name: "canonical remote fragment", ref: "https://github.com/acme/repo@bundles/tooling#fragments/solid",
			wantRepo: "https://github.com/acme/repo", wantBundle: "tooling", wantKind: trust.KindFragment, wantName: "solid",
		},
		{
			name: "ctxloom:local mcp", ref: "ctxloom:local@bundles/dev#mcp/pg",
			wantBundle: "dev", wantKind: trust.KindMCP, wantName: "pg", wantLocal: true,
		},
		{
			name: "plain local bundle name", ref: "myb#prompts/review",
			wantBundle: "myb", wantKind: trust.KindPrompt, wantName: "review", wantLocal: true,
		},
		{name: "missing selector", ref: "tooling", wantErr: true},
		{name: "unknown kind", ref: "tooling#widgets/x", wantErr: true},
		{name: "empty name", ref: "tooling#fragments/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tref, _, _, err := parseTrustItemRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTrustItemRef(%q): %v", tt.ref, err)
			}
			if tref.RepoURL != tt.wantRepo || tref.Bundle != tt.wantBundle || tref.Kind != tt.wantKind ||
				tref.Name != tt.wantName || tref.IsLocal != tt.wantLocal {
				t.Errorf("got %+v, want repo=%q bundle=%q kind=%q name=%q local=%v",
					tref, tt.wantRepo, tt.wantBundle, tt.wantKind, tt.wantName, tt.wantLocal)
			}
		})
	}
}
