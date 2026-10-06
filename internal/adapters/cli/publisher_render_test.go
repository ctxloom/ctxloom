package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exploit body from the delicious-goatskin report: cursor-up plus
// erase-line, then publisher-controlled replacement text. Every path below is
// asserted against the BYTES that reach the writer, because the failure this
// test exists to catch is an assertion that merely "contains" the safe text
// while the live sequence survives next to it.
const exploitBody = "SAFE-LINE-ONE\nAFTER\x1b[1A\x1b[2KOVERWRITTEN-BY-PUBLISHER"

// `fragment show` / `command show` share one body. It is the path the report
// CONFIRMED exploitable, so it is asserted byte-for-byte too.
func TestPrintItemBody_ShowRendersPublisherContentInert(t *testing.T) {
	var out bytes.Buffer

	printItemBody(&out, "probe#fragments/example", "example\x1b[2K", exploitBody, false)

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "example⟨ESC⟩[2K\n\n")
	assert.True(t, strings.HasSuffix(got, "AFTER⟨ESC⟩[1A⟨ESC⟩[2KOVERWRITTEN-BY-PUBLISHER\n"))
}

func TestPrintItemBody_DistilledMarkerStillPrints(t *testing.T) {
	var out bytes.Buffer

	printItemBody(&out, "probe#fragments/example", "example", "body\n", true)

	assert.Equal(t, "# (distilled version)\nexample\n\nbody\n", out.String())
}

// `bundle view <ref>#fragments/x` renders one item's body.
func TestWriteBundleViewText_ItemBodyIsInert(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeBundleViewText(&out, "probe#fragments/x", "fragments/x", []byte(exploitBody)))

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, "SAFE-LINE-ONE\nAFTER⟨ESC⟩[1A⟨ESC⟩[2KOVERWRITTEN-BY-PUBLISHER\n", got)
}

// `bundle view <bundle>` with no selector dumps the whole bundle DOCUMENT, and
// people redirect that to a file. It is still escaped — a terminal is still on
// the other end — but its blank lines are NOT collapsed, because handing back a
// document that is not the one on disk is a different bug from the one being
// fixed.
func TestWriteBundleViewText_WholeDocumentIsEscapedButNotCollapsed(t *testing.T) {
	var out bytes.Buffer
	doc := "name: probe\n\n\n\n\nfragments:\n  x: \x1b[2K\n"

	require.NoError(t, writeBundleViewText(&out, "probe", "", []byte(doc)))

	got := out.String()
	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, "name: probe\n\n\n\n\nfragments:\n  x: ⟨ESC⟩[2K\n", got)
}
