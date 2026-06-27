// Package tokens owns ctxloom's token-count estimate. It is deliberately the one
// place that knows the heuristic, so every surface that reports a token count —
// the dry-run assembly preview, distillation chunking — agrees, and a real
// tokenizer can replace the heuristic here without touching call sites.
package tokens

// CharsPerToken is the rough characters-per-token ratio. A crude heuristic, but
// a single owned constant beats scattered magic numbers.
const CharsPerToken = 4

// Estimate returns a rough token count for text.
func Estimate(text string) int {
	return len(text) / CharsPerToken
}
