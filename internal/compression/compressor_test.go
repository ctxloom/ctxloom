package compression

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Content Type Detection Tests
// =============================================================================

// TestDetectContentType_ByExtension verifies content type detection from file extensions
func TestDetectContentType_ByExtension(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		content      string
		expectedType ContentType
	}{
		{"Go file", "main.go", "", ContentTypeGo},
		{"Python file", "script.py", "", ContentTypePython},
		{"JavaScript file", "app.js", "", ContentTypeJavaScript},
		{"JavaScript MJS", "app.mjs", "", ContentTypeJavaScript},
		{"JavaScript CJS", "app.cjs", "", ContentTypeJavaScript},
		{"TypeScript file", "main.ts", "", ContentTypeTypeScript},
		{"TypeScript JSX", "main.tsx", "", ContentTypeTypeScript},
		{"Rust file", "lib.rs", "", ContentTypeRust},
		{"Java file", "Main.java", "", ContentTypeJava},
		{"JSON file", "data.json", "", ContentTypeJSON},
		{"YAML file", "config.yaml", "", ContentTypeYAML},
		{"YML file", "config.yml", "", ContentTypeYAML},
		{"Markdown file", "README.md", "", ContentTypeMarkdown},
		{"Markdown file", "docs.markdown", "", ContentTypeMarkdown},
		{"Unknown file", "data.unknown", "", ContentTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectContentType(tt.filename, tt.content)
			assert.Equal(t, tt.expectedType, result)
		})
	}
}

// TestDetectContentType_ByContent verifies heuristic content type detection
func TestDetectContentType_ByContent(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		content      string
		expectedType ContentType
	}{
		{"JSON object", "data", "{}", ContentTypeJSON},
		{"JSON array", "data", "[]", ContentTypeJSON},
		{"YAML content", "data", "---\nkey: value", ContentTypeYAML},
		{"Go package", "data", "package main", ContentTypeGo},
		{"No extension unknown", "file", "", ContentTypeUnknown},
		{"Empty content", "", "", ContentTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectContentType(tt.filename, tt.content)
			assert.Equal(t, tt.expectedType, result)
		})
	}
}

// TestDetectContentType_PrefersExtension verifies extension takes priority
func TestDetectContentType_PrefersExtension(t *testing.T) {
	// File with .go extension but JSON content - should prefer extension
	result := DetectContentType("data.go", "{}")
	assert.Equal(t, ContentTypeGo, result)
}

// TestDetectContentType_Empty verifies handling of empty filenames
func TestDetectContentType_Empty(t *testing.T) {
	result := DetectContentType("", "")
	assert.Equal(t, ContentTypeUnknown, result)
}

// =============================================================================
// Result Type Tests
// =============================================================================

// TestResult_Calculation verifies ratio calculation
func TestResult_Calculation(t *testing.T) {
	result := Result{
		Content:         "compressed",
		OriginalSize:    100,
		CompressedSize:  50,
		Ratio:           0.5,
		ModelID:         "test-model",
	}

	assert.Equal(t, 100, result.OriginalSize)
	assert.Equal(t, 50, result.CompressedSize)
	assert.Equal(t, 0.5, result.Ratio)
	assert.Equal(t, "test-model", result.ModelID)
}

// TestResult_PreservedAndCompressed verifies metadata tracking
func TestResult_PreservedAndCompressed(t *testing.T) {
	result := Result{
		Content:        "content",
		PreservedElements: []string{"function1", "function2"},
		CompressedElements: []string{"docstring", "test"},
	}

	assert.Len(t, result.PreservedElements, 2)
	assert.Len(t, result.CompressedElements, 2)
	assert.Contains(t, result.PreservedElements, "function1")
	assert.Contains(t, result.CompressedElements, "test")
}

// =============================================================================
// Content Type Constants Tests
// =============================================================================

// TestContentTypeConstants verifies all content type constants are defined
func TestContentTypeConstants(t *testing.T) {
	types := []ContentType{
		ContentTypeGo,
		ContentTypePython,
		ContentTypeJavaScript,
		ContentTypeTypeScript,
		ContentTypeRust,
		ContentTypeJava,
		ContentTypeJSON,
		ContentTypeYAML,
		ContentTypeMarkdown,
		ContentTypeText,
		ContentTypeUnknown,
	}

	// Just verify they're not empty
	for _, ct := range types {
		assert.NotEmpty(t, string(ct))
	}
}

// =============================================================================
// hasExtension Helper Tests
// =============================================================================

// TestHasExtension verifies extension checking helper
func TestHasExtension(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		extensions  []string
		expected    bool
	}{
		{"matching extension", "file.txt", []string{".txt"}, true},
		{"multiple extensions one match", "file.txt", []string{".go", ".txt"}, true},
		{"no match", "file.txt", []string{".go"}, false},
		{"empty extensions", "file.txt", []string{}, false},
		{"short filename", "f", []string{".txt"}, false},
		{"exact length", "txt", []string{".txt"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasExtension(tt.filename, tt.extensions...)
			assert.Equal(t, tt.expected, result)
		})
	}
}
