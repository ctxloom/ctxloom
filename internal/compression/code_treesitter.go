//go:build treesitter

package compression

import (
	"context"
	"fmt"
	"strings"
	"sync"

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

	// parserPools holds a sync.Pool of reusable *sitter.Parser per language. A
	// tree-sitter Parser wraps a stateful C object, so ParseCtx must NOT run on
	// a parser another goroutine is also parsing with. Each Compress borrows a
	// parser from its language pool for the duration of the parse and returns
	// it, so a single shared CodeCompressor is safe under concurrent use while
	// still reusing parsers. parserPoolsMu guards first-use pool population.
	parserPoolsMu sync.Mutex
	parserPools   map[ContentType]*sync.Pool
}

// NewCodeCompressor creates a code compressor with default settings.
func NewCodeCompressor() *CodeCompressor {
	return &CodeCompressor{
		PreserveComments: true,
		MaxBodyLines:     0, // Elide bodies by default
		parserPools:      make(map[ContentType]*sync.Pool),
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
// The routed contentType picks the grammar; content sniffing is only a
// fallback for an unknown type, and an unsniffable input degrades to verbatim
// — parsing under a guessed grammar silently loses the content.
func (c *CodeCompressor) Compress(ctx context.Context, ct ContentType, content string, ratio float64) (Result, error) {
	if !c.CanHandle(ct) {
		ct = c.detectLanguage(content)
	}
	if ct == ContentTypeUnknown {
		return verbatimResult(content, "ast:unknown"), nil
	}

	// Fault tolerance: a missing parser or a parse failure degrades to
	// verbatim pass-through rather than failing the caller.
	pool, err := c.parserPool(ct)
	if err != nil {
		return verbatimResult(content, "ast:"+string(ct)), nil
	}
	parser := pool.Get().(*sitter.Parser)
	defer pool.Put(parser)

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
	if strings.TrimSpace(output) == "" {
		// U046-F02: the grammar can yield zero recognized node kinds for
		// content that parses (e.g. prose wrongly routed here, or a real
		// file this extractor's node-kind list doesn't cover) — extractGo
		// et al only ever WRITE to result, never guarantee it ends up
		// non-empty. An empty Content with a nil error and Ratio: 0.0
		// previously read as a successful, extreme compression rather
		// than the degenerate case it is; degrade to the same
		// verbatim-pass-through every other failure path in this method
		// already uses.
		return verbatimResult(content, "ast:"+string(ct)), nil
	}
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

// languageSignature matches content for a language: a match requires every
// substring in `all` to be present and, when `any` is non-empty, at least one
// of `any`.
type languageSignature struct {
	ct  ContentType
	all []string
	any []string
}

// languageSignatures is checked in order; the first match wins.
var languageSignatures = []languageSignature{
	{ct: ContentTypePython, all: []string{"def ", ":"}},
	{ct: ContentTypeRust, all: []string{"fn ", "->"}},
	{ct: ContentTypeJava, any: []string{"public class ", "private class "}},
	{ct: ContentTypeJavaScript, any: []string{"function ", "const "}},
	{ct: ContentTypeTypeScript, any: []string{"interface ", ": string"}},
}

func (s languageSignature) matches(content string) bool {
	for _, sub := range s.all {
		if !strings.Contains(content, sub) {
			return false
		}
	}
	for _, sub := range s.any {
		if strings.Contains(content, sub) {
			return true
		}
	}
	return len(s.any) == 0
}

// detectLanguage guesses a language from content heuristics. Unrecognized
// content returns ContentTypeUnknown — the old default-to-Go behavior parsed
// non-Go content under the Go grammar, which finds no Go nodes and compresses
// the content to near-empty.
func (c *CodeCompressor) detectLanguage(content string) ContentType {
	if strings.HasPrefix(content, "package ") {
		return ContentTypeGo
	}
	for _, sig := range languageSignatures {
		if sig.matches(content) {
			return sig.ct
		}
	}
	return ContentTypeUnknown
}

// parserPool returns the per-language pool of parsers, creating it on first
// use. The pool's New builds a parser with the language already set, so every
// borrowed parser is ready to parse.
func (c *CodeCompressor) parserPool(ct ContentType) (*sync.Pool, error) {
	lang, err := languageFor(ct)
	if err != nil {
		return nil, err
	}

	c.parserPoolsMu.Lock()
	defer c.parserPoolsMu.Unlock()
	if pool, ok := c.parserPools[ct]; ok {
		return pool, nil
	}
	pool := &sync.Pool{New: func() any {
		parser := sitter.NewParser()
		parser.SetLanguage(lang)
		return parser
	}}
	c.parserPools[ct] = pool
	return pool, nil
}

// languageFor maps a ContentType to its tree-sitter grammar.
func languageFor(ct ContentType) (*sitter.Language, error) {
	switch ct {
	case ContentTypeGo:
		return golang.GetLanguage(), nil
	case ContentTypePython:
		return python.GetLanguage(), nil
	case ContentTypeJavaScript:
		return javascript.GetLanguage(), nil
	case ContentTypeTypeScript:
		return tsTypescript.GetLanguage(), nil
	case ContentTypeRust:
		return rust.GetLanguage(), nil
	case ContentTypeJava:
		return java.GetLanguage(), nil
	default:
		return nil, fmt.Errorf("unsupported language: %s", ct)
	}
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

// verbatimEmit describes a node kind kept as-is: its text is written followed
// by suffix, and label is recorded in the preserved list.
type verbatimEmit struct{ suffix, label string }

// emitVerbatim writes child's source text plus suffix and records label.
// Shared by the per-language visitors for node kinds kept verbatim.
func (c *CodeCompressor) emitVerbatim(child *sitter.Node, source []byte, out *strings.Builder, preserved *[]string, v verbatimEmit) {
	out.WriteString(c.nodeText(child, source))
	out.WriteString(v.suffix)
	*preserved = append(*preserved, v.label)
}

func (c *CodeCompressor) isDocComment(node *sitter.Node, source []byte) bool {
	text := c.nodeText(node, source)
	return strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*")
}
