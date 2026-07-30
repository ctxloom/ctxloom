//go:build treesitter

package compression

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// jsVerbatim lists JS/TS top-level node kinds kept verbatim (text + suffix).
var jsVerbatim = map[string]string{
	"import_statement":       "\n",
	"interface_declaration":  "\n\n",
	"type_alias_declaration": "\n",
}

// extractJS handles JavaScript/TypeScript AST extraction: one verbatim/handler
// branch per top-level node kind.
func (c *CodeCompressor) extractJS(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		if suffix, ok := jsVerbatim[child.Type()]; ok {
			c.emitVerbatim(child, source, out, suffix)
			continue
		}

		switch child.Type() {
		case "export_statement":
			c.extractJSExport(child, source, out)
		case "function_declaration":
			c.extractJSFunc(child, source, out)
		case "class_declaration":
			c.extractJSClass(child, source, out)
		case "lexical_declaration":
			c.extractJSLexical(child, source, out)
		}
	}
}

// extractJSExport emits an exported declaration with its body elided. Exported
// type definitions (interface_declaration, type_alias_declaration) are kept
// verbatim, same as their unexported forms in jsVerbatim. Export forms with no
// declaration child — export clauses and re-exports (`export { a } from './b'`,
// `export * from './b'`) and `export default <expr>` — are kept verbatim too:
// emitting a dangling "export " and dropping the rest is content loss in a
// compressor whose contract is that type definitions survive.
func (c *CodeCompressor) extractJSExport(node *sitter.Node, source []byte, out *strings.Builder) {
	prefix := "export "
	for j := 0; j < int(node.ChildCount()); j++ {
		exp := node.Child(j)
		if exp == nil {
			continue
		}
		if suffix, ok := jsVerbatim[exp.Type()]; ok {
			out.WriteString(prefix)
			c.emitVerbatim(exp, source, out, suffix)
			return
		}
		switch exp.Type() {
		case "default":
			prefix += "default "
		case "function_declaration":
			out.WriteString(prefix)
			c.extractJSFunc(exp, source, out)
			return
		case "class_declaration", "abstract_class_declaration":
			out.WriteString(prefix)
			c.extractJSClass(exp, source, out)
			return
		case "lexical_declaration", "variable_declaration":
			out.WriteString(prefix)
			c.extractJSLexical(exp, source, out)
			return
		}
	}
	// No compressible declaration child: export clause, re-export, or default
	// expression. These carry no function body to elide — keep the statement.
	out.WriteString(c.nodeText(node, source))
	out.WriteString("\n")
}

func (c *CodeCompressor) extractJSFunc(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder

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
			sig.WriteString(c.nodeText(child, source))
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
}

func (c *CodeCompressor) extractJSClass(node *sitter.Node, source []byte, out *strings.Builder) {
	var sig strings.Builder

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "class":
			sig.WriteString("class ")
		case "identifier", "type_identifier":
			sig.WriteString(c.nodeText(child, source))
		case "class_heritage":
			sig.WriteString(" ")
			sig.WriteString(c.nodeText(child, source))
		case "class_body":
			sig.WriteString(" {\n")
			c.extractJSClassBody(child, source, &sig)
			sig.WriteString("}")
		}
	}

	out.WriteString(sig.String())
	out.WriteString("\n\n")
}

func (c *CodeCompressor) extractJSClassBody(node *sitter.Node, source []byte, out *strings.Builder) {
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

// jsMethodModifiers are the method-prefix keywords emitted with a trailing
// space (e.g. "async foo()", "static get bar()").
var jsMethodModifiers = map[string]bool{
	"async":  true,
	"static": true,
	"get":    true,
	"set":    true,
}

// extractJSMethodSig walks a class body, emitting one signature per member.
func (c *CodeCompressor) extractJSMethodSig(node *sitter.Node, source []byte, out *strings.Builder) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "property_identifier", "formal_parameters", "type_annotation":
			out.WriteString(c.nodeText(child, source))
		case "statement_block":
			out.WriteString(" { ... }")
			return
		default:
			if jsMethodModifiers[child.Type()] {
				out.WriteString(c.nodeText(child, source))
				out.WriteString(" ")
			}
		}
	}
}

// jsDeclaredValue returns the first declarator's value node — what a const/let
// is actually bound TO. The grammar tags it, so an arrow token or an '=' byte
// occurring anywhere ELSE in the declaration (inside a string literal, a
// template, a regex, a TypeScript default type parameter) cannot be mistaken
// for it.
func jsDeclaredValue(node *sitter.Node) *sitter.Node {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && child.Type() == "variable_declarator" {
			return child.ChildByFieldName("value")
		}
	}
	return nil
}

// extractJSLexical emits a const/let declaration with its value elided: an
// arrow function keeps its parameter list and loses its body, and any other
// long value is replaced by "...". Both cuts are made at the VALUE NODE'S
// start byte rather than by searching the declaration text — the text search
// this replaces cut a declaration at the first "=> {" it saw, which for a
// string literal containing an arrow was mid-literal, throwing away the rest of
// the statement including its closing quote.
//
// A declaration is never dropped entirely: extractJSExport writes "export "
// before delegating here, so emitting nothing would leave a dangling "export "
// glued onto the next declaration.
func (c *CodeCompressor) extractJSLexical(node *sitter.Node, source []byte, out *strings.Builder) {
	text := c.nodeText(node, source)
	head := func(upTo *sitter.Node) string {
		return strings.TrimRight(string(source[node.StartByte():upTo.StartByte()]), " \t\r\n")
	}

	switch value := jsDeclaredValue(node); {
	case value == nil:
		out.WriteString(text)

	case value.Type() == "arrow_function":
		body := value.ChildByFieldName("body")
		switch {
		case body == nil:
			out.WriteString(text)
		case body.Type() == "statement_block":
			out.WriteString(head(body))
			out.WriteString(" { ... }")
		case strings.Contains(text, "\n"):
			// A multi-line expression body: elide it, keeping the signature.
			out.WriteString(head(body))
			out.WriteString(" ...")
		default:
			// A one-line expression body IS the signature — keep it whole.
			out.WriteString(text)
		}

	case len(text) < 100:
		out.WriteString(text)

	default:
		// Long value: keep the binding (name and any type annotation).
		out.WriteString(head(value))
		out.WriteString(" ...")
	}
	out.WriteString("\n")
}
