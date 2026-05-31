//go:build treesitter

package compression

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// extractJS handles JavaScript/TypeScript AST extraction.
//
// One branch per AST node kind: CCN intentionally exceeds 10 (see ADR 0016).
// A handler map would scatter this tightly-coupled visitor for no gain.
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

// extractJSMethodSig walks a class body, emitting one signature per member.
// One branch per member node kind: CCN intentionally exceeds 10 (see ADR 0016).
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
