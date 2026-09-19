package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/termsafe"
)

// Every path in this package that renders PUBLISHER-AUTHORED bytes onto a
// terminal goes through here — `review`'s listing, header, item bodies and
// diffs, `fragment|command show`, and `bundle view`. Four of them had the same
// defect independently (delicious-goatskin), which is why this is one seam and
// not four patches: a fifth display path is already queued behind it, and the
// only version of this fix that survives that is the one a new caller adopts
// by construction.
//
// The line this draws is TERMINAL vs STRUCTURED. Only the --format text
// rendering comes through here. A json/yaml/toml consumer is not a terminal —
// its own grammar already renders a control byte inert inside a string, and
// escaping the payload a second time would corrupt what a script parses — so
// the structured result keeps the raw bytes and the human rendering is the
// sanitised one. Where a command buffers content once and uses it for both
// (bundle view), that buffer stays raw and the sanitising happens in the text
// closure.

// publisherBody returns the renderer for one block of publisher-authored
// content. indent prefixes each line, empty is what to print when there is no
// content at all, and collapseBlankLines bounds how far a body can push the
// warning that surrounds it up the screen.
func publisherBody(indent, empty string, collapseBlankLines bool) termsafe.Renderer {
	return termsafe.Renderer{
		Indent:             indent,
		Empty:              empty,
		CollapseBlankLines: collapseBlankLines,
		Notices:            termsafeNotices{},
	}
}

// termsafeNotices routes termsafe's alteration notice onto ctxloom's
// human-readable diagnostic channel rather than onto the content stream.
// A `ctxloom bundle view x > file` must not grow a diagnostic line the caller
// never asked for, and stderr is where every other human-readable diagnostic
// in this binary goes.
type termsafeNotices struct{}

func (termsafeNotices) Write(p []byte) (int, error) {
	clidiag.Warn("ctxloom", "%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
