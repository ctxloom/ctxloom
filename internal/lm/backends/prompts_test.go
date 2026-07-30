package backends

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/resources"
)

// ==========================================================================
// Frontmatter parsing tests
// ==========================================================================

func TestSplitCommandFrontmatter(t *testing.T) {
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
			desc, body := resources.SplitCommandFrontmatter(tt.input)
			assert.Equal(t, tt.wantDesc, desc, "description mismatch")
			assert.Equal(t, tt.wantBody, body, "body mismatch")
		})
	}
}

// TestBuiltinCommandFrontmatterParity pins the two public seams above the
// duplicated frontmatter parser against each other, BEFORE the two copies were
// collapsed. resources.GetBuiltinCommandBody strips frontmatter with
// resources.SplitCommandFrontmatter; builtinCommands used to strip it with a
// hand-maintained copy of that parser. The two parsed the SAME embedded files
// for the SAME key, so any divergence shows up as a builtin command whose
// exported body or description differs depending on which door it came
// through. The pin sits at the seams rather than on either implementation, so
// it is unchanged by the collapse.
func TestBuiltinCommandFrontmatterParity(t *testing.T) {
	names, err := resources.ListBuiltinCommands()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	loaded := builtinCommands()
	require.Len(t, loaded, len(names))

	byName := map[string]*bundles.LoadedContent{}
	for _, c := range loaded {
		byName[c.Name] = c
	}

	for _, name := range names {
		c, ok := byName[name]
		require.Truef(t, ok, "builtinCommands() dropped %q", name)

		wantBody, err := resources.GetBuiltinCommandBody(name)
		require.NoError(t, err)
		assert.Equalf(t, wantBody, c.Content, "body for %q differs between the two frontmatter parsers", name)
		assert.NotEmptyf(t, c.Content, "builtin command %q exported an empty body", name)

		raw, err := resources.GetBuiltinCommand(name)
		require.NoError(t, err)
		if strings.HasPrefix(string(raw), "---\n") {
			assert.NotEmptyf(t, c.LLM.ClaudeCode.Description, "builtin command %q has frontmatter but no parsed description", name)
		}
	}
}
