package refuri

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A ctxloom reference CANNOT carry a control character. The grammar is built
// entirely from URL text, "/"-separated path segments and "#<kind>/<name>"
// selectors, none of which admit one — so a control character in a ref is
// either a bug upstream or an attack, never authored intent.
//
// It is a security property, not tidiness. A ref is interpolated verbatim into
// the LF-delimited countersign preimage (signing.CountersignPayload), where an
// embedded LF closes the `ref:` line early and lets the remainder of the ref
// forge the `form:` and `len:` lines the framing emits after it — two distinct
// (assertion, ref, form, payload) tuples framing to identical bytes, so one
// signature verifies for both and both file at one index hash. It is also
// rendered to the human whose approval is the entire point of the review gate:
// CR, backspace and ESC let a hostile ref repaint the terminal so the string
// shown is not the string being approved.
//
// The whole C0 range plus DEL is stripped rather than only CR/LF, because both
// hazards above generalise past the two characters that happen to break the
// frame, and no legal ref loses anything.
//
// The rule is enforced twice, deliberately and independently: stripped HERE, at
// ingest, so no consumer has to re-check; and REFUSED in
// signing.CountersignHeader.Validate, because the frame is the thing being
// signed and must not depend on any caller having come through this door. The
// second layer does not import this one — a defence in depth that shares an
// implementation is one layer.
//
// Deleting is the INGEST answer and is confined to it. Nothing here is
// exported for a display path to borrow: a string on its way to a terminal is
// shared/termsafe's business, and termsafe ESCAPES rather than deletes so the
// human reading a trust line can see that a publisher put a control byte
// there. Deletion on a display surface is lossy AND silent — two refs
// differing only by a control character render identically — which is exactly
// the forgery the trust surface exists to prevent.
func isRefControlChar(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// NormalizeRef is the ingest normaliser every reference passes through as it
// enters ctxloom from argv, a config or bundle file, a lockfile, a remote
// payload or an MCP argument. It strips control characters (see
// isRefControlChar) and returns the cleaned ref.
//
// A strip is NEVER silent: normalising user input without saying so is how a
// malformed ref becomes an invisible one. The warning is additive — it does not
// change control flow, and a ref that needed no cleaning is returned byte for
// byte. Both refs are printed with %q, which escapes exactly the characters
// being complained about, so the diagnostic itself cannot be used to paint the
// terminal.
func NormalizeRef(ref string) string {
	if strings.IndexFunc(ref, isRefControlChar) == -1 {
		return ref
	}
	clean := strings.Map(func(r rune) rune {
		if isRefControlChar(r) {
			return -1
		}
		return r
	}, ref)
	clidiag.WarnOnce("ctxloom", "reference %q contained control characters and was read as %q "+
		"— a ctxloom reference cannot carry them; this is a bug upstream or an attempt to forge one", ref, clean)
	return clean
}
