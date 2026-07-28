package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStripManagedSection_UnterminatedBeginTrimsBlankPrefix pins the
// unterminated-begin-marker branch: content before an unterminated begin
// marker that is ENTIRELY blank lines must be dropped, not returned as a
// stray "\n". U101-F17 found ifNonEmptySuffix checking the UNTRIMMED prefix
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
