//go:build treesitter

package compression

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// rustVerbatim lists Rust top-level node kinds kept verbatim (text + suffix).
var rustVerbatim = map[string]string{
	"use_declaration": "\n",
	"mod_item":        "\n",
	"struct_item":     "\n\n",
	"enum_item":       "\n\n",
	"trait_item":      "\n\n",
	"type_item":       "\n",
	"const_item":      "\n",
	"static_item":     "\n",
}

// extractRust handles Rust AST extraction: one verbatim/handler branch per
// top-level node kind.
func (c *CodeCompressor) extractRust(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if suffix, ok := rustVerbatim[child.Type()]; ok {
			c.emitVerbatim(child, source, out, suffix)
			continue
		}

		switch child.Type() {
		case "impl_item":
			c.extractRustImpl(child, source, out)
		case "function_item":
			c.extractRustFunc(child, source, out)
		}
	}
}

// rustSigParts lists Rust function-signature node kinds emitted as text with a
// fixed prefix and suffix.
var rustSigParts = map[string]struct{ prefix, suffix string }{
	"visibility_modifier": {"", " "},
	"function_modifiers":  {"", " "},
	"type_parameters":     {"", ""},
	"parameters":          {"", ""},
	"return_type":         {" ", ""},
	"where_clause":        {"\n    ", ""},
}

// extractRustFunc emits a Rust function signature, walking its child nodes.
func (c *CodeCompressor) extractRustFunc(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if p, ok := rustSigParts[child.Type()]; ok {
			sig.WriteString(p.prefix)
			sig.WriteString(c.nodeText(child, source))
			sig.WriteString(p.suffix)
			continue
		}

		switch child.Type() {
		case "fn":
			sig.WriteString("fn ")
		case "identifier":
			sig.WriteString(c.nodeText(child, source))
		case "block":
			sig.WriteString(" { ... }")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
}

func (c *CodeCompressor) extractRustImpl(node *sitter.Node, source []byte, out *strings.Builder) {
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
			c.extractRustImplBody(child, source, &sig)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
}

func (c *CodeCompressor) extractRustImplBody(node *sitter.Node, source []byte, out *strings.Builder) {
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
