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

func (c *CodeCompressor) extractJSLexical(node *sitter.Node, source []byte, out *strings.Builder) {
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
	} else {
		// Regular const/let. Keep short declarations verbatim; for long ones,
		// elide the value after the first '=' so the binding (name + type
		// annotation) still survives. Never drop the declaration entirely:
		// extractJSExport writes "export " before delegating here, so emitting
		// nothing would leave a dangling "export " glued onto the next
		// declaration (content loss + corruption).
		switch {
		case len(text) < 100:
			out.WriteString(text)
		case strings.IndexByte(text, '=') > 0:
			idx := strings.IndexByte(text, '=')
			out.WriteString(strings.TrimRight(text[:idx], " "))
			out.WriteString(" = ...")
		default:
			out.WriteString(text)
		}
		out.WriteString("\n")
	}
}
