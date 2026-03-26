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

	parser, err := c.getParser(ct)
	if err != nil {
		return Result{}, fmt.Errorf("no parser for content type %s: %w", ct, err)
	}

	tree, err := parser.ParseCtx(ctx, nil, []byte(content))
	if err != nil {
		return Result{}, fmt.Errorf("parse failed: %w", err)
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

// extractGo handles Go-specific AST extraction.
func (c *CodeCompressor) extractGo(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "package_clause":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "package clause")

		case "import_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "imports")

		case "type_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "type declaration")

		case "const_declaration", "var_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "const/var declaration")

		case "function_declaration", "method_declaration":
			c.extractGoFunc(child, source, out, preserved)

		case "comment":
			if c.PreserveComments && c.isDocComment(child, source) {
				out.WriteString(c.nodeText(child, source))
				out.WriteString("\n")
			}
		}
	}
}

func (c *CodeCompressor) extractGoFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	// For Go, the simplest approach is to get the full signature from source
	// by finding the block and taking everything before it
	var funcName string
	var blockStart uint32

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "identifier" && funcName == "" {
			funcName = c.nodeText(child, source)
		}
		if child.Type() == "block" {
			blockStart = child.StartByte()
			break
		}
	}

	// Get the signature (everything from func start to block start)
	signature := strings.TrimSpace(string(source[node.StartByte():blockStart]))
	out.WriteString(signature)
	out.WriteString(" { ... }\n\n")

	if funcName != "" {
		*preserved = append(*preserved, fmt.Sprintf("func %s", funcName))
	}
}

// extractPython handles Python-specific AST extraction.
func (c *CodeCompressor) extractPython(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "import_statement", "import_from_statement":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "import")

		case "class_definition":
			c.extractPythonClass(child, source, out, preserved)

		case "function_definition":
			c.extractPythonFunc(child, source, out, preserved)

		case "decorated_definition":
			// Handle decorators
			for j := 0; j < int(child.ChildCount()); j++ {
				dec := child.Child(j)
				if dec == nil {
					continue
				}
				if dec.Type() == "decorator" {
					out.WriteString(c.nodeText(dec, source))
					out.WriteString("\n")
				} else if dec.Type() == "function_definition" {
					c.extractPythonFunc(dec, source, out, preserved)
				} else if dec.Type() == "class_definition" {
					c.extractPythonClass(dec, source, out, preserved)
				}
			}

		case "expression_statement":
			// Could be docstring at module level
			if c.PreserveComments {
				text := c.nodeText(child, source)
				if strings.HasPrefix(text, `"""`) || strings.HasPrefix(text, `'''`) {
					out.WriteString(text)
					out.WriteString("\n")
				}
			}
		}
	}
}

func (c *CodeCompressor) extractPythonFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	// Build signature: def name(params) -> return_type:
	var sig strings.Builder
	var funcName string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "def":
			sig.WriteString("def ")
		case "identifier":
			funcName = c.nodeText(child, source)
			sig.WriteString(funcName)
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type":
			sig.WriteString(" -> ")
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(":\n    ...")
		case ":":
			// Skip, handled with block
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("def %s", funcName))
}

func (c *CodeCompressor) extractPythonClass(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var className string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "class":
			sig.WriteString("class ")
		case "identifier":
			className = c.nodeText(child, source)
			sig.WriteString(className)
		case "argument_list":
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(":\n")
			// Extract method signatures from class body
			c.extractPythonClassBody(child, source, &sig, preserved)
		case ":":
			// Skip
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n")
	*preserved = append(*preserved, fmt.Sprintf("class %s", className))
}

func (c *CodeCompressor) extractPythonClassBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_definition" {
			out.WriteString("    ")
			c.extractPythonFuncSignatureOnly(child, source, out, preserved)
		} else if child.Type() == "decorated_definition" {
			for j := 0; j < int(child.ChildCount()); j++ {
				dec := child.Child(j)
				if dec != nil && dec.Type() == "decorator" {
					out.WriteString("    ")
					out.WriteString(c.nodeText(dec, source))
					out.WriteString("\n")
				} else if dec != nil && dec.Type() == "function_definition" {
					out.WriteString("    ")
					c.extractPythonFuncSignatureOnly(dec, source, out, preserved)
				}
			}
		}
	}
}

func (c *CodeCompressor) extractPythonFuncSignatureOnly(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "def":
			sig.WriteString("def ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type":
			sig.WriteString(" -> ")
			sig.WriteString(c.nodeText(child, source))
		case "block", ":":
			// Stop at body
		}
	}
	sig.WriteString(": ...")
	out.WriteString(sig.String())
	out.WriteString("\n")
}

// extractJS handles JavaScript/TypeScript AST extraction.
func (c *CodeCompressor) extractJS(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "import_statement":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "import")

		case "export_statement":
			// Handle exported functions/classes
			out.WriteString("export ")
			for j := 0; j < int(child.ChildCount()); j++ {
				exp := child.Child(j)
				if exp == nil {
					continue
				}
				if exp.Type() == "function_declaration" {
					c.extractJSFunc(exp, source, out, preserved)
				} else if exp.Type() == "class_declaration" {
					c.extractJSClass(exp, source, out, preserved)
				} else if exp.Type() == "lexical_declaration" {
					c.extractJSLexical(exp, source, out, preserved)
				}
			}

		case "function_declaration":
			c.extractJSFunc(child, source, out, preserved)

		case "class_declaration":
			c.extractJSClass(child, source, out, preserved)

		case "lexical_declaration":
			c.extractJSLexical(child, source, out, preserved)

		case "interface_declaration":
			// TypeScript interface - keep fully
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "interface")

		case "type_alias_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "type alias")
		}
	}
}

func (c *CodeCompressor) extractJSFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var funcName string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "function", "async":
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(" ")
		case "identifier":
			funcName = c.nodeText(child, source)
			sig.WriteString(funcName)
		case "formal_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type_annotation":
			sig.WriteString(c.nodeText(child, source))
		case "statement_block":
			sig.WriteString(" { ... }")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("function %s", funcName))
}

func (c *CodeCompressor) extractJSClass(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var className string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "class":
			sig.WriteString("class ")
		case "identifier", "type_identifier":
			className = c.nodeText(child, source)
			sig.WriteString(className)
		case "class_heritage":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "class_body":
			sig.WriteString(" {\n")
			c.extractJSClassBody(child, source, &sig, preserved)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("class %s", className))
}

func (c *CodeCompressor) extractJSClassBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "method_definition":
			out.WriteString("  ")
			c.extractJSMethodSig(child, source, out)
			out.WriteString("\n")
		case "public_field_definition", "field_definition":
			out.WriteString("  ")
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
		}
	}
}

func (c *CodeCompressor) extractJSMethodSig(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "property_identifier":
			out.WriteString(c.nodeText(child, source))
		case "formal_parameters":
			out.WriteString(c.nodeText(child, source))
		case "type_annotation":
			out.WriteString(c.nodeText(child, source))
		case "statement_block":
			out.WriteString(" { ... }")
			return
		default:
			if child.Type() == "async" || child.Type() == "static" || child.Type() == "get" || child.Type() == "set" {
				out.WriteString(c.nodeText(child, source))
				out.WriteString(" ")
			}
		}
	}
}

func (c *CodeCompressor) extractJSLexical(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	// Handle const/let declarations - keep if they're arrow functions
	text := c.nodeText(node, source)
	if strings.Contains(text, "=>") {
		// Arrow function - extract signature
		// Simple approach: keep declaration, replace body
		if idx := strings.Index(text, "=> {"); idx > 0 {
			out.WriteString(text[:idx])
			out.WriteString("=> { ... }")
			out.WriteString("\n")
		} else if idx := strings.Index(text, "=>\n"); idx > 0 {
			out.WriteString(text[:idx])
			out.WriteString("=> ...")
			out.WriteString("\n")
		} else {
			out.WriteString(text)
			out.WriteString("\n")
		}
		*preserved = append(*preserved, "arrow function")
	} else {
		// Regular const/let - keep if short
		if len(text) < 100 {
			out.WriteString(text)
			out.WriteString("\n")
			*preserved = append(*preserved, "const/let")
		}
	}
}

// extractRust handles Rust AST extraction.
func (c *CodeCompressor) extractRust(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "use_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "use")

		case "mod_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "mod")

		case "struct_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "struct")

		case "enum_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "enum")

		case "trait_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "trait")

		case "impl_item":
			c.extractRustImpl(child, source, out, preserved)

		case "function_item":
			c.extractRustFunc(child, source, out, preserved)

		case "type_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "type alias")

		case "const_item", "static_item":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "const/static")
		}
	}
}

func (c *CodeCompressor) extractRustFunc(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var funcName string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "visibility_modifier", "function_modifiers":
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(" ")
		case "fn":
			sig.WriteString("fn ")
		case "identifier":
			funcName = c.nodeText(child, source)
			sig.WriteString(funcName)
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "parameters":
			sig.WriteString(c.nodeText(child, source))
		case "return_type":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "where_clause":
			sig.WriteString("\n    ")
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(" { ... }")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("fn %s", funcName))
}

func (c *CodeCompressor) extractRustImpl(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	sig.WriteString("impl")

	// Extract impl header
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "type_identifier", "generic_type":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "for":
			sig.WriteString(" for")
		case "declaration_list":
			sig.WriteString(" {\n")
			c.extractRustImplBody(child, source, &sig, preserved)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, "impl block")
}

func (c *CodeCompressor) extractRustImplBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_item" {
			out.WriteString("    ")
			c.extractRustFuncSigOnly(child, source, out)
			out.WriteString("\n")
		}
	}
}

func (c *CodeCompressor) extractRustFuncSigOnly(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "visibility_modifier":
			out.WriteString(c.nodeText(child, source))
			out.WriteString(" ")
		case "fn":
			out.WriteString("fn ")
		case "identifier":
			out.WriteString(c.nodeText(child, source))
		case "type_parameters":
			out.WriteString(c.nodeText(child, source))
		case "parameters":
			out.WriteString(c.nodeText(child, source))
		case "return_type":
			out.WriteString(" ")
			out.WriteString(c.nodeText(child, source))
		case "block":
			out.WriteString(" { ... }")
			return
		}
	}
}

// extractJava handles Java AST extraction.
func (c *CodeCompressor) extractJava(node *sitter.Node, source []byte, out *strings.Builder, preserved, compressed *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "package_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "package")

		case "import_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
			*preserved = append(*preserved, "import")

		case "class_declaration":
			c.extractJavaClass(child, source, out, preserved)

		case "interface_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "interface")

		case "enum_declaration":
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n\n")
			*preserved = append(*preserved, "enum")

		case "program":
			// Handle nested program node
			c.extractJava(child, source, out, preserved, compressed)
		}
	}
}

func (c *CodeCompressor) extractJavaClass(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	var sig strings.Builder
	var className string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "modifiers":
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(" ")
		case "class":
			sig.WriteString("class ")
		case "identifier":
			className = c.nodeText(child, source)
			sig.WriteString(className)
		case "type_parameters":
			sig.WriteString(c.nodeText(child, source))
		case "superclass":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "super_interfaces":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "class_body":
			sig.WriteString(" {\n")
			c.extractJavaClassBody(child, source, &sig, preserved)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
	*preserved = append(*preserved, fmt.Sprintf("class %s", className))
}

func (c *CodeCompressor) extractJavaClassBody(node *sitter.Node, source []byte, out *strings.Builder, preserved *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "field_declaration":
			out.WriteString("    ")
			out.WriteString(c.nodeText(child, source))
			out.WriteString("\n")
		case "method_declaration":
			out.WriteString("    ")
			c.extractJavaMethodSig(child, source, out)
			out.WriteString("\n")
		case "constructor_declaration":
			out.WriteString("    ")
			c.extractJavaConstructorSig(child, source, out)
			out.WriteString("\n")
		}
	}
}

func (c *CodeCompressor) extractJavaMethodSig(node *sitter.Node, source []byte, out *strings.Builder) {
	// Find the block and extract everything before it from source
	var blockStart uint32
	hasBlock := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == "block" {
			blockStart = child.StartByte()
			hasBlock = true
			break
		}
	}

	if hasBlock {
		sig := strings.TrimSpace(string(source[node.StartByte():blockStart]))
		out.WriteString(sig)
		out.WriteString(" { ... }")
	} else {
		// Abstract method or interface method - no body
		out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
	}
}

func (c *CodeCompressor) extractJavaConstructorSig(node *sitter.Node, source []byte, out *strings.Builder) {
	// Find the constructor_body and extract everything before it from source
	var bodyStart uint32
	hasBody := false

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == "constructor_body" {
			bodyStart = child.StartByte()
			hasBody = true
			break
		}
	}

	if hasBody {
		sig := strings.TrimSpace(string(source[node.StartByte():bodyStart]))
		out.WriteString(sig)
		out.WriteString(" { ... }")
	} else {
		out.WriteString(strings.TrimSpace(c.nodeText(node, source)))
	}
}

// Helper functions

func (c *CodeCompressor) nodeText(node *sitter.Node, source []byte) string {
	return string(source[node.StartByte():node.EndByte()])
}

func (c *CodeCompressor) isDocComment(node *sitter.Node, source []byte) bool {
	text := c.nodeText(node, source)
	return strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*")
}
