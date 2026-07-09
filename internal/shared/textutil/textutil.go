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
