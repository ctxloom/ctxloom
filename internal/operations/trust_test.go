package operations

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

const trustRepo = "https://github.com/acme/repo"

// acmeBundle is the canonical bundle-ref prefix for the (remote) acme repo, so
// seed keys parse back to RepoURL == trustRepo with bundle paths after it.
const acmeBundle = "https://github.com/acme/repo@bundles/"

// promptHash recomputes the effective-content hash for a no-distill prompt body
// (preferDistilled true ⇒ raw bytes when there is no distilled form).
func promptHash(body string) string {
	p := bundles.BundleSkill{Content: body}
	h, _ := p.EffectiveContentHash(true)
	return h
}

// mcpHashOf is the executable-surface hash an MCP acceptance binds to.
func mcpHashOf(m bundles.BundleMCP) string { return m.ComputeContentHash() }

// mcpPayloadOf is the executable-surface PAYLOAD the decision function keys on
// (the same bytes mcpHashOf hashes). Panics only on an unreachable encode error.
func mcpPayloadOf(m bundles.BundleMCP) []byte {
	p, err := m.ContentPayload()
	if err != nil {
		panic(err)
	}
	return p
}

// pbytes/phash are the paired test primitive: pbytes(tag) is a distinct payload,
// phash(tag) is the hash the ledger records for it. Feeding pbytes(tag) to the
// decision function and recording phash(tag) in the store is how a test says
// "these exact bytes were reviewed" — the decision now keys on bytes, so a bare
// hash literal can no longer stand in for content (that was the forgeable-file
// shape this rework removes).
func pbytes(tag string) []byte { return []byte("payload:" + tag) }
func phash(tag string) string  { return bundles.HashPayload(pbytes(tag)) }

func newTrustStore(t *testing.T) *trust.Store {
	t.Helper()
	s, err := trust.New("", trust.WithFS(afero.NewMemMapFs()))
	if err != nil {
		t.Fatalf("trust.New: %v", err)
	}
	return s
}

type remoteSpec struct {
	name string
	url  string
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
	}
	return reg
}

// trustedPublisher is the principal a signed-content test attributes a bundle
// to — the value bundles.Bundle.Signer() would carry after a valid publish
// signature verified against allowed_signers (step 4).
const trustedPublisher = "bundles@ctxloom.dev"

// rawForm/distilledForm name the Form values the resolver keys accepted-hash
// slots on.
var (
	rawForm       = string(bundles.FormRaw)
	distilledForm = string(bundles.FormDistilled)
)

// TestEffectiveTrust_Cascade table-drives the decision function's full
// precedence and every step: rejected (ref state + denylist, beating local and
// trusted signer), local, builtin, trusted signer (step 4, replacing the
// deleted trusted-source), approved (current-form binding, lazy single slots),
// and the pending default.
func TestEffectiveTrust_Cascade(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*trust.Store)
		ref     trust.Ref
		payload []byte
		form    string
		signer  string
		want    trust.Decision
		source  trust.Source
	}{
		// --- rejected: ref state and content denylist ---
		{
			name:    "rejected ref state denies",
			setup:   func(s *trust.Store) { _ = s.SetRejected(trustRepo, "tooling#mcp/postgres", phash("H1")) },
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			payload: pbytes("H1"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection survives a content change (new bytes still denied)",
			setup:   func(s *trust.Store) { _ = s.SetRejected(trustRepo, "tooling#mcp/postgres", phash("H1")) },
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			payload: pbytes("H3-brand-new"), // never seen; the sticky ref rejection still wins
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:  "renamed identical content stays rejected via denylist, even from a trusted publisher",
			setup: func(s *trust.Store) { _ = s.SetRejected(trustRepo, "old#fragments/foo", phash("DEAD")) },
			// A different (renamed) ref with the SAME content, from a TRUSTED
			// publisher: the content rejection still denies (rejection beats the
			// signer exemption — it is step 1).
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "new", Kind: trust.KindFragment, Name: "foo-v2"},
			payload: pbytes("DEAD"),
			form:    rawForm,
			signer:  trustedPublisher,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection beats the local exemption",
			setup:   func(s *trust.Store) { _ = s.SetRejected(remote.LocalSource, "dev#fragments/x", phash("X")) },
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("X"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection beats a trusted publisher (ref state)",
			setup:   func(s *trust.Store) { _ = s.SetRejected(trustRepo, "tooling#fragments/bad") },
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "bad"},
			payload: pbytes("whatever"),
			form:    rawForm,
			signer:  trustedPublisher,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection beats a builtin (a user can reject the ctxloom release key's content)",
			setup:   func(s *trust.Store) { _ = s.SetRejected("builtin:ctxloom", "kit#fragments/x") },
			ref:     trust.Ref{IsBuiltin: true, Bundle: "kit", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("whatever"),
			form:    rawForm,
			signer:  trust.BuiltinSigner,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},

		// --- URL-variant rejection match ---
		{
			name:    "URL-variant rejection match (git@ vs https, case, .git)",
			setup:   func(s *trust.Store) { _ = s.SetRejected("https://github.com/Acme/Repo.git", "tooling#fragments/x") },
			ref:     trust.Ref{RepoURL: "git@github.com:acme/repo", Bundle: "tooling", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("whatever"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},

		// --- local first-party exemption (all kinds) ---
		{
			name:    "local fragment auto-allowed",
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("local"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceLocal,
		},
		{
			name:    "local prompt auto-allowed",
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindPrompt, Name: "y"},
			payload: pbytes("local"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceLocal,
		},
		{
			name:    "local mcp auto-allowed (project-authored executable)",
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindMCP, Name: "z"},
			payload: pbytes("local"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceLocal,
		},

		// --- builtin ---
		{
			name:    "builtin allowed by default at its own step",
			ref:     trust.Ref{IsBuiltin: true, Bundle: "kit", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("builtin body"),
			form:    rawForm,
			signer:  trust.BuiltinSigner,
			want:    trust.Allow,
			source:  trust.SourceBuiltin,
		},

		// --- trusted signer (step 4) ---
		{
			name:    "trusted publisher allows an unreviewed item",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			payload: pbytes("x"),
			form:    rawForm,
			signer:  trustedPublisher,
			want:    trust.Allow,
			source:  trust.SourceTrustedSigner,
		},
		{
			name:    "trusted publisher allows a signed executable (mcp)",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "pg"},
			payload: pbytes("x"),
			form:    rawForm,
			signer:  trustedPublisher,
			want:    trust.Allow,
			source:  trust.SourceTrustedSigner,
		},
		{
			name:    "the synthetic builtin identity is NOT a trusted publisher on a non-builtin ref",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			payload: pbytes("x"),
			form:    rawForm,
			signer:  trust.BuiltinSigner, // must NOT launder into step 4
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name:    "unsigned remote item is NOT trusted (pending)",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			payload: pbytes("x"),
			form:    rawForm,
			signer:  "", // no verified publisher
			want:    trust.Deny,
			source:  trust.SourcePending,
		},

		// --- approved: current-form binding ---
		{
			name: "approved allows the exact raw bytes",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/solid", phash("R"), phash("D"))
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("R"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceAccepted,
		},
		{
			name: "approved allows the exact distilled bytes",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/solid", phash("R"), phash("D"))
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("D"),
			form:    distilledForm,
			want:    trust.Allow,
			source:  trust.SourceAccepted,
		},
		{
			name: "approval invalidated when content changes (back to pending)",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/solid", phash("R"), phash("D"))
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("CHANGED"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name: "form-flip closed: a raw approval cannot validate a distilled exposure",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/solid", phash("R"), phash("D"))
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("R"), // the RAW bytes presented as the distilled form
			form:    distilledForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name: "lazy single-slot approval allows only its own form (raw slot)",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/lazy", phash("RAWONLY"), "")
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "lazy"},
			payload: pbytes("RAWONLY"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceAccepted,
		},
		{
			name: "lazy empty slot for the current form is pending (fail closed)",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/lazy", phash("RAWONLY"), "")
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "lazy"},
			payload: pbytes("DISTNOW"), // current exposure is the distilled form; its slot is empty
			form:    distilledForm,
			want:    trust.Deny, source: trust.SourcePending,
		},
		{
			name: "unknown form matches no slot (fail closed)",
			setup: func(s *trust.Store) {
				_ = s.SetAccepted(trustRepo, "lib#fragments/solid", phash("R"), "")
			},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("R"),
			form:    "", // caller failed to say which form — never allow on a guess
			want:    trust.Deny,
			source:  trust.SourcePending,
		},

		// --- pending default ---
		{
			name:    "unsigned unknown remote falls to pending deny",
			ref:     trust.Ref{RepoURL: "https://github.com/nobody/unknown", Bundle: "b", Kind: trust.KindFragment, Name: "f"},
			payload: pbytes("x"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name:    "unsigned executable from an unknown remote fails closed (pending)",
			ref:     trust.Ref{RepoURL: "https://github.com/nobody/unknown", Bundle: "tooling", Kind: trust.KindHook, Name: "pre_tool/0"},
			payload: pbytes("x"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTrustStore(t)
			if tt.setup != nil {
				tt.setup(store)
			}
			res, err := EffectiveTrust(nil, EffectiveTrustRequest{
				Ref:     tt.ref,
				Payload: tt.payload,
				Form:    tt.form,
				Signer:  tt.signer,
				Store:   store,
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
	// No injected Store: getTrustStore loads from fs and must error.
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"}, // would normally ALLOW
		Payload: pbytes("x"),
		Form:    rawForm,
		FS:      fs,
	})
	if err != nil {
		t.Fatalf("EffectiveTrust should swallow the load error and deny, got err: %v", err)
	}
	if res.Decision != trust.Deny {
		t.Errorf("corrupt store resolve = %s, want deny (fail closed)", res.Decision)
	}
}

// TestEffectiveTrust_V1MigratedGrantResolves drives the v1→v2 migration through
// the resolver end to end: a REAL v1 trust.yaml (grant + blacklist) is loaded
// from disk and the migrated states decide exposure — the grant allows exactly
// its recorded form-hash and gates a changed hash; the blacklist rejects.
func TestEffectiveTrust_V1MigratedGrantResolves(t *testing.T) {
	fs := afero.NewMemMapFs()
	v1 := `version: 1
grants:
  - repo_url: https://github.com/acme/repo
    ref: tooling#fragments/solid
    content_hash: ` + phash("V1") + `
    form: raw
blacklist:
  - repo_url: https://github.com/acme/repo
    ref: tooling#fragments/evil
`
	if err := afero.WriteFile(fs, ".ctxloom/trust.yaml", []byte(v1), 0o644); err != nil {
		t.Fatalf("seed v1 trust.yaml: %v", err)
	}

	resolve := func(ref trust.Ref, payload []byte, form string) *EffectiveTrustResult {
		t.Helper()
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: ref, Payload: payload, Form: form, FS: fs,
		})
		if err != nil {
			t.Fatalf("EffectiveTrust: %v", err)
		}
		return res
	}

	solid := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	if res := resolve(solid, pbytes("V1"), rawForm); res.Decision != trust.Allow || res.Source != trust.SourceAccepted {
		t.Errorf("migrated grant at its bytes = {%s,%s}, want {allow, accepted}", res.Decision, res.Source)
	}
	if res := resolve(solid, pbytes("UPSTREAM-EDIT"), rawForm); res.Decision != trust.Deny || res.Source != trust.SourcePending {
		t.Errorf("migrated grant at changed bytes = {%s,%s}, want {deny, pending}", res.Decision, res.Source)
	}
	// The lazily-migrated grant filled only the raw slot: a distilled exposure
	// of this ref gates until re-accepted.
	if res := resolve(solid, pbytes("SOMEDISTILL"), distilledForm); res.Decision != trust.Deny || res.Source != trust.SourcePending {
		t.Errorf("migrated grant, other form = {%s,%s}, want {deny, pending}", res.Decision, res.Source)
	}

	evil := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "evil"}
	if res := resolve(evil, pbytes("anything"), rawForm); res.Decision != trust.Deny || res.Source != trust.SourceRejected {
		t.Errorf("migrated blacklist = {%s,%s}, want {deny, rejected}", res.Decision, res.Source)
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
			"dual":  {Content: "raw body", Distilled: "distilled body"},
		},
		MCP: map[string]bundles.BundleMCP{
			"postgres": {Command: "pg-mcp", Args: []string{"--port", "5432"}},
		},
	}
	loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{seedKey: b}))
	mcp := b.MCP["postgres"]
	return loader, mcp.ComputeContentHash()
}

// mcpPayload is the postgres server's canonical preimage — the bytes the
// decision function is fed for that item (its hash is seededLoader's return).
func seededMCPPayload() []byte {
	return mcpPayloadOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})
}

func TestSetItemTrust_AcceptsCurrentVersion(t *testing.T) {
	loader, mcpHash := seededLoader()
	store := newTrustStore(t)
	ref := "https://github.com/acme/repo@bundles/tooling#mcp/postgres"

	res, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, Store: store, Loader: loader})
	if err != nil {
		t.Fatalf("SetItemTrust: %v", err)
	}
	if res.Status != "accepted" {
		t.Errorf("status = %q, want accepted", res.Status)
	}
	if res.RawHash != mcpHash || res.DistilledHash != "" {
		t.Errorf("accepted hashes = {raw:%q distilled:%q}, want {%q, empty}", res.RawHash, res.DistilledHash, mcpHash)
	}
	if res.Ref != "tooling#mcp/postgres" || res.RepoURL != trustRepo {
		t.Errorf("accepted key = {%q,%q}, want {tooling#mcp/postgres, %q}", res.Ref, res.RepoURL, trustRepo)
	}

	// The acceptance must make the unsigned remote executable resolve ALLOW for
	// the exact accepted bytes, and only those bytes.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"}
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: seededMCPPayload(), Form: rawForm, Store: store})
	if err != nil {
		t.Fatalf("EffectiveTrust(accepted): %v", err)
	}
	if got.Decision != trust.Allow || got.Source != trust.SourceAccepted {
		t.Errorf("accepted resolve = {%s,%s}, want {allow, accepted}", got.Decision, got.Source)
	}
	_ = mcpHash
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("other"), Form: rawForm, Store: store})
	if got.Decision != trust.Deny {
		t.Errorf("non-accepted bytes resolve = %s, want deny", got.Decision)
	}
}

// TestSetItemTrust_RecordsBothFormHashes pins the hash-pair contract: accepting
// an item with a distilled form records BOTH hashes, so both materializations
// are reviewed-once and a change to either re-gates.
func TestSetItemTrust_RecordsBothFormHashes(t *testing.T) {
	loader, _ := seededLoader()
	store := newTrustStore(t)

	res, err := SetItemTrust(nil, SetItemTrustRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/dual", Store: store, Loader: loader,
	})
	if err != nil {
		t.Fatalf("SetItemTrust: %v", err)
	}
	frag := bundles.BundleFragment{Content: "raw body", Distilled: "distilled body"}
	wantRaw, _ := frag.EffectiveContentHash(false)
	wantDistilled, _ := frag.EffectiveContentHash(true)
	if res.RawHash != wantRaw || res.DistilledHash != wantDistilled {
		t.Errorf("hash pair = {%q,%q}, want {%q,%q}", res.RawHash, res.DistilledHash, wantRaw, wantDistilled)
	}

	// Both forms resolve accepted at their own bytes.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "dual"}
	rawPayload, _ := frag.ContentPayload(false)
	distilledPayload, _ := frag.ContentPayload(true)
	for form, payload := range map[string][]byte{rawForm: rawPayload, distilledForm: distilledPayload} {
		got, _ := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: payload, Form: form, Store: store})
		if got.Decision != trust.Allow || got.Source != trust.SourceAccepted {
			t.Errorf("form %s resolve = {%s,%s}, want {allow, accepted}", form, got.Decision, got.Source)
		}
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
	if res.Status != "rejected" {
		t.Errorf("status = %q, want rejected", res.Status)
	}
	if len(res.ContentHashes) == 0 {
		t.Fatal("rejection should have recorded the item's current content hash(es)")
	}

	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	// The exact rejected bytes (the "solid" fragment body from seededLoader).
	rejectedPayload := []byte("always raw fragment body")

	// Same bytes → denied via the rejected step, even from a TRUSTED publisher.
	got, _ := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: rejectedPayload, Form: rawForm, Signer: trustedPublisher, Store: store})
	if got.Decision != trust.Deny || got.Source != trust.SourceRejected {
		t.Errorf("rejected-bytes resolve = {%s,%s}, want {deny, rejected}", got.Decision, got.Source)
	}
	// Changed bytes → still denied via the sticky ref-level rejection.
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("changed"), Form: rawForm, Signer: trustedPublisher, Store: store})
	if got.Decision != trust.Deny || got.Source != trust.SourceRejected {
		t.Errorf("changed-bytes resolve = {%s,%s}, want {deny, rejected}", got.Decision, got.Source)
	}
	// A renamed identical copy (different ref, same content) stays rejected.
	renamed := trust.Ref{RepoURL: trustRepo, Bundle: "other", Kind: trust.KindFragment, Name: "clone"}
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: renamed, Payload: rejectedPayload, Form: rawForm, Signer: trustedPublisher, Store: store})
	if got.Decision != trust.Deny || got.Source != trust.SourceRejected {
		t.Errorf("renamed-copy resolve = {%s,%s}, want {deny, rejected}", got.Decision, got.Source)
	}
}

// TestSetBlacklist_DenylistsBothForms proves rejecting a distillable item
// denylists BOTH form hashes, so neither materialization can sneak back in
// under another ref.
func TestSetBlacklist_DenylistsBothForms(t *testing.T) {
	loader, _ := seededLoader()
	store := newTrustStore(t)

	res, err := SetBlacklist(nil, SetBlacklistRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/dual", Store: store, Loader: loader,
	})
	if err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}
	frag := bundles.BundleFragment{Content: "raw body", Distilled: "distilled body"}
	wantRaw, _ := frag.EffectiveContentHash(false)
	wantDistilled, _ := frag.EffectiveContentHash(true)
	if len(res.ContentHashes) != 2 {
		t.Fatalf("ContentHashes = %v, want both form hashes", res.ContentHashes)
	}
	for _, h := range []string{wantRaw, wantDistilled} {
		if !store.DeniedHash(h) {
			t.Errorf("form hash %q missing from the denylist", h)
		}
	}
}

func TestParseTrustItemRef(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		wantRepo    string
		wantBundle  string
		wantKind    trust.ItemKind
		wantName    string
		wantLocal   bool
		wantBuiltin bool
		wantErr     bool
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
		{
			name: "builtin source ref", ref: "builtin:taskloom#mcp/taskloom",
			wantBundle: "taskloom", wantKind: trust.KindMCP, wantName: "taskloom", wantBuiltin: true,
		},
		{name: "missing selector", ref: "tooling", wantErr: true},
		{name: "unknown kind", ref: "tooling#widgets/x", wantErr: true},
		{name: "empty name", ref: "tooling#fragments/", wantErr: true},
		// Fail-closed: an unrecognized source ref that LOOKS like an attempted
		// canonical/local ref must error, never silently resolve local (the
		// fail-open bug — see TestContentGate_UnrecognizedSourceRef_FailsClosed
		// for the end-to-end proof through the gate).
		{name: "malformed https ref (missing @type/path)", ref: "https://github.com/acme/repo#fragments/x", wantErr: true},
		{name: "malformed git@ ref (missing @type/path)", ref: "git@github.com:acme/repo#fragments/x", wantErr: true},
		{name: "malformed ctxloom:local ref (unknown type)", ref: "ctxloom:local@widgets/x#fragments/y", wantErr: true},
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
				tref.Name != tt.wantName || tref.IsLocal != tt.wantLocal || tref.IsBuiltin != tt.wantBuiltin {
				t.Errorf("got %+v, want repo=%q bundle=%q kind=%q name=%q local=%v builtin=%v",
					tref, tt.wantRepo, tt.wantBundle, tt.wantKind, tt.wantName, tt.wantLocal, tt.wantBuiltin)
			}
		})
	}
}
