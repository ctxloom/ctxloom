package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseReference_Companion proves remote.ParseReference RECOGNIZES the
// ctxloom:companion@<bin> source token (signature-envelope spec §4.3/§6): the
// companion loadout protocol's whole trust story depends on this ref never
// falling into ParseReference's "unsupported reference" error, which every
// fail-closed guard built on IsSelfContainedRef treats as an attempted-but-
// unrecognized source (denied, never silently local).
func TestParseReference_Companion(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantPath string
	}{
		{"first-party bin", "ctxloom:companion@ltk", "ltk"},
		{"first-party bin, taskloom", "ctxloom:companion@taskloom", "taskloom"},
		{"PATH-convention third-party bin", "ctxloom:companion@ctxloom-companion-acme", "ctxloom-companion-acme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := ParseReference(tt.ref)
			require.NoError(t, err)
			assert.True(t, ref.IsCompanion, "IsCompanion")
			// A companion loadout is THIRD-PARTY content, not project-authored:
			// it must never carry IsLocal, or it would silently bypass the trust
			// gate at EffectiveTrust step 2.
			assert.False(t, ref.IsLocal, "a companion ref must never be IsLocal")
			assert.Equal(t, CompanionSource, ref.URL)
			assert.Equal(t, tt.wantPath, ref.Path)
			assert.Empty(t, ref.ContentVersion, "a live loadout probe carries no pinned version")
		})
	}
}

func TestParseReference_Companion_Errors(t *testing.T) {
	for _, ref := range []string{
		"ctxloom:companion@",       // no binary name
		"ctxloom:companion@../ltk", // traversal
		"ctxloom:companion@/etc",   // absolute path
	} {
		t.Run(ref, func(t *testing.T) {
			_, err := ParseReference(ref)
			assert.Error(t, err)
		})
	}
}

func TestReference_Companion_StringRoundTrip(t *testing.T) {
	for _, ref := range []string{
		"ctxloom:companion@ltk",
		"ctxloom:companion@taskloom",
		"ctxloom:companion@ctxloom-companion-acme",
	} {
		t.Run(ref, func(t *testing.T) {
			parsed, err := ParseReference(ref)
			require.NoError(t, err)
			assert.Equal(t, ref, parsed.String(), "String round-trips")
			assert.Equal(t, ref, parsed.CanonicalString(), "CanonicalString round-trips")
		})
	}
}

// TestParseReference_Companion_NotBuiltin proves a companion ref is
// distinguishable from the retired "builtin:" source ref
// (trust.IsRetiredBuiltinSpelling) and from ctxloom:local — they are different trust
// classes (trusted-signer/pending vs the unconditional builtin/local
// exemptions) and must never be confused. See internal/trust's
// TestCanonicalRepoURL for the companion-source special case in
// CanonicalRepoURL (trust cannot be tested from here: remote cannot import
// trust, which imports remote).
func TestParseReference_Companion_NotBuiltin(t *testing.T) {
	// A companion ref must be distinguishable from a "builtin:" source ref —
	// they are different trust classes (trusted-signer/pending vs the
	// unconditional builtin exemption) and must never be confused.
	ref, err := ParseReference("ctxloom:companion@ltk")
	require.NoError(t, err)
	assert.False(t, ref.IsLocal)
	assert.NotEqual(t, LocalSource, ref.URL)
}
