package cli

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
func TestInertBody_CursorMovementAndLineClearingBecomeInert(t *testing.T) {
	const exploit = "SAFE-LINE-ONE\nAFTER\x1b[1A\x1b[2KOVERWRITTEN-BY-PUBLISHER"

	got := inertBody(exploit, publisherBodyMaxBytes, true)

	assert.Equal(t, "SAFE-LINE-ONE\nAFTER⟨ESC⟩[1A⟨ESC⟩[2KOVERWRITTEN-BY-PUBLISHER", got.Text)
	assert.Equal(t, 2, got.Escaped)
	assert.True(t, got.Altered())
	assert.NotContains(t, got.Text, "\x1b", "no ESC byte may survive")
}

// A carriage return overwrites the line it is on, so it forges just as well as
// a CSI sequence and must not survive either.
func TestInertBody_CarriageReturnCannotOverwrite(t *testing.T) {
	got := inertBody("signer: alice\rsigner: mallory", publisherBodyMaxBytes, true)

	assert.Equal(t, "signer: alice⟨U+000D⟩signer: mallory", got.Text)
	assert.Equal(t, 1, got.Escaped)
	assert.NotContains(t, got.Text, "\r")
}

// A newline is the one control character a prose body lays out by, so it
// survives (a tab is expanded); every other C0 byte, DEL, the C1 block
// (U+009B IS a single-byte CSI on a real terminal), and every format
// character — bidi and zero-width alike — are shown as markers rather than
// deleted, because a body that silently loses content is the failure mode
// this project keeps rediscovering. The count is the markers displaysafe.Text
// added; one the publisher typed is not counted.
func TestInertBody_KeepsNewlinesEscapesEveryOtherControl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		// wantEscaped pins the COUNT beside the text: a rendering that looks
		// right while reporting nothing escaped leaves Altered() false, and a
		// caller that gates its warning on Altered() then stays silent about a
		// body it did in fact rewrite.
		wantEscaped int
	}{
		{"newline kept", "a\nb", "a\nb", 0},
		{"tab expands", "a\tb", "a    b", 0},
		{"NUL", "a\x00b", "a⟨U+0000⟩b", 1},
		{"BEL", "a\ab", "a⟨U+0007⟩b", 1},
		{"backspace", "a\bb", "a⟨U+0008⟩b", 1},
		{"vertical tab", "a\vb", "a⟨U+000B⟩b", 1},
		{"DEL", "a\x7fb", "a⟨U+007F⟩b", 1},
		{"C1 CSI", "a\u009bb", "a⟨U+009B⟩b", 1},
		{"C1 NEL", "a\u0085b", "a⟨U+0085⟩b", 1},
		{"bidi override", "a\u202eb", "a⟨U+202E⟩b", 1},
		{"bidi isolate", "a\u2066b", "a⟨U+2066⟩b", 1},
		{"RLM", "a\u200fb", "a⟨U+200F⟩b", 1},
		{"zero-width space", "a\u200bb", "a⟨U+200B⟩b", 1},
		{"zero-width joiner", "a\u200db", "a⟨U+200D⟩b", 1},
		{"invalid utf8 byte", "a\xffb", "a⟨0xFF⟩b", 1},
		{"a typed marker is not counted", "a⟨U+0041⟩b", "a⟨U+0041⟩b", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inertBody(tc.in, publisherBodyMaxBytes, true)
			assert.Equal(t, tc.want, got.Text)
			assert.Equal(t, tc.wantEscaped, got.Escaped)
		})
	}
}

// Collapsing bounds how far a publisher can push the surrounding warning up the
// screen with blank lines. One blank line survives, so paragraph structure is
// preserved; the count is reported so the collapse is never silent.
func TestInertBody_CollapsesBlankLineRuns(t *testing.T) {
	got := inertBody("a\n\n\n\n\n\n\nb", publisherBodyMaxBytes, true)
	assert.Equal(t, "a\n\n\n\nb", got.Text)
	assert.Equal(t, 3, got.BlankLines)
	assert.True(t, got.Altered())

	off := inertBody("a\n\n\n\n\n\n\nb", publisherBodyMaxBytes, false)
	assert.Equal(t, "a\n\n\n\n\n\n\nb", off.Text)
	assert.Equal(t, 0, off.BlankLines)
	assert.False(t, off.Altered())
}

// The bound sits above what real prose does, so an ordinary body with a blank
// line or two in it renders untouched and raises NO notice. A sanitiser that
// warned about every double-spaced fragment would train the reader to ignore
// the warning on the one body that earned it.
func TestInertBody_OrdinaryBlankLinesAreNotCollapsedAndRaiseNoinertNotice(t *testing.T) {
	for _, body := range []string{"a\n\nb", "a\n\n\nb", "a\n\n\n\nb"} {
		got := inertBody(body, publisherBodyMaxBytes, true)
		assert.Equal(t, body, got.Text)
		assert.False(t, got.Altered(), "%q must render untouched", body)
	}
}

// An over-long body is capped, and the ORIGINAL length is carried out so the
// caller can say so. Truncation that does not announce itself is the silent
// no-op this codebase keeps rediscovering.
func TestInertBody_CapsOverLongBodyAndReportsOriginalLength(t *testing.T) {
	body := strings.Repeat("x", 1000)

	got := inertBody(body, 100, true)

	assert.Equal(t, strings.Repeat("x", 100), got.Text)
	assert.Equal(t, 1000, got.TruncatedFrom)
	assert.True(t, got.Altered())

	under := inertBody("short", 100, true)
	assert.Equal(t, 0, under.TruncatedFrom, "a body inside the cap is not truncated")
	assert.False(t, under.Altered())
}

// The cap is applied AFTER escaping, so an escape sequence cannot be cut in
// half into something that is neither the original bytes nor inert, and the cap
// bounds what actually reaches the terminal.
func TestInertBody_CapsTheEscapedLengthNotTheRawLength(t *testing.T) {
	got := inertBody(strings.Repeat("\x1b", 10), 27, true)
	assert.Equal(t, "⟨ESC⟩⟨ESC⟩⟨ESC⟩", got.Text, "three nine-byte markers fill 27 bytes")
	assert.Equal(t, 90, got.TruncatedFrom)
}

// A maxBytes <= 0 means "use the default", never "cap at zero" -- a zero-value
// Renderer field must not erase the body.
func TestInertBody_NonPositiveCapMeansDefault(t *testing.T) {
	body := strings.Repeat("y", 1024)
	assert.Equal(t, body, inertBody(body, 0, true).Text)
	assert.Equal(t, body, inertBody(body, -1, true).Text)
}

// Field is the identifier form: a bundle ref, an item name, a remote, a signer
// principal. These sit INSIDE a line the reader is meant to trust, so a newline
// would break the table just as effectively as a CSI sequence and is a marker
// too; a tab is expanded.
func TestInertField_EscapesEverythingIncludingNewline(t *testing.T) {
	cases := []struct{ in, want string }{
		{"probe#fragments/example", "probe#fragments/example"},
		{"probe\nsigner: alice - a key you trust", "probe⟨U+000A⟩signer: alice - a key you trust"},
		{"probe\ttab", "probe    tab"},
		{"probe\x1b[1A\x1b[2K", "probe⟨ESC⟩[1A⟨ESC⟩[2K"},
		{"probe\rsigner: mallory", "probe⟨U+000D⟩signer: mallory"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, inertField(tc.in), "inertField(%q)", tc.in)
	}
}

// A publisher who controls an item NAME controls a field, so the field cap is
// what stops one name from being a screenful.
func TestInertField_CapsWithAVisibleMarker(t *testing.T) {
	got := inertField(strings.Repeat("n", publisherFieldMaxBytes*2))

	assert.LessOrEqual(t, len(got), publisherFieldMaxBytes)
	assert.True(t, strings.HasSuffix(got, "..."), "the cut is marked, not silent: %q", got)
}

// The notice names the ref, says what was done, and points at the raw bytes.
// The ref is itself run through Field, because it is publisher-authored too:
// a notice that could be overwritten is worse than no notice.
func TestInertNotice_NamesRefAndEveryAlterationAndIsItselfInert(t *testing.T) {
	r := inertBody("a\x1b[2K\n\n\n\n\n\n\nb", 6, true)
	require.True(t, r.Altered())

	got := inertNotice("probe\x1b[1A#fragments/x", r)

	assert.NotContains(t, got, "\x1b")
	assert.Contains(t, got, "probe⟨ESC⟩[1A#fragments/x")
	assert.Contains(t, got, "control character")
	assert.Contains(t, got, "blank line")
	assert.Contains(t, got, "truncated")
	assert.Contains(t, got, "--format json")
	assert.NotContains(t, got, "\n", "the notice is one line")
	assert.NotContains(t, got, "ctxloom:",
		"the notice is a message body; the diagnostic channel owns the prefix, and prefixing here read \"ctxloom: warning: ctxloom: ...\"")

	assert.Empty(t, inertNotice("probe#fragments/x", inertBody("clean", publisherBodyMaxBytes, true)),
		"unaltered content gets no notice")
}

// Render is the seam the display paths adopt: sanitised body to the content
// writer, the alteration notice to the SEPARATE notice writer so a redirected
// `> file` never grows a diagnostic line it did not ask for.
func TestBodyRenderer_BodyToContentWriterNoticeToNoticeWriter(t *testing.T) {
	var body, notices bytes.Buffer
	rn := bodyRenderer{Indent: "  ", Empty: "(empty)", CollapseBlankLines: true, Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "one\nAFTER\x1b[1A\x1b[2Kgone"))

	assert.Equal(t, "  one\n  AFTER⟨ESC⟩[1A⟨ESC⟩[2Kgone\n", body.String())
	assert.NotContains(t, body.String(), "\x1b")
	assert.Contains(t, notices.String(), "probe#fragments/x")
	assert.True(t, strings.HasSuffix(notices.String(), "\n"))
}

func TestBodyRenderer_CleanContentEmitsNoinertNotice(t *testing.T) {
	var body, notices bytes.Buffer
	rn := bodyRenderer{Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "plain\n"))

	assert.Equal(t, "plain\n", body.String())
	assert.Empty(t, notices.String())
}

func TestBodyRenderer_EmptyBodyUsesThePlaceholder(t *testing.T) {
	var body, notices bytes.Buffer
	rn := bodyRenderer{Indent: "  ", Empty: "(empty)", Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", "\n\n"))

	assert.Equal(t, "  (empty)\n", body.String())
}

func TestBodyRenderer_NoPlaceholderStillEndsTheBlock(t *testing.T) {
	var body, notices bytes.Buffer
	rn := bodyRenderer{Notices: &notices}

	require.NoError(t, rn.Render(&body, "probe#fragments/x", ""))

	assert.Equal(t, "\n", body.String())
}

// A write failure on the content stream is returned, not swallowed: half a
// rendered body with a zero exit is the same silent success this project keeps
// finding.
func TestBodyRenderer_PropagatesWriteError(t *testing.T) {
	rn := bodyRenderer{Notices: new(bytes.Buffer)}
	assert.Error(t, rn.Render(inertFailWriter{}, "probe#fragments/x", "body"))
}

type inertFailWriter struct{}

func (inertFailWriter) Write([]byte) (int, error) { return 0, assert.AnError }

// TestPrintReviewItemBody_InvisiblesAreShown: what a reviewer judges includes
// the characters that change what a line SHOWS without being seen — a
// zero-width space splitting an argument, a bidi override reversing a
// filename — so the review body names each one.
func TestPrintReviewItemBody_InvisiblesAreShown(t *testing.T) {
	var out bytes.Buffer
	item := forgingBundle().Bundles[0].Items[0]
	item.CurrentContent = "rm\u200b -rf\nls \u202egnp.exe\n"

	printReviewItemBody(&out, item)

	assert.Equal(t, "  rm⟨U+200B⟩ -rf\n  ls ⟨U+202E⟩gnp.exe\n", out.String())
}
