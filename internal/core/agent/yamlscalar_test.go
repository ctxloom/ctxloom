package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

// TestYAMLQuoters_AgreeWhenBothQuote is the parity gate between this package's
// two YAML-frontmatter scalar quoters:
//
//	yamlDoubleQuoted  (skillcommandshape.go) — ALWAYS quotes, escaping via
//	                   json.Marshal, so quotes, backslashes AND control
//	                   characters are all handled. Serves kiro's
//	                   SKILL.md frontmatter.
//	EscapeYAMLString  (commandfiles.go)      — quotes CONDITIONALLY, and
//	                   delegates the escaping itself to yamlDoubleQuoted.
//	                   Serves claude/codex/opencode command frontmatter.
//
// The conditional-quoting policy is deliberate and is NOT what this pins. What
// it pins is the escaping: whenever EscapeYAMLString decides to quote, the
// bytes inside the quotes must be the ones json.Marshal would produce. Under
// hand-written rules that escape only \ and " they are not — a description
// containing a newline triggers the quote (Contains "\n") and then emits a
// LITERAL newline inside the double-quoted scalar, which YAML line-folds back
// to a space on read. Silent corruption of a delivered description, exit 0.
//
// Every case is also parsed back with a real YAML parser, so the assertion is
// "the value survives", not "the bytes look plausible".
func TestYAMLQuoters_AgreeWhenBothQuote(t *testing.T) {
	inputs := []struct {
		name string
		in   string
	}{
		{"plain", "hello world"},
		{"colon", "foo: bar"},
		{"windows path", `C:\path\to`},
		{"quote and backslash", `a:"b\c`},
		{"yaml literal true", "true"},
		{"number", "123"},
		{"embedded newline", "line one\nline two"},
		{"embedded tab", "before\tafter"},
		{"carriage return", "before\rafter"},
		{"html-ish angle brackets", "use <file> then & more"},
		{"leading indicator", "- item"},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			escaped := EscapeYAMLString(tc.in)

			// Whatever the policy decided, the emitted frontmatter must parse
			// back to the ORIGINAL string.
			var got struct {
				Description string `yaml:"description"`
			}
			doc := "description: " + escaped + "\n"
			assert.NoError(t, yaml.Unmarshal([]byte(doc), &got), "emitted frontmatter must parse: %q", doc)
			assert.Equal(t, tc.in, got.Description,
				"the value must survive a real YAML round trip; got %q from %q", got.Description, doc)

			// And when it DOES quote, the escaping must be the json.Marshal
			// escaping the sibling quoter uses — one algorithm, not two.
			if strings.HasPrefix(escaped, `"`) {
				assert.Equal(t, yamlDoubleQuoted(tc.in), escaped,
					"two quoters in one package must escape identically once both have decided to quote")
			}
		})
	}
}
