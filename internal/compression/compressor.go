// Package compression provides content-aware compression for LLM context optimization.
// It uses structural analysis (AST for code, schema for JSON) to preserve important
// elements while removing redundant content.
package compression

import "context"

// Compressor compresses content while preserving semantic structure.
type Compressor interface {
	// Compress reduces content size while preserving important information.
	// The ratio parameter (0.0-1.0) suggests target size as fraction of original.
	Compress(ctx context.Context, content string, ratio float64) (Result, error)

	// CanHandle returns true if this compressor can process the given content type.
	CanHandle(contentType ContentType) bool
}

// ContentType identifies the type of content for routing to appropriate compressor.
type ContentType string

const (
	ContentTypeGo         ContentType = "go"
	ContentTypePython     ContentType = "python"
	ContentTypeJavaScript ContentType = "javascript"
	ContentTypeTypeScript ContentType = "typescript"
	ContentTypeRust       ContentType = "rust"
	ContentTypeJava       ContentType = "java"
	ContentTypeJSON       ContentType = "json"
	ContentTypeYAML       ContentType = "yaml"
	ContentTypeMarkdown   ContentType = "markdown"
	ContentTypeText       ContentType = "text"
	ContentTypeUnknown    ContentType = "unknown"
)

// Result contains the compressed content and metadata.
type Result struct {
	// Content is the compressed output.
	Content string

	// OriginalSize is the byte length of the original content.
	OriginalSize int

	// CompressedSize is the byte length of the compressed content.
	CompressedSize int

	// Ratio is the actual compression ratio achieved (compressed/original).
	Ratio float64

	// PreservedElements lists what structural elements were kept.
	PreservedElements []string

	// CompressedElements lists what was elided or summarized.
	CompressedElements []string

	// ModelID identifies which compressor/model was used (e.g., "ast:go", "claude-3-sonnet").
	ModelID string
}

// verbatimResult returns content uncompressed (Ratio 1.0) tagged with modelID.
// Compressors use it to degrade gracefully: when input can't be parsed, the
// content passes through unchanged rather than failing the whole pipeline.
func verbatimResult(content, modelID string) Result {
	return Result{
		Content:        content,
		OriginalSize:   len(content),
		CompressedSize: len(content),
		Ratio:          1.0,
		ModelID:        modelID,
	}
}

// DetectContentType determines the content type from file extension or content analysis.
//
// One branch per file type / sniffed prefix: CCN intentionally exceeds 10 (see
// ADR 0016). The cascade of cases is the spec; a lookup table buys nothing.
func DetectContentType(filename string, content string) ContentType {
	// Check by extension first
	switch {
	case hasExtension(filename, ".go"):
		return ContentTypeGo
	case hasExtension(filename, ".py"):
		return ContentTypePython
	case hasExtension(filename, ".js", ".mjs", ".cjs"):
		return ContentTypeJavaScript
	case hasExtension(filename, ".ts", ".tsx"):
		return ContentTypeTypeScript
	case hasExtension(filename, ".rs"):
		return ContentTypeRust
	case hasExtension(filename, ".java"):
		return ContentTypeJava
	case hasExtension(filename, ".json"):
		return ContentTypeJSON
	case hasExtension(filename, ".yaml", ".yml"):
		return ContentTypeYAML
	case hasExtension(filename, ".md", ".markdown"):
		return ContentTypeMarkdown
	}

	// Heuristic detection from content
	if len(content) > 0 {
		if content[0] == '{' || content[0] == '[' {
			return ContentTypeJSON
		}
		if len(content) > 3 && content[:3] == "---" {
			return ContentTypeYAML
		}
		if len(content) > 7 && content[:7] == "package" {
			return ContentTypeGo
		}
	}

	return ContentTypeUnknown
}

func hasExtension(filename string, exts ...string) bool {
	for _, ext := range exts {
		if len(filename) > len(ext) && filename[len(filename)-len(ext):] == ext {
			return true
		}
	}
	return false
}
