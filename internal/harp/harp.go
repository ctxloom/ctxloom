// Package harp generates Human Appropriate Random Phraselets — pronounceable,
// memorable identifiers of the form "swift-amber-falcon".
//
// This is a fully-native Go port of harp-core (github.com/benjaminabbitt/harp).
// Word lists are embedded; no cgo, no WASM, no wazero. API mirrors the Rust
// crate so a future extraction to github.com/benjaminabbitt/harp-go is
// mechanical: copy this directory out, change the package import path, done.
package harp

import (
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"fmt"
	"strings"
)

//go:embed adjectives.txt
var adjectivesData string

//go:embed nouns.txt
var nounsData string

var (
	adjectives = parseList(adjectivesData)
	nouns      = parseList(nounsData)
)

func parseList(data string) []string {
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	out := lines[:0]
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Options configures name generation. Zero values fall back to defaults
// (3 components, "-" separator, no length cap).
type Options struct {
	// Components is the total number of words in the name
	// (N-1 adjectives + 1 noun). Valid range: 2..16. Defaults to 3.
	Components int

	// MaxElementLength caps the length of each individual word in chars.
	// 0 disables the cap.
	MaxElementLength int

	// Separator is the delimiter between words. Defaults to "-".
	Separator string
}

func (o Options) normalize() Options {
	if o.Components < 2 {
		o.Components = 3
	}
	if o.Components > 16 {
		o.Components = 16
	}
	if o.Separator == "" {
		o.Separator = "-"
	}
	return o
}

// rngRead is the entropy source. Package-level seam so tests can stub
// without exposing crypto/rand on the public API.
var rngRead = rand.Read

// GenerateName returns a name with default options ("adj-adj-noun").
func GenerateName() string {
	return GenerateNameWithOptions(Options{})
}

// GenerateNameWithOptions returns a name built per the given options.
// Invalid options are silently clamped (see Options.normalize).
func GenerateNameWithOptions(opts Options) string {
	opts = opts.normalize()
	parts := make([]string, opts.Components)
	for i := 0; i < opts.Components-1; i++ {
		parts[i] = pickWord(adjectives, opts.MaxElementLength)
	}
	parts[opts.Components-1] = pickWord(nouns, opts.MaxElementLength)
	return strings.Join(parts, opts.Separator)
}

// pickWord returns a uniformly random entry from words, retrying until
// MaxElementLength is satisfied. The 1000-attempt cap is a safety net
// against pathological inputs (e.g. maxLen smaller than every word).
func pickWord(words []string, maxLen int) string {
	for range 1000 {
		w := words[randIndex(len(words))]
		if maxLen <= 0 || len(w) <= maxLen {
			return w
		}
	}
	return words[0]
}

// randIndex returns a uniform random integer in [0, n). Rejection-sampled
// to avoid the modulo bias that a naive `binary.Uint32 % n` would have for
// non-power-of-two n.
func randIndex(n int) int {
	if n <= 0 {
		return 0
	}
	max := ^uint32(0)
	limit := max - (max % uint32(n))
	var buf [4]byte
	for {
		if _, err := rngRead(buf[:]); err != nil {
			panic(fmt.Sprintf("harp: rng read: %v", err))
		}
		v := binary.BigEndian.Uint32(buf[:])
		if v < limit {
			return int(v % uint32(n))
		}
	}
}
