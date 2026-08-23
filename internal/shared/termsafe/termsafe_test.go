package termsafe

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exploit from the delicious-goatskin report, asserted as the exact bytes
// that reach the writer. A publisher body carrying cursor-up + erase-line must
// come out with the ESC byte rendered inert -- not "containing" the safe text
// while the live sequence survives alongside it.
func TestSanitize_CursorMovementAndLineClearingBecomeInert(t *testing.T) {
	const exploit = "SAFE-LINE-ONE\nAFTER\x1b[1A\x1b[2KOVERWRITTEN-BY-PUBLISHER"

	got := Sanitize(exploit, DefaultMaxBytes, true)

	assert.Equal(t, "SAFE-LINE-ONE\nAFTER^[[1A^[[2KOVERWRITTEN-BY-PUBLISHER", got.Text)
	assert.Equal(t, 2, got.Escaped)
	assert.True(t, got.Altered())
	assert.NotContains(t, got.Text, "\x1b", "no ESC byte may survive")
}

// A carriage return overwrites the line it is on, so it forges just as well as
// a CSI sequence and must not survive either.
func TestSanitize_CarriageReturnCannotOverwrite(t *testing.T) {
	got := Sanitize("signer: alice\rsigner: mallory", DefaultMaxBytes, true)

	assert.Equal(t, "signer: alice^Msigner: mallory", got.Text)
	assert.Equal(t, 1, got.Escaped)
	assert.NotContains(t, got.Text, "\r")
}

// Newlines and tabs are the two control characters a prose body legitimately
// contains, so they survive; every other C0 byte, DEL, and the C1 block
// (U+009B IS a single-byte CSI on a real terminal) are escaped visibly rather
// than deleted, because a body that silently loses content is the failure mode
// this project keeps rediscovering.
func TestSanitize_KeepsNewlineAndTabEscapesEveryOtherControl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"newline kept", "a\nb", "a\nb"},
		{"tab kept", "a\tb", "a\tb"},
		{"NUL", "a\x00b", "a^@b"},
		{"BEL", "a\ab", "a^Gb"},
		{"backspace", "a\bb", "a^Hb"},
		{"vertical tab", "a\vb", "a^Kb"},
		{"DEL", "a\x7fb", "a^?b"},
		{"C1 CSI", "a\u009bb", `a\u009bb`},
		{"C1 NEL", "a\u0085b", `a\u0085b`},
		{"bidi override", "a\u202eb", `a\u202eb`},
		{"bidi isolate", "a\u2066b", `a\u2066b`},
		{"RLM", "a\u200fb", `a\u200fb`},
		{"invalid utf8 byte", "a\xffb", `a\xffb`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Sanitize(tc.in, DefaultMaxBytes, true).Text)
		})
	}
}

// A zero-width joiner is load-bearing in real text (emoji sequences, Indic
// scripts), so it is NOT escaped -- the line between "invisible" and "forging"
// is drawn at the bidi controls, which reorder what a reviewer reads.
func TestSanitize_LeavesLegitimateInvisiblesAlone(t *testing.T) {
	const zwj = "a\u200db"
	got := Sanitize(zwj, DefaultMaxBytes, true)
	assert.Equal(t, zwj, got.Text)
	assert.False(t, got.Altered())
}

// Collapsing bounds how far a publisher can push the surrounding warning up the
// screen with blank lines. One blank line survives, so paragraph structure is
// preserved; the count is reported so the collapse is never silent.
func TestSanitize_CollapsesBlankLineRuns(t *testing.T) {
	got := Sanitize("a\n\n\n\n\nb", DefaultMaxBytes, true)
	assert.Equal(t, "a\n\nb", got.Text)
	assert.Equal(t, 3, got.BlankLines)
	assert.True(t, got.Altered())

	off := Sanitize("a\n\n\n\n\nb", DefaultMaxBytes, false)
	assert.Equal(t, "a\n\n\n\n\nb", off.Text)
	assert.Equal(t, 0, off.BlankLines)
	assert.False(t, off.Altered())
}

// An over-long body is capped, and the ORIGINAL length is carried out so the
// caller can say so. Truncation that does not announce itself is the silent
// no-op this codebase keeps rediscovering.
func TestSanitize_CapsOverLongBodyAndReportsOriginalLength(t *testing.T) {
	body := strings.Repeat("x", 1000)

	got := Sanitize(body, 100, true)

	assert.Equal(t, strings.Repeat("x", 100), got.Text)
	assert.Equal(t, 1000, got.TruncatedFrom)
	assert.True(t, got.Altered())

	under := Sanitize("short", 100, true)
	assert.Equal(t, 0, under.TruncatedFrom, "a body inside the cap is not truncated")
	assert.False(t, under.Altered())
}

// The cap is applied AFTER escaping, so an escape sequence cannot be cut in
// half into something that is neither the original bytes nor inert, and the cap
// bounds what actually reaches the terminal.
func TestSanitize_CapsTheEscapedLengthNotTheRawLength(t *testing.T) {
	got := Sanitize(strings.Repeat("\x1b", 10), 10, true)
	assert.Equal(t, "^[^[^[^[^[", got.Text)
	assert.Equal(t, 20, got.TruncatedFrom)
}

// A maxBytes <= 0 means "use the default", never "cap at zero" -- a zero-value
// Renderer field must not erase the body.
func TestSanitize_NonPositiveCapMeansDefault(t *testing.T) {
	body := strings.Repeat("y", 1024)
	assert.Equal(t, body, Sanitize(body, 0, true).Text)
	assert.Equal(t, body, Sanitize(body, -1, true).Text)
}

// Field is the identifier form: a bundle ref, an item name, a remote, a signer
// principal. These sit INSIDE a line the reader is meant to trust, so a newline
// or a tab would break the table just as effectively as a CSI sequence and is
// escaped too.
func TestField_EscapesEverythingIncludingNewlineAndTab(t *testing.T) {
	cases := []struct{ in, want string }{
		{"probe#fragments/example", "probe#fragments/example"},
		{"probe\nsigner: alice - a key you trust", "probe^Jsigner: alice - a key you trust"},
		{"probe\ttab", "probe^Itab"},
		{"probe\x1b[1A\x1b[2K", "probe^[[1A^[[2K"},
		{"probe\rsigner: mallory", "probe^Msigner: mallory"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, Field(tc.in), "Field(%q)", tc.in)
	}
}

// A publisher who controls an item NAME controls a field, so the field cap is
// what stops one name from being a screenful.
func TestField_CapsWithAVisibleMarker(t *testing.T) {
	got := Field(strings.Repeat("n", DefaultFieldMaxBytes*2))

	assert.LessOrEqual(t, len(got), DefaultFieldMaxBytes)
	assert.True(t, strings.HasSuffix(got, "..."), "the cut is marked, not silent: %q", got)
}

// The notice names the ref, says what was done, and points at the raw bytes.
// The ref is itself run through Field, because it is publisher-authored too:
// a notice that could be overwritten is worse than no notice.
func TestNotice_NamesRefAndEveryAlterationAndIsItselfInert(t *testing.T) {
	r := Sanitize("a\x1b[2K\n\n\n\nb", 6, true)
	require.True(t, r.Altered())

	got := Notice("probe\x1b[1A#fragments/x", r)

	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "probe^[[1A#fragments/x")
	assert.Contains(t, got, "control character")
	assert.Contains(t, got, "blank line")
	assert.Contains(t, got, "truncated")
	assert.Contains(t, got, "--format json")
	assert.NotContains(t, got, "\n", "the notice is one line")

	assert.Empty(t, Notice("probe#fragments/x", Sanitize("clean", DefaultMaxBytes, true)),
		"unaltered content gets no notice")
}

// Render is the seam the display paths adopt: sanitised body to the content
// writer, the alteration notice to the SEPARATE notice writer so a redirected
// `> file` never grows a diagnostic line it did not ask for.
func TestRenderer_BodyToContentWriterNoticeToNoticeWriter(t *testing.T) {
	var body, notices bytes.Buffer
	rn := Renderer{Indent: "  ", Empty: "(empty)", CollapseBlankLines: true, Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "one\nAFTER\x1b[1A\x1b[2Kgone"))

	assert.Equal(t, "  one\n  AFTER^[[1A^[[2Kgone\n", body.String())
	assert.NotContains(t, body.String(), "\x1b")
	assert.Contains(t, notices.String(), "probe#fragments/x")
	assert.True(t, strings.HasSuffix(notices.String(), "\n"))
}

func TestRenderer_CleanContentEmitsNoNotice(t *testing.T) {
	var body, notices bytes.Buffer
	rn := Renderer{Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "plain\n"))

	assert.Equal(t, "plain\n", body.String())
	assert.Empty(t, notices.String())
}

func TestRenderer_EmptyBodyUsesThePlaceholder(t *testing.T) {
	var body, notices bytes.Buffer
	rn := Renderer{Indent: "  ", Empty: "(empty)", Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "\n\n"))

	assert.Equal(t, "  (empty)\n", body.String())
}

func TestRenderer_NoPlaceholderStillEndsTheBlock(t *testing.T) {
	var body, notices bytes.Buffer
	rn := Renderer{Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", ""))

	assert.Equal(t, "\n", body.String())
}

// A write failure on the content stream is returned, not swallowed: half a
// rendered body with a zero exit is the same silent success this project keeps
// finding.
func TestRenderer_PropagatesWriteError(t *testing.T) {
	rn := Renderer{Notices: new(bytes.Buffer)}
	assert.Error(t, rn.Render(failWriter{}, "probe#fragments/x", "body"))
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, assert.AnError }
