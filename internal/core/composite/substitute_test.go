package composite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubstituteVariables_Basic(t *testing.T) {
	content := "Hello {{name}}, welcome to {{place}}!"
	vars := map[string]string{
		"name":  "World",
		"place": "ctxloom",
	}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Equal(t, "Hello World, welcome to ctxloom!", result)
}

func TestSubstituteVariables_MissingVariable(t *testing.T) {
	content := "Hello {{name}}!"
	vars := map[string]string{} // No vars defined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "undefined variable")
}

func TestSubstituteVariables_EmptyContent(t *testing.T) {
	content := ""
	vars := map[string]string{"name": "test"}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Empty(t, result)
}

func TestSubstituteVariables_NoVariables(t *testing.T) {
	content := "No variables here"
	vars := map[string]string{}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Equal(t, "No variables here", result)
}

func TestSubstituteVariables_InvalidTemplate(t *testing.T) {
	// Unclosed section tag causes parse error
	content := "{{#section}}content without closing"
	vars := map[string]string{}

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	result := substituteVariables(content, vars, warnFunc)
	// Should return original content on parse error
	assert.Equal(t, content, result)
	// Should have logged a warning
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "failed to parse")
}

func TestSubstituteVariables_SectionTag(t *testing.T) {
	// Test section tags (type 3) - these also reference variables
	content := "{{#show}}visible{{/show}}"
	vars := map[string]string{} // No vars - "show" is undefined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about undefined "show" variable
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "undefined variable")
	assert.Contains(t, warnings[0], "show")
}

func TestSubstituteVariables_RawVariable(t *testing.T) {
	// Test raw variable tags (type 2) - {{{var}}} or {{&var}}
	content := "Raw: {{{name}}}"
	vars := map[string]string{"name": "<b>bold</b>"}

	result := substituteVariables(content, vars, func(s string) {})
	// Raw variables should not escape HTML
	assert.Contains(t, result, "<b>bold</b>")
}

func TestSubstituteVariables_NestedSectionWithVariables(t *testing.T) {
	// Test nested section tags with child variables - exercises recursive checkTags
	// The section "outer" is undefined, which triggers the recursive check
	content := "{{#outer}}Hello {{inner_name}}!{{/outer}}"
	vars := map[string]string{} // No vars defined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about at least the section variable
	require.GreaterOrEqual(t, len(warnings), 1, "should warn about undefined variables")
	assert.Contains(t, warnings[0], "outer")
}

func TestSubstituteVariables_InvertedSection(t *testing.T) {
	// Test inverted section tags (type 4) - {{^var}}content{{/var}}
	content := "{{^missing}}default content{{/missing}}"
	vars := map[string]string{} // "missing" is undefined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about undefined "missing" variable
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "missing")
}

func TestSubstituteVariables_DeeplyNestedSections(t *testing.T) {
	// Test deeply nested sections with variables at multiple levels
	// Exercises the recursive checkTags path for nested sections
	content := "{{#level1}}A{{#level2}}B{{deep_var}}{{/level2}}{{/level1}}"
	vars := map[string]string{}

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about at least the outermost undefined section
	require.GreaterOrEqual(t, len(warnings), 1, "should warn about undefined variables")
	assert.Contains(t, warnings[0], "level1")
}

// TestSubstituteVariables_UndefinedPlainVariableRendersVerbatim is the
// payload-asserting proof of the fixed contract: the
// real-world bug found via a live ACP client (Nori) was that a fragment
// documenting another {{...}}-flavored syntax (here, justfile's `{{TOP}}` /
// `{{ARGS}}` / `{{justfile_directory()}}`) collided with fragment
// templating — every such tag looks like a ctxloom variable reference, and
// the old contract ("undefined variable renders as empty") silently
// stripped the literal documentation text on its way to the engine. The
// DECIDED fix: an unresolved plain variable tag now renders as the literal
// text the author wrote, instead of vanishing. This asserts the actual
// rendered BYTES (not merely "no error" and not merely "a warning fired")
// so a regression that starts blanking the text again — even with the
// warning intact — fails this test.
//
// This was formerly TestSubstituteVariables_LiteralJustSyntaxIsCorrupted,
// which characterized the OLD (corrupting) behavior as by-design; that
// characterization is exactly what this task changes.
//
// The Set Delimiter escape (TestSubstituteVariables_SetDelimiterEscapesLiteralMustache
// below) remains the more surgical tool — it silences the warning entirely —
// but verbatim rendering is now the default safety net for authors who
// didn't use it.
func TestSubstituteVariables_UndefinedPlainVariableRendersVerbatim(t *testing.T) {
	content := "Run `just build`:\n```just\nbuild:\n    go build {{ARGS}}\n```\nSee {{TOP}} and {{justfile_directory()}}."
	vars := map[string]string{} // no ctxloom variables bound; this is unescaped prose

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// The fix: literal justfile syntax survives byte-for-byte in the
	// assembled output instead of vanishing.
	assert.Contains(t, result, "{{ARGS}}", "undefined plain variable must render verbatim, not vanish")
	assert.Contains(t, result, "{{TOP}}", "undefined plain variable must render verbatim, not vanish")
	assert.Contains(t, result, "{{justfile_directory()}}", "undefined plain variable must render verbatim, not vanish")
	assert.Equal(t,
		"Run `just build`:\n```just\nbuild:\n    go build {{ARGS}}\n```\nSee {{TOP}} and {{justfile_directory()}}.",
		result,
		"rendered output must be byte-identical to the input when nothing resolves")

	// The warnings are STILL real signal, not silenced by the verbatim fix:
	// they name exactly the tags that stayed unresolved, so the author can
	// still find and fix (or intentionally escape) them.
	assert.GreaterOrEqual(t, len(warnings), 3)
	joined := warnings[0] + warnings[1] + warnings[2]
	assert.Contains(t, joined, "ARGS")
	assert.Contains(t, joined, "TOP")
	assert.Contains(t, joined, "justfile_directory()")
}

// TestSubstituteVariables_SectionIdiomUnchangedByVerbatimFix PINS the
// section/inverted-section presence-toggle idiom against the verbatim-render
// fix above: an undefined SECTION name must still omit its body (falsy, key
// absent from the data map, exactly as before) and an undefined INVERTED
// SECTION name must still emit its body (falsy triggers the inverse), even
// though the fix now seeds literal text into the data map for undefined
// PLAIN variable tags. This is the documented, sanctioned idiom
// (`{{#DEBUG}}...{{/DEBUG}}`, `{{^DEBUG}}...{{/DEBUG}}`) used across existing
// profiles; seeding a literal (truthy, non-empty) string for a section name
// would silently flip every optional block's polarity. If this regresses,
// every profile relying on the idiom breaks silently.
func TestSubstituteVariables_SectionIdiomUnchangedByVerbatimFix(t *testing.T) {
	content := "{{#DEBUG}}debug on{{/DEBUG}}{{^DEBUG}}debug off{{/DEBUG}}"
	vars := map[string]string{} // DEBUG undefined

	result := substituteVariables(content, vars, func(s string) {})

	// Section body omitted (undefined section is falsy, same as before).
	assert.NotContains(t, result, "debug on")
	// Inverted-section body emitted (undefined section is falsy, so the
	// inverse still fires, same as before).
	assert.Contains(t, result, "debug off")
	// Crucially: the section name itself must NOT leak into the output as
	// literal "{{DEBUG}}" text either — that would mean the seeding
	// mistakenly treated the section tag as a plain variable.
	assert.NotContains(t, result, "{{DEBUG}}")
	assert.NotContains(t, result, "{{#DEBUG}}")
	assert.NotContains(t, result, "{{^DEBUG}}")
}

// TestSubstituteVariables_NameUsedAsBothSectionAndPlainVariableStaysFalsy
// documents the deliberate, defensive edge-case choice: if one name is used
// BOTH as a section/inverted-section tag AND as a plain variable tag
// somewhere in the same fragment, and it's undefined, the verbatim-render
// seeding is skipped for that name entirely — protecting the section idiom
// takes priority over verbatim rendering for the plain-variable use. This is
// a rare collision (typically a fragment would only use a name one way), but
// getting it wrong would risk flipping a section's polarity, which is the
// one behavior this task must never change.
func TestSubstituteVariables_NameUsedAsBothSectionAndPlainVariableStaysFalsy(t *testing.T) {
	content := "{{#DEBUG}}on{{/DEBUG}} plain:{{DEBUG}}"
	vars := map[string]string{} // DEBUG undefined

	result := substituteVariables(content, vars, func(s string) {})

	// The section is still falsy/omitted (idiom protected).
	assert.NotContains(t, result, "on")
	// The plain-variable occurrence is NOT seeded literal in this collision
	// case (it renders empty, same as the historical default) — a
	// conservative tradeoff documented here so it can't silently change.
	assert.NotContains(t, result, "plain:{{DEBUG}}")
	assert.Contains(t, result, "plain:")
}

// TestSubstituteVariables_SetDelimiterEscapesLiteralMustache is the
// payload-asserting proof that fragment authors already have a supported,
// zero-code-change way to write literal `{{...}}` syntax: Mustache's
// standard Set Delimiter tag (`{{=<% %>=}}` ... `<%={{ }}=%>`), which the
// underlying cbroglie/mustache library implements today. Content inside the
// escaped block is treated as plain text — never extracted as a Tag by
// checkTags, never warned about, and reaches the engine byte-for-byte —
// while a real ctxloom variable outside the escaped block still substitutes
// normally. This is the fix landed for this bug: documentation
// (docs/guides/templating.md) pointing fragment authors at working,
// pre-existing library behavior, proven here so a future change to
// checkTags/substituteVariables or a mustache library swap can't silently
// take this escape hatch away again.
func TestSubstituteVariables_SetDelimiterEscapesLiteralMustache(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"Use `{{ARGS}}` to forward recipe arguments; `{{justfile_directory()}}`\n" +
		"is scoped to the local file, not a composing parent. See {{TOP}}.\n" +
		"<%={{ }}=%>\n" +
		"Real variable: {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// The literal justfile syntax survives byte-for-byte inside the escaped block.
	assert.Contains(t, result, "Use `{{ARGS}}` to forward recipe arguments; `{{justfile_directory()}}`")
	assert.Contains(t, result, "is scoped to the local file, not a composing parent. See {{TOP}}.")
	// The real ctxloom variable outside the escape still substitutes.
	assert.Contains(t, result, "Real variable: World")
	// No spurious warnings: the escaped tags were never treated as undefined
	// ctxloom variables in the first place.
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_RealVariableOutsideEscapedBlockStillSubstitutes
// isolates the specific claim docs/guides/templating.md makes about the Set
// Delimiter escape ("Real ctxloom variables outside the escaped block still
// substitute normally"): a real variable placed both before and after an
// escaped block substitutes in both places, while the escaped block's own
// content never does, in one fragment.
func TestSubstituteVariables_RealVariableOutsideEscapedBlockStillSubstitutes(t *testing.T) {
	content := "Before: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"literal {{name}} stays literal\n" +
		"<%={{ }}=%>\n" +
		"After: {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "Before: World")
	assert.Contains(t, result, "literal {{name}} stays literal")
	assert.Contains(t, result, "After: World")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_MultipleSetDelimiterBlocksInOneFragment proves the
// escape is not a one-shot toggle: a fragment can open and close the Set
// Delimiter tag more than once, with real substitution resuming correctly in
// between and after every escaped block.
func TestSubstituteVariables_MultipleSetDelimiterBlocksInOneFragment(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"first literal {{ARGS}}\n" +
		"<%={{ }}=%>\n" +
		"middle real: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"second literal {{TOP}}\n" +
		"<%={{ }}=%>\n" +
		"end"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "first literal {{ARGS}}")
	assert.Contains(t, result, "middle real: World")
	assert.Contains(t, result, "second literal {{TOP}}")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_SetDelimiterEscapesEvenRealVariableNames proves the
// escape is a lexical switch, not a name-based one: a literal tag inside an
// escaped block that happens to spell a genuinely bound ctxloom variable name
// is NOT substituted (it is never tokenized as a tag at all), while the exact
// same name outside the block substitutes normally. This matters because a
// fragment documenting another {{...}}-flavored language can easily collide
// on a short, common name (ARGS, NAME, ID); the escape must win regardless.
func TestSubstituteVariables_SetDelimiterEscapesEvenRealVariableNames(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"literal {{ARGS}}\n" +
		"<%={{ }}=%>\n" +
		"real: {{ARGS}}"
	vars := map[string]string{"ARGS": "real-value"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "literal {{ARGS}}")
	assert.Contains(t, result, "real: real-value")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_UnclosedSetDelimiterSwallowsRestOfFragment
// characterizes a genuinely surprising failure mode of a MALFORMED escape: a
// `{{=<% %>=}}` opening tag with no matching `<%={{ }}=%>` closing tag does
// not error, and does not merely leave the rest of the raw text untouched —
// it silently disables ALL further variable substitution for the remainder
// of the fragment, including a real, bound ctxloom variable that appears
// after the unclosed tag. That variable is left as literal `{{name}}` text
// in the rendered output (not substituted, not warned about, not blanked —
// just silently skipped), while content BEFORE the unclosed tag still
// substitutes normally. A fragment author who forgets the closing tag gets
// no signal that every variable reference downstream of the typo stopped
// working. This is a foot-gun in the standard library behavior, not
// something ctxloom's code introduces — documented here so a future change
// cannot silently make it worse (e.g. by starting to blank that trailing
// content instead of leaving it literal) without this test forcing a look.
func TestSubstituteVariables_UnclosedSetDelimiterSwallowsRestOfFragment(t *testing.T) {
	content := "Before: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"literal {{ARGS}}\n" +
		"After (never re-escaped): {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// Content before the malformed tag is unaffected.
	assert.Contains(t, result, "Before: World")
	// Content after the unclosed tag — including a real, bound variable
	// reference — is left as literal, unsubstituted text: the missing close
	// tag never handed control back to normal Mustache parsing.
	assert.Contains(t, result, "literal {{ARGS}}")
	assert.Contains(t, result, "After (never re-escaped): {{name}}")
	assert.NotContains(t, result, "After (never re-escaped): World")
	// No warning fires for the swallowed {{name}} reference: this is not
	// treated as an undefined variable, so checkTags never sees a tag to
	// warn about in the first place — the failure is invisible by default.
	assert.Empty(t, warnings)
}
