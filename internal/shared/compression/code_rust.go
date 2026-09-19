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
	"where_clause":        {"\n    ", ""},
}

// writeRustFuncSig writes one function_item's declared signature — every
// modifier, generic parameter, argument list, return type and where-clause it
// carries — and renders its body as body. ONE walker serves both the top-level
// and the impl-nested position: the node kinds a Rust signature is built from
// do not depend on where the function sits, so a second walk of them is only an
// opportunity to recognize fewer.
//
// The return type is matched by FIELD name, not node kind: tree-sitter-rust
// tags it with the field `return_type` but leaves the node its own type
// (`generic_type`, `primitive_type`, `type_identifier`, ...), so a kind-keyed
// lookup never sees it and the `-> T` half of every signature goes missing.
func (c *CodeCompressor) writeRustFuncSig(node *sitter.Node, source []byte, out *strings.Builder, body string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if node.FieldNameForChild(i) == "return_type" {
			out.WriteString(" -> ")
			out.WriteString(c.nodeText(child, source))
			continue
		}

		if p, ok := rustSigParts[child.Type()]; ok {
			out.WriteString(p.prefix)
			out.WriteString(c.nodeText(child, source))
			out.WriteString(p.suffix)
			continue
		}

		switch child.Type() {
		case "fn":
			out.WriteString("fn ")
		case "identifier":
			out.WriteString(c.nodeText(child, source))
		case "block":
			out.WriteString(body)
			return
		}
	}
}

// extractRustFunc emits a top-level Rust function signature.
func (c *CodeCompressor) extractRustFunc(node *sitter.Node, source []byte, out *strings.Builder) {
	c.writeRustFuncSig(node, source, out, " { ... }")
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

// extractRustImplBody emits an impl block's items with function bodies elided.
// An impl declares more than functions: associated consts and types are part of
// the surface a caller programs against, and rustVerbatim already names how to
// render exactly those kinds at the top level — reused here so an impl body
// cannot enumerate fewer kinds than the module around it.
func (c *CodeCompressor) extractRustImplBody(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if child.Type() == "function_item" {
			out.WriteString("    ")
			c.writeRustFuncSig(child, source, out, " { ... }")
			out.WriteString("\n")
			continue
		}
		if _, ok := rustVerbatim[child.Type()]; ok {
			writeIndentedBlock(out, "    ", c.nodeText(child, source))
		}
	}
}
