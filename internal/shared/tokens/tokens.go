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
// chunker needs: it slices by byte offset (see internal/memory's chunkText and
// its use of textutil.TruncateBytes), so a character-based budget compared
// against a byte length would under-chunk exactly the text that is already
// hardest to fit.
const BytesPerToken = 4

// Estimate returns a rough token count for text.
func Estimate(text string) int {
	return len(text) / BytesPerToken
}
