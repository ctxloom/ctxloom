// Package keymatch answers one question: given a key a document declared that
// nothing models, which key did the author probably mean?
//
// It exists so the answer is the same everywhere. ctxloom refuses an unknown
// key in more than one place — the config schema's additionalProperties
// violations (internal/config) and the strict YAML decode of a bundle
// (internal/bundles) — and "did you mean" is only useful if it is calibrated
// identically at each of them. A second, slightly different edit-distance
// budget in a second package is how one surface starts suggesting `ui` for
// `sync` while the other stays quiet.
package keymatch

// Nearest returns the known key closest to the offending one, when it is close
// enough to be a plausible typo, and "" when nothing is near.
//
// "Close enough" is an edit distance within a budget of at most 2 and never
// more than a third of the offending key's length. The length term is what
// keeps a short key from matching an unrelated short key — without it `sync`
// "suggests" `ui` — and the constant ceiling keeps a long key from matching
// something a reader would not recognize as the same word.
func Nearest(key string, known []string) string {
	best, bestDist := "", 1<<30
	budget := len(key) / 3
	if budget > 2 {
		budget = 2
	}
	if budget < 1 {
		budget = 1
	}
	for _, k := range known {
		if d := editDistance(key, k); d < bestDist {
			best, bestDist = k, d
		}
	}
	if bestDist > budget {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance between a and b (two-row DP).
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
