package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ==========================================================================
// Frontmatter parsing tests
// ==========================================================================

func TestParseMarkdownFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantDesc string
		wantBody string
	}{
		{
			name:     "with frontmatter",
			input:    "---\ndescription: Test command\n---\nBody content",
			wantDesc: "Test command",
			wantBody: "Body content",
		},
		{
			name:     "with quoted description",
			input:    "---\ndescription: \"Quoted description\"\n---\nBody",
			wantDesc: "Quoted description",
			wantBody: "Body",
		},
		{
			name:     "no frontmatter",
			input:    "Just body content",
			wantDesc: "",
			wantBody: "Just body content",
		},
		{
			name:     "empty frontmatter",
			input:    "---\n\n---\nBody only",
			wantDesc: "",
			wantBody: "Body only",
		},
		{
			name:     "frontmatter with other fields",
			input:    "---\ntitle: Foo\ndescription: The desc\nother: bar\n---\nContent here",
			wantDesc: "The desc",
			wantBody: "Content here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, body := parseMarkdownFrontmatter(tt.input)
			assert.Equal(t, tt.wantDesc, desc, "description mismatch")
			assert.Equal(t, tt.wantBody, body, "body mismatch")
		})
	}
}
