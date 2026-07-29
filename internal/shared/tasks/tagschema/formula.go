package tagschema

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// placeholderPattern matches one {{...}} mustache placeholder in a
// priority_fn/decay_fn formula string, e.g. "{{triage:impact}}" or
// "{{age_days}}". The captured group is the placeholder's (trimmed) name,
// exactly as written between the braces.
var placeholderPattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*}}`)

// Placeholders returns the name of every {{...}} placeholder in src, in
// source order, with duplicates kept. This package owns the placeholder
// syntax because CompileFormula is what rewrites it; any other package that
// needs to know which names a formula references must ask here, so that
// changing the syntax cannot leave an inspector silently matching nothing.
//
// A name is returned exactly as written between the braces minus surrounding
// whitespace, so a value-qualified tag reference arrives as "ns:key=value"
// and the caller owns splitting it. Returns nil when src has none.
func Placeholders(src string) []string {
	ms := placeholderPattern.FindAllStringSubmatch(src, -1)
	if ms == nil {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// formulaEnv is the fixed shape expr type-checks every compiled Formula
// against. A placeholder becomes a call to one of these two functions at
// Compile time (see CompileFormula); Eval supplies the actual closures for
// one evaluation.
//
//   - Tag(name) — a "{{ns:key}}" placeholder (its name contains ":"): the
//     task's value for that tag.
//   - Builtin(name) — any other placeholder (e.g. "{{age_days}}"): a
//     taskloom-provided named datum. See internal/shared/tasks/priority's
//     builtin set for what's available and how it's extended.
//
// Both take the placeholder's NAME — config-authored text that is baked into
// the compiled expression's source at Compile time — and return a float64
// resolved at Run time by whatever closures the caller supplies. A task's tag
// VALUES are never substituted into the expression text itself: they only
// ever flow through these functions' return values, so no tag value (however
// arithmetic-looking) can inject or alter the formula's structure.
type formulaEnv struct {
	Tag     func(string) float64
	Builtin func(string) float64
}

// Formula is a priority_fn/decay_fn string compiled once via CompileFormula
// and safe to Eval repeatedly (once per task) without recompiling.
type Formula struct {
	program *vm.Program
	source  string
}

// CompileFormula parses raw's {{...}} placeholders and compiles the result to
// an expr program. A placeholder whose name contains ":" (e.g.
// "triage:impact") is a tag reference and is rewritten to a Tag("triage:impact")
// call; any other placeholder (e.g. "age_days") is a built-in reference and
// is rewritten to a Builtin("age_days") call. Everything outside a
// placeholder is left as ordinary expr arithmetic syntax (+, -, *, /, parens,
// numeric literals), so e.g.
//
//	"{{triage:impact}} * (1 + 0.25*{{triage:regression}}) * {{age_factor}}"
//
// compiles as
//
//	Tag("triage:impact") * (1 + 0.25*Tag("triage:regression")) * Builtin("age_factor")
//
// A malformed expression (bad syntax, unbalanced parens, an operator expr
// doesn't recognize) is a returned error naming both the original and the
// rewritten source, so a config author can see exactly what their mustache
// form expanded to.
func CompileFormula(raw string) (*Formula, error) {
	rewritten := placeholderPattern.ReplaceAllStringFunc(raw, func(m string) string {
		name := placeholderPattern.FindStringSubmatch(m)[1]
		if strings.Contains(name, ":") {
			return fmt.Sprintf("Tag(%q)", name)
		}
		return fmt.Sprintf("Builtin(%q)", name)
	})
	program, err := expr.Compile(rewritten, expr.Env(formulaEnv{}), expr.AsFloat64())
	if err != nil {
		return nil, fmt.Errorf("tag_schema: formula %q (compiled %q): %w", raw, rewritten, err)
	}
	return &Formula{program: program, source: raw}, nil
}

// Eval runs f for one evaluation (typically one task), resolving every
// Tag("ns:key") call through tag and every Builtin("name") call through
// builtin. Neither may be nil. The caller owns the resolution policy —
// internal/shared/tasks/priority's tag resolver treats an absent tag as 0
// and a valueless (modifier) tag as 1, and assembles the builtin map from
// age/decay data — this package only bridges mustache syntax to expr.
//
// Returns an error if expr's own Run fails, or (unreachable in practice,
// since CompileFormula's expr.AsFloat64() already coerces the result at
// compile time, but checked here rather than silently type-asserted) the
// program somehow yields a non-float64.
func (f *Formula) Eval(tag, builtin func(string) float64) (float64, error) {
	out, err := expr.Run(f.program, formulaEnv{Tag: tag, Builtin: builtin})
	if err != nil {
		return 0, fmt.Errorf("tag_schema: eval formula %q: %w", f.source, err)
	}
	v, ok := out.(float64)
	if !ok {
		return 0, fmt.Errorf("tag_schema: formula %q produced non-numeric result %v", f.source, out)
	}
	return v, nil
}

// Source returns f's original (mustache-form) formula text, for diagnostics.
func (f *Formula) Source() string { return f.source }
