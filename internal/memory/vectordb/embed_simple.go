//go:build vectors

package vectordb

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

const (
	simpleEmbedDimension = 384 // Match common embedding dimension
)

// SimpleEmbedder provides basic keyword-based embeddings.
// This is a fallback when no ML-based embedder is available.
// It uses word hashing to create sparse vectors - not semantic but enables keyword matching.
type SimpleEmbedder struct {
	dimension int
}

// NewSimpleEmbedder creates a new simple keyword-based embedder.
func NewSimpleEmbedder() *SimpleEmbedder {
	return &SimpleEmbedder{
		dimension: simpleEmbedDimension,
	}
}

// Embed generates a simple hash-based embedding for the given text.
func (e *SimpleEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	// Tokenize text into words
	words := tokenize(text)

	// Create embedding vector
	embedding := make([]float32, e.dimension)

	// Hash each word and add to embedding
	for _, word := range words {
		// Get hash positions for this word
		h := fnv.New64a()
		h.Write([]byte(word))
		hash := h.Sum64()

		// Use multiple hash positions for better distribution
		pos1 := int(hash % uint64(e.dimension))
		pos2 := int((hash >> 16) % uint64(e.dimension))
		pos3 := int((hash >> 32) % uint64(e.dimension))

		// Add to embedding (simulating TF-IDF-like behavior)
		embedding[pos1] += 1.0
		embedding[pos2] += 0.5
		embedding[pos3] += 0.25
	}

	// Normalize to unit vector
	normalize(embedding)

	return embedding, nil
}

// Close releases resources.
func (e *SimpleEmbedder) Close() error {
	return nil
}

// tokenize splits text into lowercase words.
func tokenize(text string) []string {
	text = strings.ToLower(text)

	var words []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else if current.Len() > 0 {
			word := current.String()
			// Skip very short words and common stop words
			if len(word) > 2 && !isStopWord(word) {
				words = append(words, word)
			}
			current.Reset()
		}
	}

	// Don't forget the last word
	if current.Len() > 0 {
		word := current.String()
		if len(word) > 2 && !isStopWord(word) {
			words = append(words, word)
		}
	}

	return words
}

// normalize converts a vector to unit length.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}

	if sum == 0 {
		return
	}

	magnitude := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= magnitude
	}
}

// isStopWord returns true for common English stop words.
func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"the": true, "and": true, "for": true, "are": true, "but": true,
		"not": true, "you": true, "all": true, "can": true, "had": true,
		"her": true, "was": true, "one": true, "our": true, "out": true,
		"has": true, "have": true, "been": true, "this": true, "that": true,
		"with": true, "they": true, "from": true, "will": true, "would": true,
		"there": true, "their": true, "what": true, "about": true, "which": true,
		"when": true, "make": true, "like": true, "just": true, "over": true,
		"such": true, "into": true, "than": true, "them": true, "some": true,
	}
	return stopWords[word]
}
