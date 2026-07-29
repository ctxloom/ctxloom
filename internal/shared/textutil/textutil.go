// Package textutil holds small string helpers shared across ctxloom packages.
package textutil

import "unicode/utf8"

// TruncateBytes returns s shortened to at most maxBytes bytes, backing off to
// the nearest UTF-8 rune boundary so a multibyte rune is never split in half.
// Callers that want a trailing ellipsis append it themselves. A maxBytes <= 0
// yields the empty string; an s already within the cap is returned unchanged.
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	// If the cut landed inside a multibyte rune, the trailing 1-3 bytes are an
	// incomplete sequence that decodes as RuneError with size 1. Drop those
	// trailing partial bytes until the final rune is whole. A legitimately
	// encoded U+FFFD decodes with size 3, so it is left intact.
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut
}

// ellipsis is the marker Ellipsize reserves room for.
const ellipsis = "..."

// Ellipsize returns s shortened to fit within maxBytes bytes IN TOTAL,
// appending "..." when it had to cut. It guarantees len(result) <= maxBytes —
// the suffix is reserved from the budget, not added on top of it.
//
// U129-F01: nine call sites used to hand-roll the second half of this as
// TruncateBytes(s, w-3) + "...", so the result exceeded the requested cap by
// the suffix length and every caller had to pre-subtract 3 from its real
// column width (17, 15, 32, 16, 57, 67 — none a round width). Nothing enforced
// that relationship; a caller who forgot overflowed its column silently.
//
// A maxBytes <= 0 yields the empty string. When maxBytes is too small to hold
// the marker at all, content wins over the marker: the result is a plain
// TruncateBytes to maxBytes. The cut always lands on a UTF-8 rune boundary.
func Ellipsize(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= len(ellipsis) {
		return TruncateBytes(s, maxBytes)
	}
	return TruncateBytes(s, maxBytes-len(ellipsis)) + ellipsis
}
