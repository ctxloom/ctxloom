// Package compression provides content-aware compression for LLM context optimization.
// It uses structural analysis (AST for code, schema for JSON) to preserve important
// elements while removing redundant content.
package compression

import (
	"context"
	"strings"
)

// Compressor compresses content while preserving semantic structure.
type Compressor interface {
	// Compress reduces content size while preserving important information.
	// contentType is the router's already-resolved type (extension-derived);
	// compressors must honor it rather than re-sniffing — a wrong re-sniff
	// parses content under the wrong grammar and silently loses it. The ratio
	// parameter (0.0-1.0) suggests target size as fraction of original.
	Compress(ctx context.Context, contentType ContentType, content string, ratio float64) (Result, error)

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
// extensionContentTypes maps file extensions to their content type, checked
// in order. One entry per recognized extension group.
var extensionContentTypes = []struct {
	exts []string
	ct   ContentType
}{
	{[]string{".go"}, ContentTypeGo},
	{[]string{".py"}, ContentTypePython},
	{[]string{".js", ".mjs", ".cjs"}, ContentTypeJavaScript},
	{[]string{".ts", ".tsx"}, ContentTypeTypeScript},
	{[]string{".rs"}, ContentTypeRust},
	{[]string{".java"}, ContentTypeJava},
	{[]string{".json"}, ContentTypeJSON},
	{[]string{".yaml", ".yml"}, ContentTypeYAML},
	{[]string{".md", ".markdown"}, ContentTypeMarkdown},
}

func DetectContentType(filename string, content string) ContentType {
	// Check by extension first
	for _, e := range extensionContentTypes {
		if hasExtension(filename, e.exts...) {
			return e.ct
		}
	}

	// Fall back to content sniffing
	if ct := sniffContentType(content); ct != ContentTypeUnknown {
		return ct
	}

	return ContentTypeUnknown
}

// sniffContentType guesses a content type from leading bytes when the
// filename extension is unrecognized.
func sniffContentType(content string) ContentType {
	if len(content) == 0 {
		return ContentTypeUnknown
	}
	if content[0] == '{' || content[0] == '[' {
		return ContentTypeJSON
	}
	if len(content) > 3 && content[:3] == "---" {
		return ContentTypeYAML
	}
	if len(content) > 7 && content[:7] == "package" {
		return ContentTypeGo
	}
	return ContentTypeUnknown
}

// hasExtension reports whether filename ends in any of exts. A filename that
// IS the extension (e.g. ".go") counts — the previous hand-rolled
// len(filename) > len(ext) bound excluded that case for no stated reason.
func hasExtension(filename string, exts ...string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(filename, ext) {
			return true
		}
	}
	return false
}
