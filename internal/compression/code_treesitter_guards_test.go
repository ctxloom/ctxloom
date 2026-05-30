//go:build treesitter

package compression

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests complement the per-language happy-path tests in
// code_treesitter_test.go. They target the panic guards and fault-tolerance
// paths hardened this session:
//   - the no-`block` Go func / interface-method path (untagged-unsafe-scarecrow)
//   - the abstract / no-body Java method path (the hasBlock guard mirror)
//   - the parse-failure / unsupported-content verbatim fallback (lustrous-selective-hula)
//
// Building this file at all is the first real -tags treesitter smoke-compile of
// the per-language extractors split out of code_treesitter.go (unscathed-aged-glucose):
// code_go.go, code_python.go, code_javascript.go, code_rust.go, code_java.go.

// langCase exercises one language end-to-end: detectLanguage must route to the
// intended extractor, the structural signatures must survive, and the elided
// bodies must not. Each fixture is crafted so detectLanguage() classifies it
// correctly (note: JS keywords are checked before TS, so the TS fixture avoids
// "function"/"const").
type langCase struct {
	name           string
	source         string
	mustContain    []string // signatures / structure that must be preserved
	mustNotContain []string // body-only tokens that must be elided
}

func TestCodeCompressor_AllLanguages_SignaturesPreservedBodiesElided(t *testing.T) {
	cases := []langCase{
		{
			name: "go",
			source: `package widgets

import "fmt"

// Adder sums two numbers.
func Adder(a, b int) int {
	secretGoBodyToken := a + b
	return secretGoBodyToken
}
`,
			mustContain:    []string{"package widgets", "import", "func Adder(a, b int) int"},
			mustNotContain: []string{"secretGoBodyToken"},
		},
		{
			name: "python",
			source: `import os

def compute(name: str) -> str:
    secret_py_body_token = "x"
    return secret_py_body_token + name
`,
			mustContain:    []string{"def compute"},
			mustNotContain: []string{"secret_py_body_token"},
		},
		{
			name: "javascript",
			source: `function greet(name) {
  const secretJsBodyToken = "hi";
  return secretJsBodyToken + name;
}
`,
			mustContain:    []string{"greet"},
			mustNotContain: []string{"secretJsBodyToken"},
		},
		{
			name: "typescript",
			// Avoid "function" and "const" so detectLanguage routes to TS, not JS.
			source: `interface Greeter {
  greet(name: string): string;
}

class EnglishGreeter implements Greeter {
  greet(name: string): string {
    let secretTsBodyToken = "Hello ";
    return secretTsBodyToken + name;
  }
}
`,
			mustContain:    []string{"interface Greeter", "greet"},
			mustNotContain: []string{"secretTsBodyToken"},
		},
		{
			name: "rust",
			source: `pub fn add(a: i32, b: i32) -> i32 {
    let secret_rust_body_token = a + b;
    secret_rust_body_token
}
`,
			mustContain:    []string{"fn add"},
			mustNotContain: []string{"secret_rust_body_token"},
		},
		{
			name: "java",
			// No leading "package " line: detectLanguage checks Go's "package "
			// prefix before Java's "public class ", so a package line would
			// misroute the fixture to the Go parser.
			source: `public class Service {
    public int add(int a, int b) {
        int secretJavaBodyToken = a + b;
        return secretJavaBodyToken;
    }
}
`,
			mustContain:    []string{"class Service", "add"},
			mustNotContain: []string{"secretJavaBodyToken"},
		},
	}

	c := NewCodeCompressor()
	ctx := context.Background()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Zero panics is itself part of the assertion: a slice-bounds panic
			// in any extractor would crash the test rather than fail it.
			result, err := c.Compress(ctx, tc.source, 0.5)
			require.NoError(t, err)
			require.NotEmpty(t, result.Content)

			for _, want := range tc.mustContain {
				assert.Contains(t, result.Content, want,
					"expected preserved signature %q in output:\n%s", want, result.Content)
			}
			for _, notWant := range tc.mustNotContain {
				assert.NotContains(t, result.Content, notWant,
					"expected elided body token %q to be absent from output:\n%s", notWant, result.Content)
			}
		})
	}
}

// TestCodeCompressor_GoNoBlockFunc covers the untagged-unsafe-scarecrow guard:
// a Go func node with no `block` child (interface methods, forward declarations)
// must not panic on source[StartByte():blockStart] with blockStart still 0.
func TestCodeCompressor_GoNoBlockFunc(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	// The interface methods have no body (no `block` child).
	source := `package io

// Reader has no method bodies at all.
type Reader interface {
	Read(p []byte) (n int, err error)
	Close() error
}
`

	require.NotPanics(t, func() {
		result, err := c.Compress(ctx, source, 0.5)
		require.NoError(t, err)
		assert.Contains(t, result.Content, "Reader")
	})
}

// TestCodeCompressor_JavaNoBodyMethod covers the Java hasBlock guard: a method
// with no body (a `;`-terminated / abstract method) must emit its signature
// verbatim instead of slicing into a non-existent block.
func TestCodeCompressor_JavaNoBodyMethod(t *testing.T) {
	c := NewCodeCompressor()
	ctx := context.Background()

	// "public class " routes detectLanguage to Java; findById has no body.
	// No "package " line (it would misroute to the Go parser).
	source := `public class Repository {
    void findById(long id);

    void save() {
        int filled = 1;
    }
}
`

	require.NotPanics(t, func() {
		result, err := c.Compress(ctx, source, 0.5)
		require.NoError(t, err)
		assert.Contains(t, result.Content, "findById")
	})
}

// TestCodeCompressor_VerbatimFallback covers the lustrous-selective-hula rule:
// compressors degrade to verbatim (Ratio 1.0, nil error) rather than failing.
func TestCodeCompressor_VerbatimFallback(t *testing.T) {
	t.Run("verbatimResult is Ratio 1.0 pass-through", func(t *testing.T) {
		content := "anything at all"
		got := verbatimResult(content, "ast:go")
		assert.Equal(t, content, got.Content)
		assert.Equal(t, 1.0, got.Ratio)
		assert.Equal(t, len(content), got.OriginalSize)
		assert.Equal(t, len(content), got.CompressedSize)
	})

	t.Run("garbage code never panics and never errors", func(t *testing.T) {
		c := NewCodeCompressor()
		ctx := context.Background()
		// Not valid code in any supported language; tree-sitter produces error
		// nodes rather than a hard parse error, so this should still return
		// cleanly (no panic, no error) per the fault-tolerance rule.
		for _, junk := range []string{"", "}{)(", "\x00\x01\x02 not code", strings.Repeat("???", 100)} {
			require.NotPanics(t, func() {
				_, err := c.Compress(ctx, junk, 0.5)
				require.NoError(t, err)
			})
		}
	})

	t.Run("invalid JSON degrades to verbatim Ratio 1.0", func(t *testing.T) {
		jc := NewJSONCompressor()
		result, err := jc.Compress(context.Background(), "{not valid json", 0.5)
		require.NoError(t, err)
		assert.Equal(t, 1.0, result.Ratio)
		assert.Equal(t, "{not valid json", result.Content)
	})
}
