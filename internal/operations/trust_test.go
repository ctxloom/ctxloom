package operations

import (
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

const trustRepo = "https://github.com/acme/repo"

// acmeBundle is the canonical bundle-ref prefix for the (remote) acme repo, so
// seed keys parse back to RepoURL == trustRepo with bundle paths after it.
const acmeBundle = "https://github.com/acme/repo@bundles/"

// mcpPayloadOf is the executable-surface PAYLOAD the decision function keys on
// (the same bytes mcpHashOf hashes). Panics only on an unreachable encode error.
func mcpPayloadOf(m bundles.BundleMCP) []byte {
	p, err := m.ContentPayload()
	if err != nil {
		panic(err)
	}
	return p
}

// pbytes is a distinct test payload for a given tag — the decision function
// keys on bytes, so a bare hash literal can no longer stand in for content
// (that was the forgeable-file shape this rework removes).
func pbytes(tag string) []byte { return []byte("payload:" + tag) }

// fakeRecords is a minimal, closure-driven ReviewRecords for testing the
// DECISION FUNCTION itself (steps 1-6): it never touches a real
// countersignature store, so TestEffectiveTrust_Cascade exercises exactly the
// precedence/cascade logic and nothing about signature verification (which
// has its own dedicated tests — see TestCountersignRecords_* and the
// countersign/signing packages).
type fakeRecords struct {
	rejected func(trust.Ref, []byte) bool
	approved func(trust.Ref, []byte, string) bool
}

func (f fakeRecords) Rejected(ref trust.Ref, payload []byte) bool {
	return f.rejected != nil && f.rejected(ref, payload)
}

func (f fakeRecords) Approved(ref trust.Ref, payload []byte, form string) bool {
	return f.approved != nil && f.approved(ref, payload, form)
}

// byBytes builds a fakeRecords whose Rejected/Approved match by exact byte
// equality against the given "recorded" payload(s) — the decision function's
// new shape (bytes, not hashes) made real.
func rejectedByBytes(recorded ...[]byte) func(trust.Ref, []byte) bool {
	return func(_ trust.Ref, payload []byte) bool {
		for _, r := range recorded {
			if len(payload) > 0 && string(payload) == string(r) {
				return true
			}
		}
		return false
	}
}

// fakeRetraction is a minimal, closure-driven RetractionRecords for testing
// the DECISION FUNCTION's retraction step in isolation — mirroring fakeRecords
// for Rejected/Approved above. The zero value fakeRetraction{} reports
// "never retracted", so cascade cases that don't care about retraction can
// simply omit the field.
type fakeRetraction struct {
	retracted func(trust.Ref) (bool, string)
}

func (f fakeRetraction) Retracted(ref trust.Ref) (bool, string) {
	if f.retracted == nil {
		return false, ""
	}
	return f.retracted(ref)
}

// retractedFor builds a fakeRetraction that reports ref as retracted (with
// reason) whenever bundle/name match, mirroring rejectedByBytes's shape.
func retractedFor(bundle, name, reason string) fakeRetraction {
	return fakeRetraction{retracted: func(r trust.Ref) (bool, string) {
		if r.Bundle == bundle && r.Name == name {
			return true, reason
		}
		return false, ""
	}}
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
// precedence and every step: rejected (ref state + content match, beating
// local and trusted signer), local, builtin, trusted signer (step 4,
// replacing the deleted trusted-source), approved (current-form binding), and
// the pending default. It drives EffectiveTrust directly over a fakeRecords
// so it tests exactly the cascade — untouched by this slice — never signature
// verification.
func TestEffectiveTrust_Cascade(t *testing.T) {
	tests := []struct {
		name       string
		records    fakeRecords
		retraction fakeRetraction
		ref        trust.Ref
		payload    []byte
		form       string
		signer     string
		want       trust.Decision
		source     trust.Source
	}{
		// --- rejected: ref state and content match ---
		{
			name:    "rejected ref state denies",
			records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool { return r.Bundle == "tooling" && r.Name == "postgres" }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			payload: pbytes("H1"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection survives a content change (new bytes still denied)",
			records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool { return r.Bundle == "tooling" && r.Name == "postgres" }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			payload: pbytes("H3-brand-new"), // never seen; the sticky ref rejection still wins
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "renamed identical content stays rejected via content match, even from a trusted publisher",
			records: fakeRecords{rejected: rejectedByBytes(pbytes("DEAD"))},
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
			records: fakeRecords{rejected: rejectedByBytes(pbytes("X"))},
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("X"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection beats a trusted publisher (ref state)",
			records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool { return r.Bundle == "tooling" && r.Name == "bad" }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "bad"},
			payload: pbytes("whatever"),
			form:    rawForm,
			signer:  trustedPublisher,
			want:    trust.Deny,
			source:  trust.SourceRejected,
		},
		{
			name:    "rejection beats a builtin (a user can reject the ctxloom release key's content)",
			records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool { return r.IsBuiltin && r.Bundle == "kit" && r.Name == "x" }},
			ref:     trust.Ref{IsBuiltin: true, Bundle: "kit", Kind: trust.KindFragment, Name: "x"},
			payload: pbytes("whatever"),
			form:    rawForm,
			signer:  trust.BuiltinSigner,
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
		{
			name:    "local skill auto-allowed (project-authored Agent Skill package)",
			ref:     trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindSkill, Name: "reviewer"},
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

		// --- retracted (step 2): a peer of rejected, beats every allow below ---
		{
			name:       "retracted bundle denies, beating a trusted signer",
			retraction: retractedFor("tooling", "postgres", "compromised release"),
			ref:        trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"},
			payload:    pbytes("x"),
			form:       rawForm,
			signer:     trustedPublisher,
			want:       trust.Deny,
			source:     trust.SourceRetracted,
		},
		{
			name:       "retraction of one bundle does not deny a different bundle from the same signer",
			retraction: retractedFor("tooling", "postgres", "compromised release"),
			ref:        trust.Ref{RepoURL: trustRepo, Bundle: "other", Kind: trust.KindFragment, Name: "solid"},
			payload:    pbytes("x"),
			form:       rawForm,
			signer:     trustedPublisher,
			want:       trust.Allow,
			source:     trust.SourceTrustedSigner,
		},
		{
			// Retraction is a REMOTE-manifest concept and local/builtin refs never
			// carry a lockfile entry in production (RetractionRecords.Retracted
			// guards on RepoURL/IsLocal/IsBuiltin) — but the DECISION FUNCTION's
			// ordering is still exercised directly here, the same way the rejected
			// block above proves rejection beats local/builtin regardless of
			// whether a real store would ever produce that combination.
			name:       "retraction ordering beats the local exemption",
			retraction: fakeRetraction{retracted: func(r trust.Ref) (bool, string) { return r.IsLocal && r.Bundle == "dev", "withdrawn" }},
			ref:        trust.Ref{IsLocal: true, Bundle: "dev", Kind: trust.KindFragment, Name: "x"},
			payload:    pbytes("x"),
			form:       rawForm,
			want:       trust.Deny,
			source:     trust.SourceRetracted,
		},
		{
			name:       "retraction ordering beats the builtin exemption",
			retraction: fakeRetraction{retracted: func(r trust.Ref) (bool, string) { return r.IsBuiltin && r.Bundle == "kit", "withdrawn" }},
			ref:        trust.Ref{IsBuiltin: true, Bundle: "kit", Kind: trust.KindFragment, Name: "x"},
			payload:    pbytes("x"),
			form:       rawForm,
			signer:     trust.BuiltinSigner,
			want:       trust.Deny,
			source:     trust.SourceRetracted,
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
			name:    "trusted publisher allows a signed skill package",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindSkill, Name: "reviewer"},
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
		{
			// The TDD "unsigned / untrusted-publisher skill" contract: a real
			// Agent Skill package from a publisher this machine does not trust
			// stays pending, mirroring every other kind above — a skill is not
			// trusted until a human reviews and accepts it (see
			// TestSetItemTrust_ApprovesSkillCurrentVersion for the full
			// review+accept round trip).
			name:    "unsigned remote skill is NOT trusted (pending)",
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindSkill, Name: "reviewer"},
			payload: pbytes("x"),
			form:    rawForm,
			signer:  "",
			want:    trust.Deny,
			source:  trust.SourcePending,
		},

		// --- approved: current-form binding ---
		{
			name:    "approved allows the exact raw bytes",
			records: fakeRecords{approved: func(_ trust.Ref, p []byte, f string) bool { return string(p) == string(pbytes("R")) && f == rawForm }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("R"),
			form:    rawForm,
			want:    trust.Allow,
			source:  trust.SourceAccepted,
		},
		{
			name: "approved allows the exact distilled bytes",
			records: fakeRecords{approved: func(_ trust.Ref, p []byte, f string) bool {
				return string(p) == string(pbytes("D")) && f == distilledForm
			}},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("D"),
			form:    distilledForm,
			want:    trust.Allow,
			source:  trust.SourceAccepted,
		},
		{
			name:    "approval invalidated when content changes (back to pending)",
			records: fakeRecords{approved: func(_ trust.Ref, p []byte, f string) bool { return string(p) == string(pbytes("R")) && f == rawForm }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("CHANGED"),
			form:    rawForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name:    "form-flip closed: a raw approval cannot validate a distilled exposure",
			records: fakeRecords{approved: func(_ trust.Ref, p []byte, f string) bool { return string(p) == string(pbytes("R")) && f == rawForm }},
			ref:     trust.Ref{RepoURL: trustRepo, Bundle: "lib", Kind: trust.KindFragment, Name: "solid"},
			payload: pbytes("R"), // the RAW bytes presented as the distilled form
			form:    distilledForm,
			want:    trust.Deny,
			source:  trust.SourcePending,
		},
		{
			name:    "unknown form matches no slot (fail closed)",
			records: fakeRecords{approved: func(_ trust.Ref, p []byte, f string) bool { return string(p) == string(pbytes("R")) && f == rawForm }},
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
			res, err := EffectiveTrust(nil, EffectiveTrustRequest{
				Ref:        tt.ref,
				Posture:    postureCtxOf(tt.ref),
				Provenance: postureProvOf(tt.ref),
				Payload:    tt.payload,
				Form:       tt.form,
				Signer:     tt.signer,
				Records:    tt.records,
				Retraction: tt.retraction,
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

// TestEffectiveTrust_DefaultRecords_NothingApprovedOrRejected proves the
// seam: when Records is nil, EffectiveTrust builds the default (the real
// countersignature stores) rather than crashing or panicking, and — with no
// stores populated (a fresh project, HOME pointed at an empty temp dir) —
// everything remote resolves pending, exactly as an empty store always did.
func TestEffectiveTrust_DefaultRecords_NothingApprovedOrRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:        trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
		Posture:    postureCtxOf(trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}),
		Provenance: postureProvOf(trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}),
		Payload:    pbytes("x"),
		Form:       rawForm,
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourcePending, res.Source)
}

// --- mutation ops, end-to-end through the resolver ---------------------------

func seededLoader(t *testing.T) (*bundles.Loader, string) {
	t.Helper()
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
		Skills: map[string]bundles.BundleSkill{
			"reviewer": {Files: map[string]bundles.SkillFileMeta{
				"SKILL.md": {SHA256: "sha256:abc123", Mode: "0644"},
			}},
		},
	}
	loader := seedLoader(t, map[string]*bundles.Bundle{seedKey: b})
	mcp := b.MCP["postgres"]
	return loader, mcp.ComputeContentHash()
}

// mcpPayload is the postgres server's canonical preimage — the bytes the
// decision function is fed for that item.
func seededMCPPayload() []byte {
	return mcpPayloadOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})
}

// seededSkillPayload is the "reviewer" skill's canonical manifest preimage —
// the bytes the decision function is fed for that item, mirroring
// seededMCPPayload.
func seededSkillPayload() []byte {
	skill := bundles.BundleSkill{Files: map[string]bundles.SkillFileMeta{
		"SKILL.md": {SHA256: "sha256:abc123", Mode: "0644"},
	}}
	// An authored manifest is present, so the preimage never consults the
	// filesystem (BundleSkill.EffectiveManifest short-circuits).
	payload, err := skill.ContentPayload(nil, "", "reviewer")
	if err != nil {
		panic(err)
	}
	return payload
}

func TestSetItemTrust_ApprovesCurrentVersion(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	ref := "https://github.com/acme/repo@bundles/tooling#mcp/postgres"

	res, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, Signer: fx.signer, Root: fx.root, UserStore: fx.user, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "approved", res.Status)
	assert.False(t, res.Unsigned)
	assert.Equal(t, "user", res.Store)
	// KeyFingerprint now reuses the `principal` the approval was
	// keyed under instead of recomputing it. Assert the VALUE, not just
	// non-emptiness — the two must remain the same fingerprint.
	assert.Equal(t, ssh.FingerprintSHA256(fx.signer.PublicKey()), res.KeyFingerprint)
	assert.Equal(t, "tooling#mcp/postgres", res.Ref)
	assert.Equal(t, trustRepo, res.RepoURL)

	// The approval must make the unsigned remote executable resolve ALLOW for
	// the exact approved bytes, and only those bytes.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindMCP, Name: "postgres"}
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: seededMCPPayload(), Form: rawForm, Records: fx.records()})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, got.Decision)
	assert.Equal(t, trust.SourceAccepted, got.Source)

	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("other"), Form: rawForm, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
}

// TestSetItemTrust_ApprovesSkillCurrentVersion mirrors
// TestSetItemTrust_ApprovesCurrentVersion for the NEW trust.KindSkill kind:
// a skill from an untrusted/unsigned publisher is NOT trusted
// (pending) until a human reviews and accepts it — exactly like a
// command/prompt — and once SetItemTrust records that acceptance, the exact
// approved manifest bytes resolve ALLOW/SourceAccepted while any other
// (never-reviewed) skill in the same bundle stays pending.
func TestSetItemTrust_ApprovesSkillCurrentVersion(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	ref := "https://github.com/acme/repo@bundles/tooling#skills/reviewer"

	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindSkill, Name: "reviewer"}
	skillPayload := seededSkillPayload()

	// Before review: an unsigned remote skill is pending, exactly like an
	// unsigned remote command/fragment/mcp (mirrors "unsigned remote item is
	// NOT trusted (pending)" in TestEffectiveTrust_Cascade).
	before, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: skillPayload, Form: rawForm, Records: fx.records()})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, before.Decision)
	assert.Equal(t, trust.SourcePending, before.Source)

	res, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, Signer: fx.signer, Root: fx.root, UserStore: fx.user, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "approved", res.Status)
	assert.False(t, res.Unsigned)
	assert.Equal(t, "user", res.Store)
	// KeyFingerprint now reuses the `principal` the approval was
	// keyed under instead of recomputing it. Assert the VALUE, not just
	// non-emptiness — the two must remain the same fingerprint.
	assert.Equal(t, ssh.FingerprintSHA256(fx.signer.PublicKey()), res.KeyFingerprint)
	assert.Equal(t, "tooling#skills/reviewer", res.Ref, "the stored key uses trust.KindSkill.Dir() == \"skills\"")
	assert.Equal(t, trustRepo, res.RepoURL)

	// After review+accept: the exact approved manifest bytes now resolve
	// ALLOW.
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: skillPayload, Form: rawForm, Records: fx.records()})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, got.Decision)
	assert.Equal(t, trust.SourceAccepted, got.Source)

	// A DIFFERENT, never-reviewed skill in the same bundle stays pending —
	// approving one skill must not launder any other.
	other := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindSkill, Name: "unreviewed"}
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: other, Payload: pbytes("other skill body"), Form: rawForm, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourcePending, got.Source)

	// A CHANGED manifest for the same ref (e.g. a script edited after review)
	// also reverts to pending — editing any file re-triggers review.
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("tampered manifest"), Form: rawForm, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourcePending, got.Source)
}

// TestSetItemTrust_ApprovesBothForms pins the "approve of a dual-form item
// covers BOTH forms" contract: approving records a countersignature over the
// raw bytes AND the distilled bytes, so both materializations are
// reviewed-once and a change to either re-gates.
func TestSetItemTrust_ApprovesBothForms(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)

	res, err := SetItemTrust(nil, SetItemTrustRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/dual", Signer: fx.signer, Root: fx.root, UserStore: fx.user, Loader: loader,
	})
	require.NoError(t, err)
	assert.Equal(t, "approved", res.Status)

	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "dual"}
	frag := bundles.BundleFragment{Content: "raw body", Distilled: "distilled body"}
	rawPayload, _ := frag.ContentPayload(false)
	distilledPayload, _ := frag.ContentPayload(true)
	for form, payload := range map[string][]byte{rawForm: rawPayload, distilledForm: distilledPayload} {
		got, _ := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: payload, Form: form, Records: fx.records()})
		if got.Decision != trust.Allow || got.Source != trust.SourceAccepted {
			t.Errorf("form %s resolve = {%s,%s}, want {allow, accepted}", form, got.Decision, got.Source)
		}
	}
}

func TestSetBlacklist_WritesBothComponents(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	ref := "https://github.com/acme/repo@bundles/tooling#fragments/solid"

	res, err := SetBlacklist(nil, SetBlacklistRequest{Ref: ref, Signer: fx.signer, Root: fx.root, UserStore: fx.user, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "rejected", res.Status)
	require.NotEmpty(t, res.ContentForms, "rejection should have recorded a content countersignature for the item's current form(s)")

	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	// The exact rejected bytes (the "solid" fragment body from seededLoader).
	rejectedPayload := []byte("always raw fragment body")

	// Same bytes → denied via the rejected step, even from a TRUSTED publisher.
	got, _ := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: rejectedPayload, Form: rawForm, Signer: trustedPublisher, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourceRejected, got.Source)

	// Changed bytes → still denied via the sticky ref-level rejection.
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("changed"), Form: rawForm, Signer: trustedPublisher, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourceRejected, got.Source)

	// A renamed identical copy (different ref, same content) stays rejected.
	renamed := trust.Ref{RepoURL: trustRepo, Bundle: "other", Kind: trust.KindFragment, Name: "clone"}
	got, _ = EffectiveTrust(nil, EffectiveTrustRequest{Ref: renamed, Payload: rejectedPayload, Form: rawForm, Signer: trustedPublisher, Records: fx.records()})
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourceRejected, got.Source)
}

// TestSetBlacklist_RejectsBothForms proves rejecting a distillable item
// content-rejects BOTH forms, so neither materialization can sneak back in
// under another ref.
func TestSetBlacklist_RejectsBothForms(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)

	res, err := SetBlacklist(nil, SetBlacklistRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/dual", Signer: fx.signer, Root: fx.root, UserStore: fx.user, Loader: loader,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{rawForm, distilledForm}, res.ContentForms)

	// Both forms verify as content-rejected — content-reject is deliberately
	// ref-omitted, so it is never queried under a ref at all.
	_, ok := fx.user.VerifiedContentReject(signing.AttestFragmentRaw, []byte("raw body"), fx.root, time.Now())
	assert.True(t, ok)
	_, ok = fx.user.VerifiedContentReject(signing.AttestFragmentDistilled, []byte("distilled body"), fx.root, time.Now())
	assert.True(t, ok)
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
		// A companion loadout ref — the "one thing that can go wrong":
		// remote.ParseReference must RECOGNIZE it (so it never falls into the
		// unrecognized-source guard below) and it must land as neither local
		// nor builtin, so it reaches EffectiveTrust's trusted-signer/approved/
		// pending steps like any other third-party content. See
		// TestEffectiveTrust_CompanionRef_NeitherLocalNorDenied for the
		// end-to-end gate proof.
		{
			name: "companion loadout ref", ref: "ctxloom:companion@ltk#fragments/ltk",
			wantRepo: remote.CompanionSource, wantBundle: "ltk", wantKind: trust.KindFragment, wantName: "ltk",
		},
		{name: "missing selector", ref: "tooling", wantErr: true},
		{name: "unknown kind", ref: "tooling#widgets/x", wantErr: true},
		{name: "empty name", ref: "tooling#fragments/", wantErr: true},
		// Regression lock: internal/lm/backends/managed.go's
		// gateProfileMCP/gateProfileHooks used to compose the gate ref as
		// "<profile-display-name>#<kind>/<name>". For a bundle-shipped
		// profile the display name is itself "<bundle>#profiles/<name>", so
		// the composed ref carried a SECOND '#' ("<bundle>#profiles/<name>
		// #hooks/<event>/<i>") and trust.ParseItemRef — which cuts at the
		// FIRST '#' — mis-split it into base="<bundle>" and
		// sel="profiles/<name>#hooks/...", which trust.ParseSelector then
		// rejected (kind "profiles" is not a recognized selector directory):
		// a permanent, un-reviewable withhold with no valid trust.Ref to
		// approve. The fix keys the gate off the profile's SOURCE ref
		// (profiles.ResolvedProfile.SourceRef — the origin bundle's
		// canonical ref, WITHOUT the "#profiles/<name>" selector) instead of
		// its display name, so the composed ref below — exactly what
		// gateProfileHooks now produces for a remote bundle-shipped profile
		// — carries exactly one '#' and parses cleanly: reviewable, not
		// dead.
		{
			name:     "bundle-shipped profile hook ref (uncut-grub fixed shape)",
			ref:      "https://github.com/acme/tools@bundles/kit#hooks/pre_tool/0",
			wantRepo: "https://github.com/acme/tools", wantBundle: "kit", wantKind: trust.KindHook, wantName: "pre_tool/0",
		},
		// The MCP twin of the same fixed shape.
		{
			name:     "bundle-shipped profile mcp ref (uncut-grub fixed shape)",
			ref:      "https://github.com/acme/tools@bundles/kit#mcp/server-a",
			wantRepo: "https://github.com/acme/tools", wantBundle: "kit", wantKind: trust.KindMCP, wantName: "server-a",
		},
		// A LOCAL bundle-shipped profile's composed ref (ctxloom:local — a
		// local bundle's own directly-declared hook stays honestly
		// IsLocal:true, not the buggy always-local-via-bare-name shape).
		{
			name:       "local bundle-shipped profile hook ref (uncut-grub fixed shape, local)",
			ref:        "ctxloom:local@bundles/kit#hooks/session_start/0",
			wantBundle: "kit", wantKind: trust.KindHook, wantName: "session_start/0", wantLocal: true,
		},
		// Regression lock: the OLD buggy ref shape for a REMOTE
		// profile's hook was just the bare display name
		// ("my-remote-profile#hooks/pre_tool/0") — trust.ParseItemRef's
		// bare-token fallback resolves that IsLocal:true unconditionally,
		// which is exactly the "gate is a no-op, remote content auto-allowed"
		// bug. This case documents that the bare-token fallback itself is
		// unchanged (it is still correct for a GENUINELY local profile,
		// which the fix continues to key this way — see
		// profileGateRefFor) — the fix is that a REMOTE profile no longer
		// PRODUCES this shape (it produces the canonical-URL shape above
		// instead).
		{
			name:       "bare profile display name still resolves local (correct ONLY for a genuinely local profile)",
			ref:        "my-remote-profile#hooks/pre_tool/0",
			wantBundle: "my-remote-profile", wantKind: trust.KindHook, wantName: "pre_tool/0", wantLocal: true,
		},
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
			tref, _, _, err := trust.ParseItemRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("trust.ParseItemRef(%q): %v", tt.ref, err)
			}
			if tref.RepoURL != tt.wantRepo || tref.Bundle != tt.wantBundle || tref.Kind != tt.wantKind ||
				tref.Name != tt.wantName || tref.IsLocal != tt.wantLocal || tref.IsBuiltin != tt.wantBuiltin {
				t.Errorf("got %+v, want repo=%q bundle=%q kind=%q name=%q local=%v builtin=%v",
					tref, tt.wantRepo, tt.wantBundle, tt.wantKind, tt.wantName, tt.wantLocal, tt.wantBuiltin)
			}
		})
	}
}

// TestEffectiveTrust_CompanionRef_LocalEquivalentButStillReachable pins the
// companion posture, both halves of it.
//
// This test previously asserted the OPPOSITE — that unsigned companion content
// is PENDING, gated "exactly like a remote bundle". That was reversed
// deliberately, and the reason is order-of-operations rather than trust in
// companion authors: ctxloom reads a loadout by EXECUTING the companion binary,
// so by the time this content exists that binary has already run arbitrary code
// as the user. A review prompt afterwards buys ~nothing while costing friction
// on content the user deliberately installed. The decision that DOES have
// purchase moved to exec (config.AdmitCompanions).
//
// What did NOT move is everything below: rejection still reaches it, and an
// unreadable approvals store still denies it.
//
// A companion's SIGNATURE does not enter this decision in either direction — a
// loadout crosses no intermediary, so there is nothing for a publisher
// signature to protect it from, and config.ProbeCompanionLoadouts reports a
// signature that fails to verify (a stale sig in the companion's release)
// rather than withholding. That posture is pinned where it lives:
// config.TestProbeCompanionLoadouts_InvalidSignatureIsReportedNotWithheld.
func TestEffectiveTrust_CompanionRef_LocalEquivalentButStillReachable(t *testing.T) {
	ref := "ctxloom:companion@ltk#fragments/ltk"

	tref, loadRef, _, err := trust.ParseItemRef(ref)
	require.NoError(t, err, "a companion ref MUST be recognized by remote.ParseReference, never fail-closed as unrecognized")
	assert.Equal(t, "ctxloom:companion@ltk", loadRef)
	assert.True(t, tref.IsCompanion, "the companion flag must ride the same parse the ref does")
	assert.False(t, tref.IsLocal, "a companion is its OWN exemption step, never laundered through the local one")
	assert.False(t, tref.IsBuiltin, "a companion loadout is not compiled into this binary")
	assert.Equal(t, remote.CompanionSource, tref.RepoURL)
	assert.Equal(t, "ltk", tref.Bundle)

	payload := pbytes("ltk-fragment-body")

	t.Run("unsigned companion content is ALLOWED as companion — installed is the consent act", func(t *testing.T) {
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: "",
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records: fakeRecords{},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Allow, res.Decision)
		assert.Equal(t, trust.SourceCompanion, res.Source,
			"allowed as COMPANION specifically, so a reader can see which exemption fired")
	})

	t.Run("a signed companion is allowed at the COMPANION step, above trusted-signer", func(t *testing.T) {
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: trustedPublisher,
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records: fakeRecords{},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Allow, res.Decision)
		assert.Equal(t, trust.SourceCompanion, res.Source,
			"the companion exemption sits where builtin's does — above step 5 — so a signature can neither admit nor "+
				"withhold companion content. A loadout's signature only ATTRIBUTES it; the admission decision "+
				"happened at exec (config.AdmitCompanions)")
	})

	t.Run("REJECTION still reaches companion content, ahead of the exemption", func(t *testing.T) {
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: "",
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool {
				return r.RepoURL == remote.CompanionSource && r.Bundle == "ltk"
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Deny, res.Decision)
		assert.Equal(t, trust.SourceRejected, res.Source,
			"a human who rejected a companion item keeps that rejection — step 1 runs above every exemption")
	})

	t.Run("a rejection of the companion ref still wins over a trusted signer", func(t *testing.T) {
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: trustedPublisher,
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records: fakeRecords{rejected: func(r trust.Ref, _ []byte) bool {
				return r.RepoURL == remote.CompanionSource && r.Bundle == "ltk"
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Deny, res.Decision)
		assert.Equal(t, trust.SourceRejected, res.Source, "rejection is supreme even over a trusted-signer companion")
	})

	t.Run("an unreadable approvals store denies companion content too", func(t *testing.T) {
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: "",
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records: unreadableRecords{},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Deny, res.Decision)
		assert.Equal(t, trust.SourcePending, res.Source,
			"the store-fault gate runs above EVERY exemption, this one included")
	})

	t.Run("an unreadable lockfile still withholds companion content", func(t *testing.T) {
		// Deliberate asymmetry with local/builtin, pinned so nobody "tidies"
		// it away: a companion ref carries a RepoURL, so it stays in
		// retractable()'s scope, and step 2a can therefore only make companion
		// content MORE withheld — never less. Relaxing a fail-closed gate is
		// not part of making companion CONTENT local-equivalent.
		assert.True(t, retractable(tref), "a companion ref carries a RepoURL and stays in the step-2a scope")
		res, err := EffectiveTrust(nil, EffectiveTrustRequest{
			Ref: tref, Payload: payload, Form: rawForm, Signer: "",
			Posture: postureCtxOf(tref), Provenance: postureProvOf(tref),
			Records:    fakeRecords{},
			Retraction: &lockfileRetraction{unreadable: assert.AnError, path: "lock.yaml"},
		})
		require.NoError(t, err)
		assert.Equal(t, trust.Deny, res.Decision)
		assert.Equal(t, trust.SourcePending, res.Source)
	})
}

// unreadableRecords is a ReviewRecords that also implements readableRecords and
// reports the store as unreadable — the fault EffectiveTrust's preamble gate
// denies everything on.
type unreadableRecords struct{ fakeRecords }

func (unreadableRecords) readable() error { return assert.AnError }

// TestEffectiveTrust_LocalExemptionSitsBelowRetraction pins the CASCADE
// POSITION of the first-party local exemption, which ForLocalMCP's doc comment
// names by number. The number is not decoration: it is the whole
// content of the claim "a rejection beats this, and so does a retraction".
//
// Nothing observable distinguishes step 2 from step 3 for a local ref through
// the PRODUCTION retraction store, because lockfileRetraction.Retracted is
// scoped by retractable() and a local ref has no lockfile entry by
// construction. So the position is pinned through the seam instead: a
// retraction record that does answer for a local ref must WIN, because the
// exemption is below it. Were the exemption actually step 2 — above retraction,
// as the comment used to say — this would come back allowed-as-local.
func TestEffectiveTrust_LocalExemptionSitsBelowRetraction(t *testing.T) {
	localRef := trust.Ref{Bundle: "project-tools", Kind: trust.KindMCP, Name: "local-server", IsLocal: true}

	assert.False(t, retractable(localRef),
		"a local ref has no remote lockfile entry, so production never asks the retraction store about it — that scoping, not cascade position, is why a local item is never retracted")

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:        localRef,
		Posture:    postureCtxOf(localRef),
		Provenance: postureProvOf(localRef),
		Payload:    []byte("local mcp payload"),
		Form:       rawForm,
		Records:    fakeRecords{},
		Retraction: fakeRetraction{retracted: func(trust.Ref) (bool, string) { return true, "withdrawn" }},
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourceRetracted, res.Source,
		"retraction is evaluated BEFORE the local exemption — the exemption is step 3, not step 2")
}
