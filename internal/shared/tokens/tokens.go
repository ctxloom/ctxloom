// Package tokens owns ctxloom's token-count estimate. It is deliberately the one
// place that knows the heuristic, so every surface that reports a token count —
// the dry-run assembly preview, distillation chunking — agrees, and a real
// tokenizer can replace the heuristic here without touching call sites.
package tokens

// BytesPerToken is the rough bytes-per-token ratio. A crude heuristic, but a
// single owned constant beats scattered magic numbers.
//
// BYTES, not characters — the name says so because the arithmetic does. Go's
// len() over a string counts bytes, and for non-ASCII text the two diverge by
// 2-4x (a CJK rune is three bytes in UTF-8, an emoji four), so a constant
// named for characters but applied to bytes silently means something other
// than what every consumer reads it as. The byte reading is also the one the
// budget arithmetic needs: callers slice by byte offset (see internal/memory's
// fitToBudget and its use of textutil.TruncateBytes), so a character-based
// budget compared against a byte length would under-count exactly the text
// that is already hardest to fit.
const BytesPerToken = 4

// Estimate returns a rough token count for text.
//
// It rounds UP, so any non-empty text estimates at one token or more. Plain
// integer division returned 0 for anything shorter than BytesPerToken, and a
// non-empty string that estimates at zero is a trap for every caller that
// gates on the result: a budget check concluding there is nothing to send, a
// "does this need chunking" test, a ratio with the estimate in the
// denominator. Only genuinely empty text estimates at zero. Rounding up is
// also the safer direction for an estimate feeding a budget — it can never
// under-report.
func Estimate(text string) int {
	return (len(text) + BytesPerToken - 1) / BytesPerToken
}

// Budget returns the rough size in BYTES of text that would estimate at
// tokens — the inverse of Estimate. A non-positive budget is no size at all.
//
// It exists so this package's promise holds in both directions: "a real
// tokenizer can replace the heuristic here without touching call sites" was
// true of Estimate's call sites and false of the constant's. A consumer sizing
// a chunk from a token budget multiplied BytesPerToken itself, which is a
// second implementation of the heuristic living outside the package that owns
// it — and one no real tokenizer could satisfy, because a tokenizer is not
// invertible by multiplication. With the inverse expressed as a function the
// substitution point is inside this package for both directions.
func Budget(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * BytesPerToken
}
