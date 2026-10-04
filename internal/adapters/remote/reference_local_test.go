package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReference_Local(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		wantType    ItemType
		wantPath    string
		wantVersion string
	}{
		{"bundle, versionless", "ctxloom:local@bundles/foo", ItemTypeBundle, "foo", ""},
		{"bundle, pinned", "ctxloom:local@bundles/foo@abc123", ItemTypeBundle, "foo", "abc123"},
		{"nested path", "ctxloom:local@bundles/team/standards", ItemTypeBundle, "team/standards", ""},
		// Opaque versions: a name, an hg-style changeset, an svn rev all pass through.
		{"branch-name version", "ctxloom:local@bundles/foo@feature/x", ItemTypeBundle, "foo", "feature/x"},
		{"numeric svn-style version", "ctxloom:local@bundles/foo@1492", ItemTypeBundle, "foo", "1492"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := ParseReference(tt.ref)
			require.NoError(t, err)
			assert.True(t, ref.IsLocal, "IsLocal")
			assert.False(t, ref.IsCanonical(), "local is not URL-canonical")
			assert.Empty(t, ref.URL)
			assert.Equal(t, tt.wantType, ref.ItemType)
			assert.Equal(t, tt.wantPath, ref.Path)
			assert.Equal(t, tt.wantVersion, ref.ContentVersion)
		})
	}
}

func TestParseReference_Local_Errors(t *testing.T) {
	for _, ref := range []string{
		"ctxloom:local@",            // no item
		"ctxloom:local@bundles",     // type without path
		"ctxloom:local@widgets/foo", // unknown item type
	} {
		t.Run(ref, func(t *testing.T) {
			_, err := ParseReference(ref)
			assert.Error(t, err)
		})
	}
}

func TestReference_Local_StringRoundTrip(t *testing.T) {
	for ref, want := range map[string]string{
		"ctxloom:local@bundles/foo":               "ctxloom+local:foo",
		"ctxloom:local@bundles/foo@abc123":        "ctxloom+local:foo@abc123",
		"ctxloom:local@bundles/team/standards@v2": "ctxloom+local:team/standards@v2",
	} {
		t.Run(ref, func(t *testing.T) {
			parsed, err := ParseReference(ref)
			require.NoError(t, err)
			assert.Equal(t, want, parsed.String(), "String renders the canonical URI")

			// The rendered identity parses back to the same reference, which is
			// what makes the two spellings one identity rather than two keys.
			round, err := ParseReference(want)
			require.NoError(t, err)
			assert.Equal(t, parsed, round)
		})
	}
}

func TestReference_Local_BuildFilePath(t *testing.T) {
	// No redundant ctxloom/ segment — local content sits under .ctxloom/content/.
	bundle, err := ParseReference("ctxloom:local@bundles/foo")
	require.NoError(t, err)
	assert.Equal(t, "bundles/v2/foo", bundle.BuildFilePath(ItemTypeBundle))
}

func TestParseReference_NonLocalUnaffected(t *testing.T) {
	// A canonical URL must not be misread as local.
	canonical, err := ParseReference("https://github.com/owner/repo@bundles/core")
	require.NoError(t, err)
	assert.False(t, canonical.IsLocal)

	// The short "repo/path" form is rejected outright (no longer a ref at all).
	_, err = ParseReference("alice/security")
	assert.Error(t, err)
}
