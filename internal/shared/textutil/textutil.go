// Package textutil holds ONE invariant, not a grab-bag: shortening a string to
// fit a BYTE budget without ever splitting a UTF-8 rune. Both exported
// functions are that operation — TruncateBytes cuts, Ellipsize cuts and marks
// the cut — and a helper that is not about a byte budget on a rune boundary
// does not belong here, because the name says nothing about what it may
// contain and an unrelated helper landing here inherits neither review nor
// meaning. That budget is measured in BYTES: it is not a rune count and it is
// not a terminal display width, and callers sizing a padded column need one of
// those instead.
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
	//
	// The backoff is bounded at utf8.UTFMax-1 because that is the most bytes a
	// split rune can leave behind. Past three, the trailing bytes are not a
	// severed rune but input that was already invalid, and dropping them
	// deletes real content — an unbounded loop walks the whole prefix away and
	// answers "" for a string that had plenty in it. Invalid bytes in, invalid
	// bytes out: this function caps length on rune boundaries, it does not
	// sanitize (a byte the caller supplied mid-string is passed through today
	// too).
	for dropped := 0; len(cut) > 0; dropped++ {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			return cut
		}
		if dropped == utf8.UTFMax-1 {
			return s[:maxBytes]
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}

// ellipsis is the marker Ellipsize reserves room for.
const ellipsis = "..."

// Ellipsize returns s shortened to fit within maxBytes bytes IN TOTAL,
// appending "..." when it had to cut. It guarantees len(result) <= maxBytes —
// the suffix is reserved from the budget, not added on top of it.
//
// Callers must not hand-roll the second half of this as
// TruncateBytes(s, w-3) + "...": the result then exceeds the requested cap by
// the suffix length, and every caller has to pre-subtract 3 from its real
// column width. Nothing enforces that relationship, so a caller who forgets
// overflows its column silently.
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
