package compression

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONCompressor_InvalidJSONDegradesToVerbatim(t *testing.T) {
	c := NewJSONCompressor()
	input := `{not valid json,,,`

	result, err := c.Compress(context.Background(), ContentTypeJSON, input)
	require.NoError(t, err, "invalid JSON must degrade, not error")
	assert.Equal(t, input, result.Content, "content should pass through verbatim")
	assert.Equal(t, 1.0, result.Ratio)
}

// TestJSONCompressor_TrailingDataDegradesToVerbatim pins that multi-value
// (NDJSON/JSONL) and value-plus-trailing-garbage inputs degrade to verbatim
// pass-through instead of being silently truncated to their first record.
func TestJSONCompressor_TrailingDataDegradesToVerbatim(t *testing.T) {
	c := NewJSONCompressor()
	cases := map[string]string{
		"ndjson":          "{\"a\":1}\n{\"b\":2}\n{\"c\":3}",
		"trailing_object": `{"a":1} {"b":2}`,
		"trailing_array":  `[1,2,3] [4,5,6]`,
		"trailing_junk":   `{"a":1} garbage`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := c.Compress(context.Background(), ContentTypeJSON, input)
			require.NoError(t, err)
			assert.Equal(t, input, result.Content, "multi-value/trailing-data input must pass through verbatim, not be truncated")
			assert.Equal(t, 1.0, result.Ratio)
		})
	}
}

// TestJSONCompressor_TrailingWhitespaceStillCompresses pins that a single value
// with only trailing whitespace is NOT treated as trailing data.
func TestJSONCompressor_TrailingWhitespaceStillCompresses(t *testing.T) {
	c := NewJSONCompressor()
	input := "{\"a\":1}\n\n  "
	result, err := c.Compress(context.Background(), ContentTypeJSON, input)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &parsed))
	assert.Contains(t, parsed, "a")
}

func TestJSONCompressor_Basic(t *testing.T) {
	c := NewJSONCompressor()
	ctx := context.Background()

	input := `{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "John Doe",
  "description": "This is a very long description that contains a lot of text that is not really necessary for understanding the structure of the data. It goes on and on with various details that could easily be summarized.",
  "email": "john@example.com",
  "created_at": "2024-01-15T10:30:00Z"
}`

	result, err := c.Compress(ctx, ContentTypeJSON, input)
	require.NoError(t, err)

	// Should be valid JSON
	var parsed map[string]any
	err = json.Unmarshal([]byte(result.Content), &parsed)
	require.NoError(t, err)

	// Should preserve all keys
	assert.Contains(t, parsed, "id")
	assert.Contains(t, parsed, "name")
	assert.Contains(t, parsed, "description")
	assert.Contains(t, parsed, "email")
	assert.Contains(t, parsed, "created_at")

	// UUID should be preserved (high entropy)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", parsed["id"])

	// Short values preserved
	assert.Equal(t, "John Doe", parsed["name"])
	assert.Equal(t, "john@example.com", parsed["email"])

	// Long description should be truncated
	desc, ok := parsed["description"].(string)
	require.True(t, ok)
	assert.True(t, len(desc) < 100, "Description should be truncated")
	assert.Contains(t, desc, "...")

	t.Logf("Compression ratio: %.2f%%", result.Ratio*100)
	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestJSONCompressor_Array(t *testing.T) {
	c := NewJSONCompressor()
	c.MaxArrayItems = 2
	ctx := context.Background()

	input := `{
  "users": [
    {"id": 1, "name": "Alice"},
    {"id": 2, "name": "Bob"},
    {"id": 3, "name": "Charlie"},
    {"id": 4, "name": "Diana"},
    {"id": 5, "name": "Eve"}
  ]
}`

	result, err := c.Compress(ctx, ContentTypeJSON, input)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal([]byte(result.Content), &parsed)
	require.NoError(t, err)

	users, ok := parsed["users"].([]any)
	require.True(t, ok)

	// Should keep first 2 items + summary
	assert.Len(t, users, 3)

	// First two should be objects
	user1, ok := users[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), user1["id"])
	assert.Equal(t, "Alice", user1["name"])

	// Last should be summary string
	summary, ok := users[2].(string)
	require.True(t, ok)
	assert.Contains(t, summary, "3 more items")

	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestJSONCompressor_NestedObjects(t *testing.T) {
	c := NewJSONCompressor()
	ctx := context.Background()

	input := `{
  "user": {
    "id": "uuid-12345678-abcd",
    "profile": {
      "name": "John",
      "bio": "This is a very long biography that contains extensive details about the person's life, career, achievements, and interests. It spans multiple sentences and paragraphs.",
      "avatar_url": "https://example.com/avatars/john.png"
    },
    "settings": {
      "theme": "dark",
      "notifications": true
    }
  }
}`

	result, err := c.Compress(ctx, ContentTypeJSON, input)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal([]byte(result.Content), &parsed)
	require.NoError(t, err)

	// Navigate nested structure
	user := parsed["user"].(map[string]any)
	profile := user["profile"].(map[string]any)

	// URL should be preserved (identifier-like)
	assert.Contains(t, profile["avatar_url"], "https://")

	// Bio should be truncated
	bio := profile["bio"].(string)
	assert.True(t, len(bio) < 50 || bio[len(bio)-3:] == "...")

	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestJSONCompressor_HighEntropy(t *testing.T) {
	c := NewJSONCompressor()
	ctx := context.Background()

	tests := []struct {
		name       string
		value      string
		shouldKeep bool
	}{
		{"UUID", "550e8400-e29b-41d4-a716-446655440000", true},
		{"hash", "a1b2c3d4e5f6789012345678901234567890abcd", true},
		{"API key pattern", "xk_test_fake1234567890abcdefgh", true},
		{"short id", "abc123", true},
		{"long prose", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false}, // Repeated char = low entropy
		{"URL", "https://api.example.com/v1/users", true},
		{"path", "/api/users/123/profile", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `{"value": "` + tt.value + `"}`
			result, err := c.Compress(ctx, ContentTypeJSON, input)
			require.NoError(t, err)

			var parsed map[string]any
			err = json.Unmarshal([]byte(result.Content), &parsed)
			require.NoError(t, err)

			if tt.shouldKeep {
				assert.Equal(t, tt.value, parsed["value"],
					"High-entropy/identifier value should be preserved")
			} else {
				val := parsed["value"].(string)
				if len(tt.value) > c.MaxValueLength {
					assert.Contains(t, val, "...",
						"Long low-entropy value should be truncated")
				}
			}
		})
	}
}

func TestJSONCompressor_PreservesStructure(t *testing.T) {
	c := NewJSONCompressor()
	ctx := context.Background()

	// API response with various types
	input := `{
  "status": "success",
  "code": 200,
  "data": {
    "items": [
      {"id": 1, "active": true},
      {"id": 2, "active": false}
    ],
    "pagination": {
      "page": 1,
      "total": 100,
      "has_more": true
    }
  },
  "meta": null
}`

	result, err := c.Compress(ctx, ContentTypeJSON, input)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal([]byte(result.Content), &parsed)
	require.NoError(t, err)

	// Verify structure preserved
	assert.Equal(t, "success", parsed["status"])
	assert.Equal(t, float64(200), parsed["code"])
	assert.Nil(t, parsed["meta"])

	data := parsed["data"].(map[string]any)
	pagination := data["pagination"].(map[string]any)
	assert.Equal(t, true, pagination["has_more"])

	t.Logf("\n--- Compressed output ---\n%s", result.Content)
}

func TestJSONCompressor_Entropy(t *testing.T) {
	c := NewJSONCompressor()

	tests := []struct {
		input    string
		highEntr bool
	}{
		{"aaaaaaaaaaaaa", false},                       // Low entropy - single repeated char
		{"aaaaabbbbb", false},                          // Low entropy - two repeated chars
		{"abcdefghijklmnop", true},                     // High entropy - varied chars
		{"550e8400-e29b-41d4-a716-446655440000", true}, // UUID
		{"x7Kj2mNpQrStUvWx", true},                     // Random-looking
	}

	for _, tt := range tests {
		t.Run(tt.input[:min(20, len(tt.input))], func(t *testing.T) {
			result := c.isHighEntropy(tt.input)
			assert.Equal(t, tt.highEntr, result,
				"isHighEntropy(%q) = %v, want %v", tt.input, result, tt.highEntr)
		})
	}
}

// U046-F10: calculateEntropy counted frequencies per RUNE but divided by BYTE
// length, so for any multibyte string the probabilities summed to less than 1
// and the entropy came out understated by exactly the bytes-per-rune factor.
// A maximally-varied non-ASCII value (every rune distinct — entropy 1.0 by
// construction) was therefore classified low-entropy and TRUNCATED, while its
// ASCII twin was preserved.
func TestJSONCompressor_EntropyIsPerRuneNotPerByte(t *testing.T) {
	c := NewJSONCompressor()
	// 20 distinct runes, 40 bytes: every rune unique, so normalized Shannon
	// entropy is 1.0 whatever the alphabet.
	const greek = "αβγδεζηθικλμνξοπρστυ"
	const ascii = "abcdefghijklmnopqrst"

	assert.InDelta(t, 1.0, c.calculateEntropy(ascii), 1e-9, "all-distinct ASCII is maximum entropy")
	assert.InDelta(t, 1.0, c.calculateEntropy(greek), 1e-9, "all-distinct non-ASCII is maximum entropy too")
	assert.True(t, c.isHighEntropy(greek), "a maximally-varied non-ASCII value is high entropy")

	result, err := c.Compress(context.Background(), ContentTypeJSON, `{"token":"`+greek+`"}`)
	require.NoError(t, err)
	assert.Contains(t, result.Content, greek, "a high-entropy non-ASCII value is preserved, not truncated")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestJSONCompressor_PreservesBigIntegers pins the UseNumber decode: numbers
// are always preserved for IDs, and IDs beyond float64's 2^53 mantissa
// (snowflake IDs) were re-marshaled in scientific notation through the
// float64 round-trip.
func TestJSONCompressor_PreservesBigIntegers(t *testing.T) {
	c := NewJSONCompressor()
	input := `{"id": 9223372036854775807}`

	result, err := c.Compress(context.Background(), ContentTypeJSON, input)
	require.NoError(t, err)
	assert.Contains(t, result.Content, "9223372036854775807",
		"big integer IDs must round-trip verbatim, not as float64 scientific notation")
}

// U046-F07 called the key reordering and duplicate-key collapse a correctness
// defect. Both are properties of decoding JSON into Go values, and both are
// what RFC 8259 licenses: an object is an UNORDERED collection, and a repeated
// name is undefined behaviour that encoding/json resolves last-wins. What would
// be a defect is losing a key, which is what this pins alongside the two
// documented behaviours — so a future ordered-map representation is a
// deliberate change, not an accident, and the "reordering is data loss" reading
// cannot be re-applied without this test disagreeing.
func TestJSONCompressor_KeyOrderIsNotSemantic(t *testing.T) {
	c := NewJSONCompressor()

	result, err := c.Compress(context.Background(), ContentTypeJSON, `{"zulu":1,"alpha":2,"mike":3}`)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"alpha\": 2,\n  \"mike\": 3,\n  \"zulu\": 1\n}", result.Content,
		"keys are re-emitted in sorted order — every one of them, none dropped")

	dup, err := c.Compress(context.Background(), ContentTypeJSON, `{"a":1,"a":2}`)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(dup.Content), &decoded))
	assert.Equal(t, float64(2), decoded["a"], "a duplicate name resolves last-wins, as encoding/json does")
}
