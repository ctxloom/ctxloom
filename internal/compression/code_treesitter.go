//go:build treesitter

package compression

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	tsTypescript "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// CodeCompressor uses tree-sitter AST analysis to compress code while
// preserving structural elements (imports, signatures, type definitions).
type CodeCompressor struct {
	// PreserveComments keeps doc comments when true.
	PreserveComments bool

	// MaxBodyLines limits function body preview lines (0 = elide entirely).
	MaxBodyLines int

	// parsers cached by language
	parsers map[ContentType]*sitter.Parser
}

// NewCodeCompressor creates a code compressor with default settings.
func NewCodeCompressor() *CodeCompressor {
	return &CodeCompressor{
		PreserveComments: true,
		MaxBodyLines:     0, // Elide bodies by default
		parsers:          make(map[ContentType]*sitter.Parser),
	}
}

// CanHandle returns true for supported programming languages.
func (c *CodeCompressor) CanHandle(ct ContentType) bool {
	switch ct {
	case ContentTypeGo, ContentTypePython, ContentTypeJavaScript,
		ContentTypeTypeScript, ContentTypeRust, ContentTypeJava:
		return true
	}
	return false
}

// Compress extracts structural elements from code, eliding function bodies.
func (c *CodeCompressor) Compress(ctx context.Context, content string, ratio float64) (Result, error) {
	// Detect language from content if not obvious
	ct := c.detectLanguage(content)

	// Fault tolerance: a missing parser or a parse failure degrades to
	// verbatim pass-through rather than failing the caller.
	parser, err := c.getParser(ct)
	if err != nil {
		return verbatimResult(content, "ast:"+string(ct)), nil
	}

	tree, err := parser.ParseCtx(ctx, nil, []byte(content))
	if err != nil {
		return verbatimResult(content, "ast:"+string(ct)), nil
	}
	defer tree.Close()

	var result strings.Builder
	var preserved, compressed []string

	c.extractStructure(tree.RootNode(), []byte(content), ct, &result, &preserved, &compressed)

	compressed = append(compressed, "function/method bodies")

	output := result.String()
	return Result{
		Content:            output,
		OriginalSize:       len(content),
		CompressedSize:     len(output),
		Ratio:              float64(len(output)) / float64(len(content)),
		PreservedElements:  preserved,
		CompressedElements: compressed,
		ModelID:            fmt.Sprintf("ast:%s", ct),
	}, nil
}

func (c *CodeCompressor) detectLanguage(content string) ContentType {
	// Simple heuristics
	if strings.HasPrefix(content, "package ") {
		return ContentTypeGo
	}
	if strings.Contains(content, "def ") && strings.Contains(content, ":") {
		return ContentTypePython
	}
	if strings.Contains(content, "fn ") && strings.Contains(content, "->") {
		return ContentTypeRust
	}
	if strings.Contains(content, "public class ") || strings.Contains(content, "private class ") {
		return ContentTypeJava
	}
	if strings.Contains(content, "function ") || strings.Contains(content, "const ") {
		return ContentTypeJavaScript
	}
	if strings.Contains(content, "interface ") || strings.Contains(content, ": string") {
		return ContentTypeTypeScript
	}
	return ContentTypeGo // Default
}

func (c *CodeCompressor) getParser(ct ContentType) (*sitter.Parser, error) {
	if p, ok := c.parsers[ct]; ok {
		return p, nil
	}

	parser := sitter.NewParser()
	var lang *sitter.Language

	switch ct {
	case ContentTypeGo:
		lang = golang.GetLanguage()
	case ContentTypePython:
		lang = python.GetLanguage()
	case ContentTypeJavaScript:
		lang = javascript.GetLanguage()
	case ContentTypeTypeScript:
		lang = tsTypescript.GetLanguage()
	case ContentTypeRust:
		lang = rust.GetLanguage()
	case ContentTypeJava:
		lang = java.GetLanguage()
	default:
		return nil, fmt.Errorf("unsupported language: %s", ct)
	}

	parser.SetLanguage(lang)
	c.parsers[ct] = parser
	return parser, nil
}

// extractStructure walks the AST and extracts structural elements.
func (c *CodeCompressor) extractStructure(
	node *sitter.Node,
	source []byte,
	ct ContentType,
	out *strings.Builder,
	preserved, compressed *[]string,
) {
	if node == nil {
		return
	}

	nodeType := node.Type()

	// Language-specific structural extraction
	switch ct {
	case ContentTypeGo:
		c.extractGo(node, source, out, preserved, compressed)
	case ContentTypePython:
		c.extractPython(node, source, out, preserved, compressed)
	case ContentTypeJavaScript, ContentTypeTypeScript:
		c.extractJS(node, source, out, preserved, compressed)
	case ContentTypeRust:
		c.extractRust(node, source, out, preserved, compressed)
	case ContentTypeJava:
		c.extractJava(node, source, out, preserved, compressed)
	default:
		// Fallback: just extract top-level children
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child != nil {
				out.WriteString(c.nodeText(child, source))
				out.WriteString("\n")
			}
		}
	}

	_ = nodeType // Used for debugging
}

// Helper functions

func (c *CodeCompressor) nodeText(node *sitter.Node, source []byte) string {
	return string(source[node.StartByte():node.EndByte()])
}

func (c *CodeCompressor) isDocComment(node *sitter.Node, source []byte) bool {
	text := c.nodeText(node, source)
	return strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*")
}
