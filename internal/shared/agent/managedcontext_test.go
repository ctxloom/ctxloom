package agent

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStripManagedSection_UnterminatedBeginTrimsBlankPrefix pins the
// unterminated-begin-marker branch: content before an unterminated begin
// marker that is ENTIRELY blank lines must be dropped, not returned as a
// stray "\n". The prior ifNonEmptySuffix checked the UNTRIMMED prefix
// for emptiness while the trimmed value was what actually got returned — for
// "\n\n<begin>...", the trim yields "" but the guard saw "\n\n" (non-empty)
// and appended "\n", so StripManagedSection returned "\n" instead of "".
func TestStripManagedSection_UnterminatedBeginTrimsBlankPrefix(t *testing.T) {
	content := "\n\n" + ManagedContextBegin + "\nunterminated body, no end marker"

	got := StripManagedSection(content)

	assert.Equal(t, "", got, "an all-blank-lines prefix before an unterminated begin marker must not survive as a stray newline")
}

// TestStripManagedSection_UnterminatedBeginPreservesRealPrefix is the sibling
// case: real (non-blank) content before an unterminated begin marker must
// survive, trimmed of trailing blank lines and terminated with exactly one
// newline.
func TestStripManagedSection_UnterminatedBeginPreservesRealPrefix(t *testing.T) {
	content := "real user content\n\n\n" + ManagedContextBegin + "\nunterminated body, no end marker"

	got := StripManagedSection(content)

	assert.Equal(t, "real user content\n", got)
}

// TestWriteManagedContext_PreservesPositionOfTrailingUserContent pins the
// fix: WriteManagedContext documents "content OUTSIDE [the markers] is
// the user's and is preserved byte-for-byte", but content that came AFTER
// the end marker used to be hoisted ABOVE the (re-appended) managed section
// on every rewrite, because the merge always appended the new section at the
// very end regardless of where the old one had been.
func TestWriteManagedContext_PreservesPositionOfTrailingUserContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/proj/CLAUDE.md"
	existing := "A\n" + ManagedContextBegin + "\nold\n" + ManagedContextEnd + "\nB\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(existing), 0644))

	_, err := WriteManagedContext(fs, path, "CLAUDE.md", "new", "CLAUDE.md")
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	want := "A\n" + ManagedContextBegin + "\nnew\n" + ManagedContextEnd + "\nB\n"
	assert.Equal(t, want, string(data), "user content after the end marker must stay after the managed section, not get hoisted above it")
}
