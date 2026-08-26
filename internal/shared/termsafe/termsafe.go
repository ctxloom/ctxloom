// Package termsafe renders publisher-authored bytes onto a terminal without
// letting their author control it.
//
// The threat it closes is a TRUST-DECISION FORGERY, not a cosmetic glitch.
// `ctxloom review` renders content a publisher wrote at exactly the moment a
// human is deciding whether to trust that publisher, and a terminal executes
// what it is sent: a cursor-up plus erase-line pair lets the rendered body
// rewrite the line above it — the line naming which bundle and which signer
// the item came from. The naive form of this is blocked upstream by the YAML
// parser refusing raw control bytes, but a double-quoted scalar decodes
// "\e[1A" into a live ESC at RENDER time, so nothing on disk and nothing in a
// `git diff` looks dangerous. Sanitising at the render seam is the only place
// that sees the bytes as the terminal will.
//
// Everything here ESCAPES rather than deletes. A body that quietly loses
// content is this project's characteristic silent no-op wearing a new hat, so
// a control character becomes its visible caret form (the notation `cat -v`
// prints) and the reader still knows exactly what the publisher wrote. Only
// two alterations lose bytes — the blank-line collapse and the length cap —
// and both are counted into Result and named by Notice.
//
// This package is deliberately about the TERMINAL. Structured output
// (--format json/yaml/toml) must NOT come through here: a JSON consumer is not
// a terminal, JSON's own grammar already renders a control byte inert as
// the six characters \u001b inside a string, and escaping the payload a
// second time would corrupt what a script parses.
package termsafe

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

const (
	// DefaultMaxBytes caps one rendered body. It is generous on purpose: this
	// is a bomb-stopper, not an abridgement, and a review surface that hides
	// the tail of what it is asking a human to judge would defeat its own
	// point. A prose fragment does not reach a quarter of a megabyte; one that
	// does is itself worth being told about.
	DefaultMaxBytes = 256 << 10

	// maxNewlineRun bounds a run of consecutive newlines: four of them, so
	// three blank lines survive. This is a BOUND on how far a publisher can
	// push the framing around its content up the screen, not a style rule, so
	// it is deliberately set above anything real prose does. Set tighter, it
	// fires on an ordinary double-blank-line body and the alteration notice
	// becomes noise a reader learns to ignore — which would cost more than it
	// buys on a surface whose whole job is to be believed.
	maxNewlineRun = 4

	// DefaultFieldMaxBytes caps one rendered identifier — a bundle ref, an
	// item name, a remote, a signer principal. These sit inside a line, so the
	// budget is a line's worth and not a screen's.
	DefaultFieldMaxBytes = 256
)

// Result is the outcome of sanitising one body: the safe bytes, plus what had
// to be done to them. The counts exist so a caller can SAY what changed —
// Notice turns them into that sentence.
type Result struct {
	// Text is the sanitised body: what may be written to a terminal.
	Text string
	// Escaped counts control characters rendered into visible, inert form.
	// Escaping loses nothing, so this alone never means content was dropped.
	Escaped int
	// BlankLines counts newlines removed by the blank-line collapse.
	BlankLines int
	// TruncatedFrom is the pre-cap byte length when the body was cut, and 0
	// when it was not. Bytes were dropped whenever this is non-zero.
	TruncatedFrom int
}

// Altered reports whether sanitising changed the bytes at all. A false here is
// the common case and is what suppresses the notice: a clean body renders
// exactly as its publisher wrote it, with nothing added.
func (r Result) Altered() bool {
	return r.Escaped > 0 || r.BlankLines > 0 || r.TruncatedFrom > 0
}

// Sanitize renders s inert for a terminal.
//
// It escapes every C0 control character except newline and tab (the two a
// prose body legitimately contains), DEL, the whole C1 block — U+009B is a
// single-byte CSI on a real terminal, so it introduces a sequence with no ESC
// anywhere in sight — the bidi controls that reorder what a reader sees, and
// any byte that is not valid UTF-8. C0 and DEL take caret notation (^[ for
// ESC, ^M for carriage return); everything else takes \uXXXX, and an invalid
// byte takes \xNN.
//
// Escaping the ESC BYTE, rather than matching sequence grammars, is the whole
// defence: there is no CSI/OSC/DCS/SS3 parser here to get subtly wrong, and a
// sequence whose introducer is inert is inert however it was spelled.
//
// collapseBlankLines bounds a run of blank lines, which bounds how far a
// publisher can push the surrounding warning up the screen. It is a caller's
// choice because it is the one alteration that changes a DOCUMENT rather than
// defusing it — see Renderer.CollapseBlankLines.
//
// maxBytes <= 0 means DefaultMaxBytes, never "cap at zero": a zero-value
// Renderer must render the body, not erase it. The cap is applied last, to the
// ESCAPED text, so the cap bounds what actually reaches the terminal and no
// half-cut multi-byte rune or half-escaped sequence can survive it.
func Sanitize(s string, maxBytes int, collapseBlankLines bool) Result {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	res := escapeControls(s, true)
	if collapseBlankLines {
		res.Text, res.BlankLines = collapseBlankRuns(res.Text)
	}
	if len(res.Text) > maxBytes {
		res.TruncatedFrom = len(res.Text)
		res.Text = textutil.TruncateBytes(res.Text, maxBytes)
	}
	return res
}

// Field renders one publisher-authored identifier inert and guaranteed to stay
// on the line it was written into. Newline and tab are escaped here where
// Sanitize keeps them: a name carrying a newline could otherwise open a line of
// its own and impersonate the ctxloom-authored line below it, which in a
// pending-review listing is precisely the forgery being closed.
//
// It returns a plain string because every caller interpolates it into a
// Printf; the escaping is visible in the output itself, and an over-long name
// is cut with the "..." marker so the cut is not silent either.
func Field(s string) string {
	return textutil.Ellipsize(escapeControls(s, false).Text, DefaultFieldMaxBytes)
}

// Notice returns the single line explaining what Sanitize did to ref's content,
// or "" when it did nothing. It names every alteration — escapes are lossless,
// the collapse and the cap are not, and a reader deciding whether to trust
// content is owed the difference.
//
// ref goes through Field because a bundle/item ref is publisher-authored too:
// a warning that its subject could overwrite would be worse than no warning.
//
// The line is a message BODY with no channel prefix of its own: the diagnostic
// channel it is handed to owns that, and prefixing here made ctxloom's own
// warning read "ctxloom: warning: ctxloom: ..." in the live repro.
func Notice(ref string, r Result) string {
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
		Field(ref), strings.Join(parts, ", "))
}

// Renderer is the seam every content-display path adopts. Four commands had
// this wrong the same way and a fifth was queued behind them, so the deliverable
// is one renderer with the framing differences as fields — not four patches
// that will drift.
type Renderer struct {
	// Indent prefixes every rendered line, so a body reads as a block under
	// its header. It is applied AFTER sanitising, so no publisher byte can
	// forge or escape the indent.
	Indent string
	// Empty is what to print when the body has no content at all. Empty ""
	// prints nothing but the block's closing newline.
	Empty string
	// MaxBytes caps the body; 0 means DefaultMaxBytes.
	MaxBytes int
	// CollapseBlankLines caps runs of blank lines. Callers rendering a BODY
	// set it; a caller dumping a whole document a user may redirect to a file
	// leaves it off, because collapsing there would hand back a file that is
	// not the one on disk.
	CollapseBlankLines bool
	// Notices is where the alteration notice goes; nil means os.Stderr. It is
	// a separate stream on purpose — human-readable diagnostics belong on
	// stderr in this codebase, and a `> file` redirect must not grow a
	// diagnostic line the caller never asked for.
	Notices io.Writer
}

// Render writes content to w, sanitised, indented, and always terminated by a
// newline; when sanitising altered anything it writes the Notice to Notices.
//
// A write error on the content stream is returned rather than swallowed: half a
// body with a zero exit is the silent success this project keeps rediscovering.
// A failure writing the NOTICE is not fatal — the safe bytes already landed,
// and failing the command because a warning could not be printed would trade a
// working render for nothing.
func (rn Renderer) Render(w io.Writer, ref, content string) error {
	res := Sanitize(content, rn.MaxBytes, rn.CollapseBlankLines)

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

	if notice := Notice(ref, res); notice != "" {
		notices := rn.Notices
		if notices == nil {
			notices = os.Stderr
		}
		fmt.Fprintln(notices, notice)
	}
	return nil
}

// escapeControls is the shared escaping pass. keepNewlineTab distinguishes the
// BODY reading (a prose block may span lines and indent with tabs) from the
// FIELD reading (an identifier must not leave its line).
func escapeControls(s string, keepNewlineTab bool) Result {
	var (
		b       strings.Builder
		escaped int
	)
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid byte, not a rune: a terminal in a legacy encoding can
			// read 0x9b here as CSI, and passing it through would also hand
			// the reader a body that is not valid UTF-8.
			fmt.Fprintf(&b, `\x%02x`, s[i])
			escaped++
			i++
			continue
		}
		i += size
		if writeRuneEscaped(&b, r, keepNewlineTab) {
			escaped++
		}
	}
	return Result{Text: b.String(), Escaped: escaped}
}

// writeRuneEscaped writes r to b in its safe rendering and reports whether that
// rendering ESCAPED it — i.e. whether the rune reached the writer as something
// other than itself. The bool is the counted unit: Result.Escaped is the number
// of runes the reader is not seeing verbatim.
func writeRuneEscaped(b *strings.Builder, r rune, keepNewlineTab bool) bool {
	switch {
	case (r == '\n' || r == '\t') && keepNewlineTab:
		b.WriteRune(r)
	case r < 0x20:
		// Caret notation: the control's printable partner 0x40 above it,
		// which is what `cat -v` prints and what the exploit report used.
		b.WriteByte('^')
		b.WriteByte(byte(r) + '@')
		return true
	case r == 0x7f:
		b.WriteString("^?")
		return true
	case r >= 0x80 && r <= 0x9f, isBidiControl(r):
		fmt.Fprintf(b, `\u%04x`, r)
		return true
	default:
		b.WriteRune(r)
	}
	return false
}

// isBidiControl reports whether r is a bidirectional formatting control.
//
// These are in scope for the same reason the CSI sequences are: they change
// what a reader SEES without changing what is stored, so a body can display a
// different line from the one it contains. The zero-width joiner family
// (U+200B..U+200D) is deliberately NOT here — it is load-bearing in emoji
// sequences and Indic scripts, and escaping it would corrupt legitimate prose
// to defend against something that cannot reorder anything.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x200e, r == 0x200f: // LRM, RLM
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}

// collapseBlankRuns caps any run of newlines at maxNewlineRun and returns the
// text with the number of newlines removed. That count is what keeps the
// collapse from being silent.
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
