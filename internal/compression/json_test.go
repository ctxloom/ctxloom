package compression

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	result, err := c.Compress(ctx, input, 0.5)
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

	result, err := c.Compress(ctx, input, 0.5)
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

	result, err := c.Compress(ctx, input, 0.5)
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
		name        string
		value       string
		shouldKeep  bool
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
			result, err := c.Compress(ctx, input, 0.5)
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

	result, err := c.Compress(ctx, input, 0.5)
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
		{"aaaaaaaaaaaaa", false},                                  // Low entropy - single repeated char
		{"aaaaabbbbb", false},                                     // Low entropy - two repeated chars
		{"abcdefghijklmnop", true},                                // High entropy - varied chars
		{"550e8400-e29b-41d4-a716-446655440000", true},           // UUID
		{"x7Kj2mNpQrStUvWx", true},                               // Random-looking
	}

	for _, tt := range tests {
		t.Run(tt.input[:min(20, len(tt.input))], func(t *testing.T) {
			result := c.isHighEntropy(tt.input)
			assert.Equal(t, tt.highEntr, result,
				"isHighEntropy(%q) = %v, want %v", tt.input, result, tt.highEntr)
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
