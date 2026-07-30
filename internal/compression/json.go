package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"

	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// JSONCompressor compresses JSON while preserving structure (keys, types).
// It truncates long string values while keeping high-entropy identifiers.
//
// Structure, not layout: the document is decoded into Go values and re-encoded,
// so object keys come back in SORTED order and a duplicate key resolves to its
// last occurrence — RFC 8259's own reading, objects being unordered and
// duplicate names undefined. Every key survives; only their order does not.
type JSONCompressor struct {
	// MaxValueLength is the maximum length for string values before truncation.
	MaxValueLength int

	// MaxArrayItems is the maximum number of array items to keep fully.
	MaxArrayItems int

	// EntropyThreshold determines what counts as a high-entropy value (0.0-1.0).
	// High-entropy values (UUIDs, hashes) are preserved.
	EntropyThreshold float64
}

// NewJSONCompressor creates a JSON compressor with default settings.
func NewJSONCompressor() *JSONCompressor {
	return &JSONCompressor{
		MaxValueLength:   30,
		MaxArrayItems:    3,
		EntropyThreshold: 0.75,
	}
}

// CanHandle returns true for JSON content types.
func (c *JSONCompressor) CanHandle(ct ContentType) bool {
	return ct == ContentTypeJSON
}

// Compress reduces JSON size while preserving structure.
func (c *JSONCompressor) Compress(ctx context.Context, _ ContentType, content string) (Result, error) {
	// Parse JSON with UseNumber so integers beyond float64's 2^53 mantissa
	// (snowflake IDs, hashes-as-ints) round-trip verbatim instead of being
	// re-marshaled in scientific notation — numbers are always preserved, for
	// IDs. Fault tolerance: invalid JSON degrades to verbatim pass-through
	// rather than failing the caller.
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return verbatimResult(content, "json-compressor"), nil
	}

	// json.Decoder.Decode reads exactly one value and does NOT error on
	// trailing data, so NDJSON/JSONL or a value followed by garbage would
	// otherwise be silently truncated to its first record. Require the stream
	// to be exhausted (only trailing whitespace, i.e. io.EOF); anything else
	// degrades to verbatim pass-through rather than dropping content.
	if _, err := dec.Token(); err != io.EOF {
		return verbatimResult(content, "json-compressor"), nil
	}

	// Compress the structure
	compressed := c.compressValue(data, 0)

	// Marshal back to JSON. A marshal failure on already-parsed data is not
	// expected, but degrade to verbatim rather than erroring out.
	output, err := json.MarshalIndent(compressed, "", "  ")
	if err != nil {
		return verbatimResult(content, "json-compressor"), nil
	}

	return Result{
		Content: string(output),
		Ratio:   float64(len(output)) / float64(len(content)),
		ModelID: "json-compressor",
	}, nil
}

func (c *JSONCompressor) compressValue(v any, depth int) any {
	switch val := v.(type) {
	case map[string]any:
		return c.compressObject(val, depth)

	case []any:
		return c.compressArray(val, depth)

	case string:
		return c.compressString(val)

	default:
		return val
	}
}

func (c *JSONCompressor) compressObject(obj map[string]any, depth int) map[string]any {
	result := make(map[string]any)

	for key, value := range obj {
		result[key] = c.compressValue(value, depth+1)
	}

	return result
}

func (c *JSONCompressor) compressArray(arr []any, depth int) []any {
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
		result = append(result, c.compressValue(arr[i], depth+1))
	}

	// If there are more items, add a summary
	if len(arr) > keepCount {
		remaining := len(arr) - keepCount
		result = append(result, fmt.Sprintf("... %d more items", remaining))
	}

	return result
}

func (c *JSONCompressor) compressString(s string) string {
	// An unset budget means "do not truncate", never "delete". At or below
	// zero every guard below is bypassed and the value is handed to Ellipsize
	// with no room at all, which yields the empty string — the document keeps
	// its shape, the compressor reports success, and the payload is gone.
	if c.MaxValueLength <= 0 {
		return s
	}

	// Keep short strings
	if len(s) <= c.MaxValueLength {
		return s
	}

	// Keep high-entropy strings (likely IDs, hashes, UUIDs)
	if c.isHighEntropy(s) {
		return s
	}

	// Check for common patterns to preserve
	if c.isIdentifier(s) {
		return s
	}

	// Truncate long strings on a rune boundary so multibyte runes aren't split.
	// Ellipsize reserves the ellipsis from MaxValueLength rather than
	// appending it on top, so the result honours the maximum the field
	// documents instead of exceeding it by three bytes.
	return textutil.Ellipsize(s, c.MaxValueLength)
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

	// Count character frequencies. The divisor is the RUNE count the loop
	// actually counted, not the byte length: dividing rune frequencies by bytes
	// makes the probabilities sum to less than 1 for any multibyte string, and
	// understates its entropy by the bytes-per-rune factor.
	freq := make(map[rune]int)
	total := 0
	for _, r := range s {
		freq[r]++
		total++
	}

	// Calculate entropy
	length := float64(total)
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

// isIdentifier checks if a string looks like a code identifier:
// camelCase/snake_case/kebab-case names, paths, URLs, or UUIDs.
func (c *JSONCompressor) isIdentifier(s string) bool {
	if len(s) == 0 {
		return false
	}

	// UUID pattern (loose check)
	if len(s) == 36 && strings.Count(s, "-") == 4 {
		return true
	}

	// URL or path
	if strings.HasPrefix(s, "http") || strings.HasPrefix(s, "/") {
		return true
	}

	alphaCount, uniqueCount := identCharStats(s)

	// Reject strings with very low character diversity (like "aaaaaaa").
	// An identifier should have reasonable variety.
	if uniqueCount < 4 && len(s) > 10 {
		return false
	}

	// If >90% alphanumeric-ish, treat as identifier
	return float64(alphaCount)/float64(len(s)) > 0.9
}

// identCharStats counts identifier-like characters and distinct runes in s.
func identCharStats(s string) (alphaCount, uniqueCount int) {
	uniqueChars := make(map[rune]bool)
	for _, r := range s {
		uniqueChars[r] = true
		if isIdentRune(r) {
			alphaCount++
		}
	}
	return alphaCount, len(uniqueChars)
}

// isIdentRune reports whether r is allowed in an identifier-shaped string.
func isIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
}
