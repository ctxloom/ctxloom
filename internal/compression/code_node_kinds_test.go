//go:build treesitter

package compression

import (
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U046-F20 counted ~70 tree-sitter node-type literals inlined across five files
// and asked for named constants. Constants would not have caught what actually
// goes wrong with these strings: they are not OUR vocabulary, they are the
// GRAMMAR'S, and a constant spelled `nodeStatementBlock = "statment_block"` is
// exactly as wrong as the literal — silently, by extracting nothing. What the
// row's own diagnosis names ("no test that any of them matches the grammar") is
// the guard worth having, and tree-sitter can answer it directly: every symbol
// a grammar can produce is enumerable.
//
// So this is the constants' job done properly. A literal that no longer exists
// in the grammar (renamed upstream, mistyped here) fails HERE, naming the
// language and the string, instead of quietly dropping that declaration kind
// from every compressed file.
var nodeKindVocabulary = map[ContentType][]string{
	ContentTypeGo: {
		"block", "comment", "const_declaration", "function_declaration",
		"import_declaration", "method_declaration", "package_clause",
		"type_declaration", "var_declaration",
	},
	ContentTypePython: {
		"argument_list", "assignment", "async", "augmented_assignment", "block",
		"class", "class_definition", "decorated_definition", "decorator", "def",
		"expression_statement", "function_definition", "identifier",
		"import_from_statement", "import_statement", "parameters", "type",
	},
	ContentTypeJavaScript: {
		"arrow_function", "async", "class", "class_body", "class_declaration",
		"class_heritage", "default", "export_statement", "field_definition",
		"formal_parameters", "function", "function_declaration", "get", "identifier",
		"import_statement", "lexical_declaration", "method_definition",
		"property_identifier", "set", "statement_block", "static",
		"variable_declaration", "variable_declarator",
	},
	// The TypeScript grammar is a superset: these kinds appear ONLY under it,
	// which is why extractJS — one walker for both — matches on them alongside
	// the plain-JS kinds. public_field_definition in particular is TS-only,
	// paired in the extractor with JS's field_definition.
	ContentTypeTypeScript: {
		"abstract_class_declaration", "interface_declaration",
		"public_field_definition", "type_alias_declaration", "type_annotation",
		"type_identifier",
	},
	ContentTypeRust: {
		"block", "const_item", "declaration_list", "enum_item", "fn", "for",
		"function_item", "function_modifiers", "generic_type", "identifier",
		"impl_item", "mod_item", "parameters", "static_item", "struct_item",
		"trait_item", "type_identifier", "type_item", "type_parameters",
		"use_declaration", "visibility_modifier", "where_clause",
	},
	ContentTypeJava: {
		"annotation_type_declaration", "class", "class_body", "class_declaration",
		"constructor_declaration", "enum_declaration", "field_declaration",
		"identifier", "import_declaration", "interface_declaration",
		"method_declaration", "modifiers", "package_declaration", "program",
		"record_declaration", "super_interfaces", "superclass", "type_parameters",
	},
}

// nodeFieldVocabulary is the same guard for the FIELD names the extractors read
// (node.ChildByFieldName / FieldNameForChild). A field is how the Rust
// extractor finds a return type at all — tree-sitter tags it by field, not by
// node kind — so a stale field name loses the "-> T" half of every signature.
var nodeFieldVocabulary = map[ContentType][]string{
	ContentTypePython:     {"right"},
	ContentTypeJavaScript: {"body", "value"},
	ContentTypeRust:       {"return_type"},
}

func grammarSymbols(t *testing.T, lang *sitter.Language) map[string]bool {
	t.Helper()
	symbols := make(map[string]bool)
	for id := uint32(0); id < lang.SymbolCount(); id++ {
		symbols[lang.SymbolName(sitter.Symbol(id))] = true
	}
	require.NotEmpty(t, symbols, "the grammar enumerates its own symbols")
	return symbols
}

func grammarFields(t *testing.T, lang *sitter.Language) map[string]bool {
	t.Helper()
	fields := make(map[string]bool)
	for idx := 0; idx < 256; idx++ {
		if name := lang.FieldName(idx); name != "" {
			fields[name] = true
		}
	}
	require.NotEmpty(t, fields, "the grammar enumerates its own field names")
	return fields
}

func TestNodeKindVocabularyMatchesGrammar(t *testing.T) {
	for ct, kinds := range nodeKindVocabulary {
		t.Run(string(ct), func(t *testing.T) {
			lang, err := languageFor(ct)
			require.NoError(t, err)
			symbols := grammarSymbols(t, lang)
			for _, kind := range kinds {
				assert.True(t, symbols[kind],
					"%s: the extractors match on %q, which this grammar never produces", ct, kind)
			}
		})
	}
}

func TestNodeFieldVocabularyMatchesGrammar(t *testing.T) {
	for ct, names := range nodeFieldVocabulary {
		t.Run(string(ct), func(t *testing.T) {
			lang, err := languageFor(ct)
			require.NoError(t, err)
			fields := grammarFields(t, lang)
			for _, name := range names {
				assert.True(t, fields[name],
					"%s: the extractors read the %q field, which this grammar never tags", ct, name)
			}
		})
	}
}
