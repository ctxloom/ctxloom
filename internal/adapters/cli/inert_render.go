package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/displaysafe"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// Every path in this package that renders PUBLISHER-AUTHORED bytes onto a
// terminal goes through here — `fragment|command show`, `bundle view`, and
// every identifier a listing interpolates (inertField). Four display paths once had the same
// defect independently (delicious-goatskin), which is why this is one seam and
// not four patches: a new display path adopts it by construction.
//
// The threat it closes is a FORGERY of what the reader sees, not a cosmetic
// glitch. A terminal executes what it is sent: a cursor-up plus erase-line
// pair lets a rendered body rewrite the line above it — the line naming which
// bundle the item came from. A double-quoted YAML scalar decodes "\e[1A" into a live
// ESC at RENDER time, so nothing on disk looks dangerous; the render seam is
// the only place that sees the bytes as the terminal will. The escaping itself
// is displaysafe.Text's, the one policy every human-facing surface shares.
//
// The line this draws is TERMINAL vs STRUCTURED. Only the --format text
// rendering comes through here. A json/yaml/toml consumer is not a terminal —
// its own grammar already renders a control byte inert inside a string, and
// a marker written into it would corrupt what a script parses — so the
// structured result keeps the raw bytes and the human rendering is the
// sanitised one. Where a command buffers content once and uses it for both
// (bundle view), that buffer stays raw and the sanitising happens in the text
// closure.

const (
	// publisherBodyMaxBytes caps one rendered body. It is generous on purpose:
	// a bomb-stopper, not an abridgement — a review surface that hid the tail
	// of what it asks a human to judge would defeat its own point.
	publisherBodyMaxBytes = 256 << 10

	// publisherFieldMaxBytes caps one rendered identifier — a bundle ref, an
	// item name, a remote, a signer principal. These sit inside a line, so
	// the budget is a line's worth and not a screen's.
	publisherFieldMaxBytes = 256

	// maxNewlineRun bounds a run of consecutive newlines: four of them, so
	// three blank lines survive. It bounds how far a publisher can push the
	// framing around its content up the screen, and sits above anything real
	// prose does — set tighter, it fires on ordinary bodies and the notice
	// becomes noise a reader learns to ignore.
	maxNewlineRun = 4

	// markerOpen begins every marker displaysafe.Text writes and nothing else
	// it adds; it keeps a typed one as is. Counting the ones it added is the
	// count of characters it escaped.
	markerOpen = "⟨"
)

// inertResult is one sanitised body plus what had to be done to it, so a
// caller can SAY what changed (inertNotice).
type inertResult struct {
	// Text is the sanitised body: what may be written to a terminal.
	Text string
	// Escaped counts characters shown as markers. Escaping loses nothing.
	Escaped int
	// BlankLines counts newlines removed by the blank-line collapse.
	BlankLines int
	// TruncatedFrom is the pre-cap byte length when the body was cut, 0 when
	// it was not.
	TruncatedFrom int
}

// Altered reports whether sanitising changed anything a reader is owed a word
// about. Expanding a tab or reading CRLF as one line break is layout, not
// alteration.
func (r inertResult) Altered() bool {
	return r.Escaped > 0 || r.BlankLines > 0 || r.TruncatedFrom > 0
}

// inertBody renders a multi-line body inert for a terminal (displaysafe.Text),
// optionally bounding blank-line runs, and caps the ESCAPED text so the cap
// bounds what reaches the terminal. maxBytes <= 0 means publisherBodyMaxBytes,
// never "cap at zero".
//
// collapseBlankLines is the caller's choice because it is the one alteration
// that changes a DOCUMENT rather than defusing it: a caller dumping a whole
// document a user may redirect to a file leaves it off.
func inertBody(s string, maxBytes int, collapseBlankLines bool) inertResult {
	if maxBytes <= 0 {
		maxBytes = publisherBodyMaxBytes
	}
	text := displaysafe.Text(s, true)
	res := inertResult{Text: text, Escaped: strings.Count(text, markerOpen) - strings.Count(s, markerOpen)}
	if collapseBlankLines {
		res.Text, res.BlankLines = collapseBlankRuns(res.Text)
	}
	if len(res.Text) > maxBytes {
		res.TruncatedFrom = len(res.Text)
		res.Text = textutil.TruncateBytes(res.Text, maxBytes)
	}
	return res
}

// inertField renders one publisher-authored identifier inert and on the line
// it was written into: a newline is a marker here, since a name carrying one
// could open a line of its own and impersonate the ctxloom-authored line
// below it. An over-long name is cut with the "..." marker, so the cut is not
// silent either.
func inertField(s string) string {
	return textutil.Ellipsize(displaysafe.Text(s, false), publisherFieldMaxBytes)
}

// inertNotice is the one line explaining what inertBody did to ref's content,
// or "" when it did nothing. It names every alteration — escapes are lossless,
// the collapse and the cap are not, and a reader deciding whether to trust
// content is owed the difference. ref is publisher-authored too, so it goes
// through inertField. The line carries no channel prefix: the diagnostic
// channel it is handed to owns that.
func inertNotice(ref string, r inertResult) string {
	if !r.Altered() {
		return ""
	}
	var parts []string
	if r.Escaped > 0 {
		parts = append(parts, fmt.Sprintf("%d control character(s) escaped", r.Escaped))
	}
	if r.BlankLines > 0 {
		parts = append(parts, fmt.Sprintf("%d blank line(s) collapsed", r.BlankLines))
	}
	if r.TruncatedFrom > 0 {
		parts = append(parts, fmt.Sprintf("truncated to %d of %d bytes", len(r.Text), r.TruncatedFrom))
	}
	return fmt.Sprintf(
		"%s is publisher-authored and was rendered inert for this terminal (%s); use --format json for the raw bytes",
		inertField(ref), strings.Join(parts, ", "))
}

// collapseBlankRuns caps any run of newlines at maxNewlineRun and returns the
// text with the number of newlines removed, which keeps the collapse from
// being silent.
func collapseBlankRuns(s string) (string, int) {
	var (
		b       strings.Builder
		run     int
		removed int
	)
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' {
			run++
			if run > maxNewlineRun {
				removed++
				continue
			}
		} else {
			run = 0
		}
		b.WriteRune(r)
	}
	return b.String(), removed
}

// bodyRenderer renders one block of publisher-authored content.
type bodyRenderer struct {
	// Indent prefixes every rendered line, applied AFTER sanitising, so no
	// publisher byte can forge or escape it.
	Indent string
	// Empty is what to print when the body has no content; "" prints only
	// the block's closing newline.
	Empty string
	// MaxBytes caps the body; 0 means publisherBodyMaxBytes.
	MaxBytes int
	// CollapseBlankLines caps runs of blank lines (see inertBody).
	CollapseBlankLines bool
	// Notices receives the alteration notice; nil means os.Stderr. A separate
	// stream, so a `> file` redirect never grows a diagnostic line it did not
	// ask for.
	Notices io.Writer
}

// Render writes content to w, sanitised, indented, and always terminated by a
// newline; when sanitising altered anything it writes the notice to Notices.
//
// A write error on the content stream is returned: half a body with a zero
// exit is a silent success. A failure writing the NOTICE is not fatal — the
// safe bytes already landed.
func (rn bodyRenderer) Render(w io.Writer, ref, content string) error {
	res := inertBody(content, rn.MaxBytes, rn.CollapseBlankLines)

	body := strings.TrimRight(res.Text, "\n")
	var b strings.Builder
	if body == "" {
		if rn.Empty != "" {
			b.WriteString(rn.Indent)
			b.WriteString(rn.Empty)
		}
		b.WriteString("\n")
	} else {
		for _, line := range strings.Split(body, "\n") {
			b.WriteString(rn.Indent)
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if notice := inertNotice(ref, res); notice != "" {
		notices := rn.Notices
		if notices == nil {
			notices = os.Stderr
		}
		_, _ = fmt.Fprintln(notices, notice)
	}
	return nil
}

// publisherBody returns the renderer for one block of publisher-authored
// content. indent prefixes each line, empty is what to print when there is no
// content at all, and collapseBlankLines bounds how far a body can push the
// warning that surrounds it up the screen.
func publisherBody(indent, empty string, collapseBlankLines bool) bodyRenderer {
	return bodyRenderer{
		Indent:             indent,
		Empty:              empty,
		CollapseBlankLines: collapseBlankLines,
		Notices:            diagNotices{},
	}
}

// diagNotices routes the alteration notice onto ctxloom's human-readable
// diagnostic channel (stderr) rather than onto the content stream.
type diagNotices struct{}

func (diagNotices) Write(p []byte) (int, error) {
	clidiag.Warn("ctxloom", "%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
