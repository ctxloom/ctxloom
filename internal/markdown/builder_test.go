package markdown

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	b := New()
	assert.NotNil(t, b)
	assert.Empty(t, b.sections)
}

func TestBuilder_H1(t *testing.T) {
	b := New().H1("Title")
	result := b.String()
	assert.Contains(t, result, "# Title")
}

func TestBuilder_H2(t *testing.T) {
	b := New().H2("Subtitle")
	result := b.String()
	assert.Contains(t, result, "## Subtitle")
}

func TestBuilder_H3(t *testing.T) {
	b := New().H3("Section")
	result := b.String()
	assert.Contains(t, result, "### Section")
}

func TestBuilder_P(t *testing.T) {
	t.Run("adds paragraph to section", func(t *testing.T) {
		b := New().H1("Title").P("Paragraph text")
		result := b.String()
		assert.Contains(t, result, "Paragraph text")
	})

	t.Run("creates section if none exists", func(t *testing.T) {
		b := New().P("Just a paragraph")
		result := b.String()
		assert.Contains(t, result, "Just a paragraph")
	})
}

func TestBuilder_Text(t *testing.T) {
	b := New().Text("Some text")
	result := b.String()
	assert.Contains(t, result, "Some text")
}

func TestBuilder_Bullet(t *testing.T) {
	b := New().Bullet("Item one").Bullet("Item two")
	result := b.String()
	assert.Contains(t, result, "- Item one")
	assert.Contains(t, result, "- Item two")
}

func TestBuilder_BulletBold(t *testing.T) {
	b := New().BulletBold("Label", "value")
	result := b.String()
	assert.Contains(t, result, "- **Label**: value")
}

func TestBuilder_CodeBlock(t *testing.T) {
	b := New().CodeBlock("go", "fmt.Println(\"hello\")")
	result := b.String()
	assert.Contains(t, result, "```go")
	assert.Contains(t, result, "fmt.Println(\"hello\")")
	assert.Contains(t, result, "```")
}

func TestBuilder_String(t *testing.T) {
	t.Run("combines multiple sections", func(t *testing.T) {
		b := New().
			H1("Title").
			P("Introduction").
			H2("Details").
			P("More info")
		result := b.String()

		// Check order is preserved
		titleIdx := strings.Index(result, "# Title")
		introIdx := strings.Index(result, "Introduction")
		detailsIdx := strings.Index(result, "## Details")
		moreIdx := strings.Index(result, "More info")

		assert.True(t, titleIdx < introIdx)
		assert.True(t, introIdx < detailsIdx)
		assert.True(t, detailsIdx < moreIdx)
	})

	t.Run("ends with newline", func(t *testing.T) {
		b := New().P("Text")
		result := b.String()
		assert.True(t, strings.HasSuffix(result, "\n"))
	})
}

func TestBold(t *testing.T) {
	assert.Equal(t, "**text**", Bold("text"))
}

func TestItalic(t *testing.T) {
	assert.Equal(t, "*text*", Italic("text"))
}

func TestCode(t *testing.T) {
	assert.Equal(t, "`text`", Code("text"))
}

func TestLink(t *testing.T) {
	assert.Equal(t, "[Click here](https://example.com)", Link("Click here", "https://example.com"))
}

func TestNewFragment(t *testing.T) {
	f := NewFragment()
	assert.NotNil(t, f)
	assert.NotNil(t, f.context)
	assert.NotNil(t, f.variables)
}

func TestFragmentBuilder_Context(t *testing.T) {
	f := NewFragment()
	ctx := f.Context()
	assert.NotNil(t, ctx)
	assert.Equal(t, f.context, ctx)
}

func TestFragmentBuilder_SetVar(t *testing.T) {
	f := NewFragment().SetVar("key", "value")
	assert.Equal(t, "value", f.variables["key"])
}

func TestFragmentBuilder_SetVars(t *testing.T) {
	vars := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}
	f := NewFragment().SetVars(vars)
	assert.Equal(t, "value1", f.variables["key1"])
	assert.Equal(t, "value2", f.variables["key2"])
}

func TestFragmentBuilder_String(t *testing.T) {
	t.Run("with content only", func(t *testing.T) {
		f := NewFragment()
		f.Context().P("Some content")
		result := f.String()
		assert.Contains(t, result, "content: |")
		assert.Contains(t, result, "Some content")
	})

	t.Run("with variables", func(t *testing.T) {
		f := NewFragment().SetVar("name", "test")
		f.Context().P("Content here")
		result := f.String()
		assert.Contains(t, result, "variables:")
		assert.Contains(t, result, "- name")
		assert.Contains(t, result, "exports:")
		assert.Contains(t, result, "name: test")
	})

	t.Run("ends with newline", func(t *testing.T) {
		f := NewFragment()
		f.Context().P("Text")
		result := f.String()
		assert.True(t, strings.HasSuffix(result, "\n"))
	})
}

func TestBuilder_Chaining(t *testing.T) {
	// Test that all methods return the builder for chaining
	b := New().
		H1("Title").
		H2("Subtitle").
		H3("Section").
		P("Paragraph").
		Text("Text").
		Bullet("Item").
		BulletBold("Key", "Value").
		CodeBlock("go", "code")

	result := b.String()
	assert.Contains(t, result, "# Title")
	assert.Contains(t, result, "## Subtitle")
	assert.Contains(t, result, "### Section")
	assert.Contains(t, result, "Paragraph")
	assert.Contains(t, result, "Text")
	assert.Contains(t, result, "- Item")
	assert.Contains(t, result, "- **Key**: Value")
	assert.Contains(t, result, "```go")
}
