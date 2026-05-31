package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/ctxloom/ctxloom/internal/textutil"
)

// JSONCompressor compresses JSON while preserving structure (keys, types).
// It truncates long string values while keeping high-entropy identifiers.
type JSONCompressor struct {
	// MaxValueLength is the maximum length for string values before truncation.
	MaxValueLength int

	// MaxArrayItems is the maximum number of array items to keep fully.
	MaxArrayItems int

	// EntropyThreshold determines what counts as a high-entropy value (0.0-1.0).
	// High-entropy values (UUIDs, hashes) are preserved.
	EntropyThreshold float64

	// PreserveNumbers keeps all numeric values (useful for IDs).
	PreserveNumbers bool
}

// NewJSONCompressor creates a JSON compressor with default settings.
func NewJSONCompressor() *JSONCompressor {
	return &JSONCompressor{
		MaxValueLength:   30,
		MaxArrayItems:    3,
		EntropyThreshold: 0.75,
		PreserveNumbers:  true,
	}
}

// CanHandle returns true for JSON content types.
func (c *JSONCompressor) CanHandle(ct ContentType) bool {
	return ct == ContentTypeJSON
}

// Compress reduces JSON size while preserving structure.
func (c *JSONCompressor) Compress(ctx context.Context, content string, ratio float64) (Result, error) {
	// Parse JSON. Fault tolerance: invalid JSON degrades to verbatim
	// pass-through rather than failing the caller.
	var data any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return verbatimResult(content, "json-compressor"), nil
	}

	// Compress the structure
	compressed, stats := c.compressValue(data, 0)

	// Marshal back to JSON. A marshal failure on already-parsed data is not
	// expected, but degrade to verbatim rather than erroring out.
	output, err := json.MarshalIndent(compressed, "", "  ")
	if err != nil {
		return verbatimResult(content, "json-compressor"), nil
	}

	return Result{
		Content:            string(output),
		OriginalSize:       len(content),
		CompressedSize:     len(output),
		Ratio:              float64(len(output)) / float64(len(content)),
		PreservedElements:  stats.preserved,
		CompressedElements: stats.compressed,
		ModelID:            "json-compressor",
	}, nil
}

type compressStats struct {
	preserved  []string
	compressed []string
}

func (c *JSONCompressor) compressValue(v any, depth int) (any, compressStats) {
	var stats compressStats

	switch val := v.(type) {
	case map[string]any:
		return c.compressObject(val, depth, &stats), stats

	case []any:
		return c.compressArray(val, depth, &stats), stats

	case string:
		return c.compressString(val, &stats), stats

	case float64:
		if c.PreserveNumbers {
			stats.preserved = append(stats.preserved, "number")
		}
		return val, stats

	case bool, nil:
		stats.preserved = append(stats.preserved, "primitive")
		return val, stats

	default:
		return val, stats
	}
}

func (c *JSONCompressor) compressObject(obj map[string]any, depth int, stats *compressStats) map[string]any {
	result := make(map[string]any)
	stats.preserved = append(stats.preserved, fmt.Sprintf("%d keys", len(obj)))

	for key, value := range obj {
		compressed, childStats := c.compressValue(value, depth+1)
		result[key] = compressed
		stats.preserved = append(stats.preserved, childStats.preserved...)
		stats.compressed = append(stats.compressed, childStats.compressed...)
	}

	return result
}

func (c *JSONCompressor) compressArray(arr []any, depth int, stats *compressStats) []any {
	if len(arr) == 0 {
		return arr
	}

	// Keep first N items fully, summarize the rest
	keepCount := c.MaxArrayItems
	if keepCount > len(arr) {
		keepCount = len(arr)
	}

	result := make([]any, 0, keepCount+1)

	for i := 0; i < keepCount; i++ {
		compressed, childStats := c.compressValue(arr[i], depth+1)
		result = append(result, compressed)
		stats.preserved = append(stats.preserved, childStats.preserved...)
		stats.compressed = append(stats.compressed, childStats.compressed...)
	}

	// If there are more items, add a summary
	if len(arr) > keepCount {
		remaining := len(arr) - keepCount
		result = append(result, fmt.Sprintf("... %d more items", remaining))
		stats.compressed = append(stats.compressed, fmt.Sprintf("%d array items", remaining))
	}

	return result
}

func (c *JSONCompressor) compressString(s string, stats *compressStats) string {
	// Keep short strings
	if len(s) <= c.MaxValueLength {
		stats.preserved = append(stats.preserved, "short string")
		return s
	}

	// Keep high-entropy strings (likely IDs, hashes, UUIDs)
	if c.isHighEntropy(s) {
		stats.preserved = append(stats.preserved, "high-entropy value")
		return s
	}

	// Check for common patterns to preserve
	if c.isIdentifier(s) {
		stats.preserved = append(stats.preserved, "identifier")
		return s
	}

	// Truncate long strings on a rune boundary so multibyte runes aren't split.
	truncated := textutil.TruncateBytes(s, c.MaxValueLength) + "..."
	stats.compressed = append(stats.compressed, "long string")
	return truncated
}

// isHighEntropy checks if a string has high information density (UUID, hash, etc.)
func (c *JSONCompressor) isHighEntropy(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}

	// Count unique characters
	unique := make(map[rune]bool)
	for _, r := range s {
		unique[r] = true
	}

	// High entropy strings (UUIDs, hashes) typically have many unique chars
	// Require at least 8 unique chars for strings longer than 16
	if len(s) > 16 && len(unique) < 8 {
		return false
	}

	// Require at least 4 unique chars for shorter strings
	if len(unique) < 4 {
		return false
	}

	entropy := c.calculateEntropy(s)
	return entropy >= c.EntropyThreshold
}

// calculateEntropy computes Shannon entropy normalized to 0-1.
func (c *JSONCompressor) calculateEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}

	// Count character frequencies
	freq := make(map[rune]int)
	for _, r := range s {
		freq[r]++
	}

	// Calculate entropy
	length := float64(len(s))
	var entropy float64
	for _, count := range freq {
		p := float64(count) / length
		entropy -= p * math.Log2(p)
	}

	// Normalize to 0-1 (max entropy for printable ASCII is ~6.5 bits)
	maxEntropy := math.Log2(float64(len(freq)))
	if maxEntropy == 0 {
		return 0
	}
	return entropy / maxEntropy
}

// isIdentifier checks if a string looks like a code identifier.
//
// One branch per recognized shape (UUID / URL / path / char-diversity /
// alphanumeric ratio): CCN intentionally exceeds 10 (see ADR 0016). The
// cascade of independent pattern checks is the spec.
func (c *JSONCompressor) isIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}

	// Check for common identifier patterns
	// - camelCase, PascalCase, snake_case, kebab-case
	// - paths like /api/users/123
	// - URLs
	// - UUIDs

	// UUID pattern (loose check)
	if len(s) == 36 && strings.Count(s, "-") == 4 {
		return true
	}

	// URL or path
	if strings.HasPrefix(s, "http") || strings.HasPrefix(s, "/") {
		return true
	}

	// Check if it's mostly alphanumeric with underscores/hyphens
	alphaCount := 0
	uniqueChars := make(map[rune]bool)
	for _, r := range s {
		uniqueChars[r] = true
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			alphaCount++
		}
	}

	// Reject strings with very low character diversity (like "aaaaaaa")
	// An identifier should have reasonable variety
	if len(uniqueChars) < 4 && len(s) > 10 {
		return false
	}

	// If >90% alphanumeric-ish, treat as identifier
	return float64(alphaCount)/float64(len(s)) > 0.9
}
