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
func (c *CodeCompressor) Compress(ctx context.Context, ct ContentType, content string) (Result, error) {
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

	c.extractStructure(tree.RootNode(), []byte(content), ct, &result)

	output := result.String()
	if strings.TrimSpace(output) == "" {
		// The grammar can yield zero recognized node kinds for content that
		// parses (e.g. prose wrongly routed here, or a real file this
		// extractor's node-kind list doesn't cover) — extractGo et al only
		// ever WRITE to result, never guarantee it ends up non-empty. An
		// empty Content with a nil error and Ratio: 0.0 previously read as
		// a successful, extreme compression rather than the degenerate
		// case it is; degrade to the same verbatim-pass-through every
		// other failure path in this method already uses.
		return verbatimResult(content, "ast:"+string(ct)), nil
	}
	return Result{
		Content: output,
		Ratio:   float64(len(output)) / float64(len(content)),
		ModelID: fmt.Sprintf("ast:%s", ct),
	}, nil
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
) {
	if node == nil {
		return
	}

	// Language-specific structural extraction. ct is guaranteed to be one of
	// the cases below: Compress only reaches this point after parserPool ->
	// languageFor has already rejected any other ContentType.
	switch ct {
	case ContentTypeGo:
		c.extractGo(node, source, out)
	case ContentTypePython:
		c.extractPython(node, source, out)
	case ContentTypeJavaScript, ContentTypeTypeScript:
		c.extractJS(node, source, out)
	case ContentTypeRust:
		c.extractRust(node, source, out)
	case ContentTypeJava:
		c.extractJava(node, source, out)
	}
}

// Helper functions

func (c *CodeCompressor) nodeText(node *sitter.Node, source []byte) string {
	return string(source[node.StartByte():node.EndByte()])
}

// emitVerbatim writes child's source text plus suffix. Shared by the
// per-language visitors for node kinds kept verbatim.
func (c *CodeCompressor) emitVerbatim(child *sitter.Node, source []byte, out *strings.Builder, suffix string) {
	out.WriteString(c.nodeText(child, source))
	out.WriteString(suffix)
}

// writeIndentedBlock writes s into out with prefix on every non-blank line. The
// class/impl body extractors use it to nest a member that was itself extracted
// (a nested class, an enum) at the depth it sits at in the source, instead of
// dropping it for want of somewhere to put it.
func writeIndentedBlock(out *strings.Builder, prefix, s string) {
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out.WriteString(prefix)
			out.WriteString(line)
		}
		out.WriteString("\n")
	}
}

// extractIndented runs extract into a scratch builder and writes the result
// into out at prefix — for a member whose own extractor writes at column zero.
func (c *CodeCompressor) extractIndented(node *sitter.Node, source []byte, out *strings.Builder, prefix string, extract func(*sitter.Node, []byte, *strings.Builder)) {
	var nested strings.Builder
	extract(node, source, &nested)
	writeIndentedBlock(out, prefix, nested.String())
}

func (c *CodeCompressor) isDocComment(node *sitter.Node, source []byte) bool {
	text := c.nodeText(node, source)
	return strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*")
}
