package remote

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/refuri"
)

// TestParseReference_CanonicalURIFamilyParses is the reproduction, inverted.
//
// Before the canonical arm existed, every one of these refs fell through to
// ParseReference's "unsupported reference" arm — and the fall-through was
// SILENT, because CanonicalBundleRef's fallback handed the whole URI to
// LocalBundleRef and minted "ctxloom:local@bundles/<the entire ref>". Nothing
// errored, nothing warned; the identity simply stopped matching anything, and
// the miss surfaced one layer up as "parent not installed", an install message
// for a grammar fault.
func TestParseReference_CanonicalURIFamilyParses(t *testing.T) {
	tests := []struct {
		ref            string
		wantURL        string
		wantPath       string
		wantVersion    string
		wantLocal      bool
		wantCompanion  bool
		wantCanonical  string
		wantSameAsOld  string
		itemTypeBundle bool
	}{
		{
			ref:           "ctxloom+git://github.com/acme/repo//bundles/tooling",
			wantURL:       "https://github.com/acme/repo",
			wantPath:      "tooling",
			wantCanonical: "ctxloom+git://github.com/acme/repo//bundles/tooling",
			wantSameAsOld: "https://github.com/acme/repo@bundles/tooling",
		},
		{
			ref:           "ctxloom+git://github.com/acme/repo//bundles/lang/go@v1.2.3",
			wantURL:       "https://github.com/acme/repo",
			wantPath:      "lang/go",
			wantVersion:   "v1.2.3",
			wantCanonical: "ctxloom+git://github.com/acme/repo//bundles/lang/go@v1.2.3",
			wantSameAsOld: "https://github.com/acme/repo@bundles/lang/go@v1.2.3",
		},
		{
			// The item selector addresses an item WITHIN the bundle and is not
			// part of the bundle's identity, exactly as in the pre-canonical
			// grammar.
			ref:           "ctxloom+git://github.com/acme/repo//bundles/tooling#fragments/x",
			wantURL:       "https://github.com/acme/repo",
			wantPath:      "tooling",
			wantCanonical: "ctxloom+git://github.com/acme/repo//bundles/tooling",
			wantSameAsOld: "https://github.com/acme/repo@bundles/tooling#fragments/x",
		},
		{
			ref:           "ctxloom+file:///srv/content//bundles/tooling",
			wantURL:       "file:///srv/content",
			wantPath:      "tooling",
			wantCanonical: "ctxloom+file:///srv/content//bundles/tooling",
			wantSameAsOld: "file:///srv/content@bundles/tooling",
		},
		{
			ref:           "ctxloom+local:my-tools",
			wantPath:      "my-tools",
			wantLocal:     true,
			wantCanonical: "ctxloom+local:my-tools",
			wantSameAsOld: "ctxloom:local@bundles/my-tools",
		},
		{
			ref:           "ctxloom+companion:ltk",
			wantURL:       CompanionSource,
			wantPath:      "ltk",
			wantCompanion: true,
			wantCanonical: "ctxloom+companion:ltk",
			wantSameAsOld: "ctxloom:companion@ltk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, err := ParseReference(tt.ref)
			require.NoError(t, err, "the canonical URI family must parse")
			assert.Equal(t, tt.wantURL, got.URL)
			assert.Equal(t, tt.wantPath, got.Path)
			assert.Equal(t, tt.wantVersion, got.ContentVersion)
			assert.Equal(t, tt.wantLocal, got.IsLocal)
			assert.Equal(t, tt.wantCompanion, got.IsCompanion)
			assert.Equal(t, ItemTypeBundle, got.ItemType)

			// The identity is the canonical URI, asserted as a literal: it is
			// the string a grant is keyed on.
			assert.Equal(t, tt.wantCanonical, got.CanonicalString(),
				"a reference's identity is its canonical URI")

			// A canonical URI and its PRE-CANONICAL spelling are one identity:
			// both are accepted as input and both resolve to the same URI, so
			// an authored ref can be written either way without becoming a
			// second trust key.
			old, err := ParseReference(tt.wantSameAsOld)
			require.NoError(t, err)
			assert.Equal(t, old.CanonicalString(), got.CanonicalString(),
				"the two spellings must be one identity")
		})
	}
}

// TestParseReference_BuiltinURIIsRefusedNotDowngraded pins the one class with
// no Reference. Refusing is the whole point: ClassLocal is the only near-fit,
// and mapping onto it would hand an embedded bundle the first-party local
// auto-allow under a name the project chose.
func TestParseReference_BuiltinURIIsRefusedNotDowngraded(t *testing.T) {
	got, err := ParseReference("ctxloom+builtin:ltk")
	require.Error(t, err, "a builtin URI has no source to fetch and must not parse into one")
	assert.Nil(t, got)
	assert.False(t, strings.Contains(err.Error(), "unsupported reference"),
		"the refusal must name the reason, not fall through to the catch-all arm")
}

// TestIsSelfContainedRef_KnowsEveryCanonicalClass is the defect-2 pin.
//
// The list matched only the "://" classes, by accident of shape: three of the
// five canonical classes are OPAQUE URIs, so "ctxloom+builtin:x" and
// "ctxloom+local:x" answered FALSE — read as bare local bundle names, which is
// exactly the first-party exemption a guard must not grant by accident. The
// wrong direction for a guard to fail.
func TestIsSelfContainedRef_KnowsEveryCanonicalClass(t *testing.T) {
	for _, class := range refuri.Classes() {
		ref := refuri.SchemePrefix + string(class) + ":name"
		switch class {
		case refuri.ClassGit:
			ref = refuri.SchemePrefix + string(class) + "://host/repo//bundles/name"
		case refuri.ClassFile:
			ref = refuri.SchemePrefix + string(class) + ":///repo//bundles/name"
		}
		assert.True(t, IsSelfContainedRef(ref),
			"class %q is scheme-qualified and must never be read as a bare bundle name", class)
	}

	// The predecessor spellings and a bare name keep their answers.
	assert.True(t, IsSelfContainedRef("ctxloom:local@bundles/dev"))
	assert.True(t, IsSelfContainedRef("ctxloom:companion@ltk"))
	assert.True(t, IsSelfContainedRef("https://github.com/acme/repo@bundles/dev"))
	assert.True(t, IsSelfContainedRef("git@github.com:acme/repo@bundles/dev"))
	assert.False(t, IsSelfContainedRef("dev"))
	assert.False(t, IsSelfContainedRef("lang/go"))
}

// TestCanonicalBundleRef_UnparseableSourceErrorsRatherThanBecomingLocal is the
// defect-1 pin, and the half that made the whole class of fault invisible.
//
// The old fallback was unconditional: anything CanonicalKey could not parse
// became LocalBundleRef(name), so a canonical URI, a malformed https ref and
// an "ssh://" ref all silently minted a local identity naming the entire ref
// — a syntactically valid identity for content that does not exist. The
// assertion below is deliberately on the RETURNED STRING as well as the error:
// an error paired with the mangled local ref would still let a caller that
// ignores errors carry the forged identity forward.
func TestCanonicalBundleRef_UnparseableSourceErrorsRatherThanBecomingLocal(t *testing.T) {
	for _, ref := range []string{
		"ctxloom+git://github.com/acme/repo/bundles/no-separator",
		"ctxloom+git:///repo//bundles/no-host",
		"ctxloom+builtin:ltk",
		"ctxloom+registry:unknown-class",
		"ssh://git.example.com/acme/repo",
		"https://github.com/acme/repo",
		"ctxloom:local@",
	} {
		t.Run(ref, func(t *testing.T) {
			got, err := CanonicalBundleRef(ref)
			require.Error(t, err, "a ref that names a source and does not parse must fail loudly")
			assert.Empty(t, got)
			assert.NotEqual(t, LocalBundleRef(ref), got,
				"an unparseable source must never be minted as a local bundle name")
		})
	}

	// A BARE NAME is still the local fallback's whole purpose and must keep
	// working: this function is on the path of every plain bundle ask.
	for name, want := range map[string]string{
		"dev":                          "ctxloom+local:dev",
		"team/dev":                     "ctxloom+local:team/dev",
		"ctxloom:local@bundles/dev":    "ctxloom+local:dev",
		"ctxloom+local:dev":            "ctxloom+local:dev",
		"ctxloom+git://h/r//bundles/x": "ctxloom+git://h/r//bundles/x",
	} {
		got, err := CanonicalBundleRef(name)
		require.NoError(t, err, "CanonicalBundleRef(%q)", name)
		assert.Equal(t, want, got)
	}
}

// TestCanonicalProfileKey_FalseOnUnparseableBundlePart pins the second half of
// the same silence. CanonicalProfileKey reported ok=TRUE on a key built from a
// bundle part it could not parse, so the corruption arrived at the profile
// loader labelled a SUCCESS — and the loader, finding nothing under it,
// reported the profile as not installed.
func TestCanonicalProfileKey_FalseOnUnparseableBundlePart(t *testing.T) {
	for _, ref := range []string{
		"ctxloom+git://github.com/acme/repo/bundles/no-separator#profiles/dev",
		"ctxloom+registry:unknown-class#profiles/dev",
		"ssh://git.example.com/acme/repo#profiles/dev",
	} {
		key, ok := CanonicalProfileKey(ref)
		assert.False(t, ok, "CanonicalProfileKey(%q) must not report success", ref)
		assert.Empty(t, key)
	}

	// The canonical spelling and its predecessor produce ONE key — the property
	// that makes a migrated profile ref find its seed entry.
	canonical, ok := CanonicalProfileKey(
		"ctxloom+git://github.com/acme/repo//bundles/agent-ensemble#profiles/coordinator")
	require.True(t, ok)
	predecessor, ok := CanonicalProfileKey(
		"https://github.com/acme/repo@bundles/agent-ensemble#profiles/coordinator")
	require.True(t, ok)
	assert.Equal(t, predecessor, canonical,
		"a migrated profile ref must key to the same seed entry as the ref it replaced")
	assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/agent-ensemble#profiles/coordinator", canonical)
}

// TestParseReference_CanonicalURIRejectsTraversal pins containment on the new
// arm. The bundle path is joined under a repository root and, for
// filesystem-backed sources, under a directory root, so a "." or ".." segment
// reaching a read path is an escape. refuri RESOLVES dot segments rather than
// refusing them — a URI path legitimately carries them — which is exactly why
// the containment rule has to be re-asserted here and cannot be assumed from
// the syntax layer.
func TestParseReference_CanonicalURIRejectsTraversal(t *testing.T) {
	for _, ref := range []string{
		"ctxloom+local:..",
		"ctxloom+companion:..",
		"ctxloom+local:/abs",
	} {
		got, err := ParseReference(ref)
		require.Error(t, err, "%q escapes its root and must not parse", ref)
		assert.Nil(t, got)
	}
}

// TestCanonicalString_AndLockKey_AreTwoDifferentAddresses pins BOTH strings a
// Reference renders, as literals, for one bundle of each class it can be.
//
// They are asserted together because the whole point is that they DIFFER:
// CanonicalString is the identity every API and stored identity carries, and
// LockKey is the fetch address a lockfile entry is keyed on. A change that
// collapsed one into the other would either freeze the identity in the
// pre-canonical spelling or rewrite every lockfile key on disk, and only an
// assertion over both catches that.
//
// There is no builtin row: a builtin bundle has no source to fetch from, so no
// Reference can be builtin (see TestParseReference_BuiltinURIIsRefusedNotDowngraded).
func TestCanonicalString_AndLockKey_AreTwoDifferentAddresses(t *testing.T) {
	cases := []struct {
		name         string
		ref          Reference
		wantIdentity string
		wantLockKey  string
	}{
		{
			name:         "git",
			ref:          Reference{URL: "https://github.com/acme/repo", ItemType: ItemTypeBundle, Path: "tooling"},
			wantIdentity: "ctxloom+git://github.com/acme/repo//bundles/tooling",
			wantLockKey:  "https://github.com/acme/repo@bundles/tooling",
		},
		{
			name:         "git, version-pinned",
			ref:          Reference{URL: "https://github.com/acme/repo", ItemType: ItemTypeBundle, Path: "lang/go", ContentVersion: "v1.2.3"},
			wantIdentity: "ctxloom+git://github.com/acme/repo//bundles/lang/go@v1.2.3",
			wantLockKey:  "https://github.com/acme/repo@bundles/lang/go",
		},
		{
			name:         "file",
			ref:          Reference{URL: "file:///srv/content", ItemType: ItemTypeBundle, Path: "tooling"},
			wantIdentity: "ctxloom+file:///srv/content//bundles/tooling",
			wantLockKey:  "file:///srv/content@bundles/tooling",
		},
		{
			name:         "local",
			ref:          Reference{IsLocal: true, ItemType: ItemTypeBundle, Path: "my-tools"},
			wantIdentity: "ctxloom+local:my-tools",
			wantLockKey:  "ctxloom:local@bundles/my-tools",
		},
		{
			name:         "companion",
			ref:          Reference{URL: CompanionSource, IsCompanion: true, ItemType: ItemTypeBundle, Path: "ltk"},
			wantIdentity: "ctxloom+companion:ltk",
			wantLockKey:  "ctxloom:companion@ltk",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := tc.ref
			assert.Equal(t, tc.wantIdentity, ref.CanonicalString(), "identity")
			assert.Equal(t, tc.wantLockKey, ref.LockKey(), "fetch address")
			assert.NotEqual(t, ref.CanonicalString(), ref.LockKey(),
				"the identity and the fetch address must not collapse into one string")

			// Both spellings address the same bundle: each parses back to a
			// reference whose version-less identity is the same string. The
			// version is dropped on both sides because the fetch address never
			// carries one — an identity that moved with every commit would key
			// every grant to a single revision.
			fromIdentity, err := ParseReference(ref.CanonicalString())
			require.NoError(t, err)
			fromLockKey, err := ParseReference(ref.LockKey())
			require.NoError(t, err)
			fromIdentity.ContentVersion, fromLockKey.ContentVersion = "", ""
			assert.Equal(t, fromIdentity.CanonicalString(), fromLockKey.CanonicalString())
		})
	}
}
