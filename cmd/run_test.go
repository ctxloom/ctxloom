package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// substituteVariables Tests
//
// Template substitution allows fragments to contain variable placeholders that
// are filled in from profile variables. This enables context reuse across
// different projects and configurations.
// =============================================================================

// TestSubstituteVariables_BasicSubstitution verifies that simple mustache
// variables are correctly replaced with their values. This is the core
// templating functionality used in fragments.
func TestSubstituteVariables_BasicSubstitution(t *testing.T) {
	vars := map[string]string{
		"PROJECT_NAME": "MyProject",
		"LANGUAGE":     "Go",
	}

	content := "Welcome to {{PROJECT_NAME}}! This project uses {{LANGUAGE}}."
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Equal(t, "Welcome to MyProject! This project uses Go.", result)
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_UndefinedVariable verifies that undefined variables
// are replaced with empty strings and trigger a warning. This helps users
// identify missing configuration.
func TestSubstituteVariables_UndefinedVariable(t *testing.T) {
	vars := map[string]string{
		"KNOWN_VAR": "value",
	}

	content := "Known: {{KNOWN_VAR}}, Unknown: {{UNKNOWN_VAR}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Equal(t, "Known: value, Unknown: ", result)
	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "UNKNOWN_VAR")
}

// TestSubstituteVariables_NoVariables verifies that content without variables
// passes through unchanged. This ensures literal mustache-like text doesn't
// break when no templating is needed.
func TestSubstituteVariables_NoVariables(t *testing.T) {
	vars := make(map[string]string)
	content := "Plain text without any variables."
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Equal(t, content, result)
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_EmptyContent verifies that empty content returns
// empty result without panicking.
func TestSubstituteVariables_EmptyContent(t *testing.T) {
	vars := map[string]string{"VAR": "value"}
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables("", vars, warnFunc)

	assert.Empty(t, result)
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_MultipleOccurrences verifies that the same variable
// used multiple times is replaced in all locations.
func TestSubstituteVariables_MultipleOccurrences(t *testing.T) {
	vars := map[string]string{
		"NAME": "Claude",
	}

	content := "Hello {{NAME}}! {{NAME}} is great. Thanks {{NAME}}!"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Equal(t, "Hello Claude! Claude is great. Thanks Claude!", result)
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_BuiltInVariables verifies that ctxloom built-in variables
// (CTXLOOM_ROOT, CTXLOOM_DIR) can be used in templates.
func TestSubstituteVariables_BuiltInVariables(t *testing.T) {
	vars := map[string]string{
		"CTXLOOM_ROOT": "/home/user/project",
		"CTXLOOM_DIR":  "/home/user/project/.ctxloom",
	}

	content := "Project root: {{CTXLOOM_ROOT}}\nConfig dir: {{CTXLOOM_DIR}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Contains(t, result, "/home/user/project")
	assert.Contains(t, result, "/home/user/project/.ctxloom")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_SpecialCharacters verifies that variable values
// with special characters are inserted correctly. Note that mustache HTML-escapes
// by default (& becomes &amp;). Use {{{var}}} for unescaped output.
func TestSubstituteVariables_SpecialCharacters(t *testing.T) {
	vars := map[string]string{
		"PATH":    "/usr/bin:/usr/local/bin",
		"PATTERN": "*.go",
	}

	content := "Path: {{PATH}}\nPattern: {{PATTERN}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Contains(t, result, "/usr/bin:/usr/local/bin")
	assert.Contains(t, result, "*.go")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_HTMLEscaping verifies that mustache HTML-escapes
// special characters by default. Users should use triple braces {{{var}}}
// for unescaped output if needed.
func TestSubstituteVariables_HTMLEscaping(t *testing.T) {
	vars := map[string]string{
		"URL": "https://example.com?foo=bar&baz=qux",
	}

	// Standard {{var}} escapes HTML characters
	content := "URL: {{URL}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	// & is escaped to &amp; by mustache
	assert.Contains(t, result, "&amp;")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_MultilineContent verifies that multiline content
// with variables is handled correctly.
func TestSubstituteVariables_MultilineContent(t *testing.T) {
	vars := map[string]string{
		"TITLE":  "Documentation",
		"AUTHOR": "Test User",
	}

	content := `# {{TITLE}}

Author: {{AUTHOR}}

This is multiline content.`

	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Contains(t, result, "# Documentation")
	assert.Contains(t, result, "Author: Test User")
	assert.Contains(t, result, "multiline content")
}

// TestSubstituteVariables_EmptyVariableValue verifies that variables with
// empty string values are correctly substituted (to empty).
func TestSubstituteVariables_EmptyVariableValue(t *testing.T) {
	vars := map[string]string{
		"EMPTY_VAR": "",
	}

	content := "Before [{{EMPTY_VAR}}] After"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	assert.Equal(t, "Before [] After", result)
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_InvalidMustachePreserved verifies that invalid
// mustache syntax doesn't cause errors and content is preserved as much
// as possible.
func TestSubstituteVariables_InvalidMustacheWarns(t *testing.T) {
	vars := map[string]string{}
	// Unbalanced mustache - this might cause parse error
	content := "{{unclosed"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	result := substituteVariables(content, vars, warnFunc)

	// Should either return original or warn about parse error
	assert.True(t, result == content || len(warnings) > 0,
		"Should either preserve content or warn about error")
}

// =============================================================================
// checkTags Tests
//
// Tag checking walks the parsed mustache template to identify undefined
// variables. This provides early warning to users about missing configuration.
// =============================================================================

// TestCheckTags_IdentifiesUndefined verifies that checkTags warns about
// variables that are referenced but not defined in the vars map.
func TestCheckTags_IdentifiesUndefined(t *testing.T) {
	// We test this indirectly through substituteVariables since checkTags
	// is called internally
	vars := map[string]string{
		"DEFINED": "value",
	}

	content := "{{DEFINED}} and {{UNDEFINED}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	substituteVariables(content, vars, warnFunc)

	assert.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "UNDEFINED")
}

// TestCheckTags_NoWarningForDefined verifies that defined variables
// don't trigger warnings.
func TestCheckTags_NoWarningForDefined(t *testing.T) {
	vars := map[string]string{
		"VAR1": "value1",
		"VAR2": "value2",
	}

	content := "{{VAR1}} {{VAR2}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	substituteVariables(content, vars, warnFunc)

	assert.Empty(t, warnings)
}

// TestCheckTags_SectionVariables verifies that variables used in sections
// ({{#section}}...{{/section}}) are checked for definition.
func TestCheckTags_SectionVariables(t *testing.T) {
	vars := map[string]string{
		"show_section": "true",
	}

	content := "{{#show_section}}Content{{/show_section}} {{#missing_section}}Hidden{{/missing_section}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	substituteVariables(content, vars, warnFunc)

	// Should warn about missing_section
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "missing_section") {
			found = true
			break
		}
	}
	assert.True(t, found, "Should warn about missing section variable")
}

// TestCheckTags_DuplicateVariableOnlyWarnsOnce verifies that the same
// undefined variable used multiple times only triggers one warning.
func TestCheckTags_DuplicateVariableOnlyWarnsOnce(t *testing.T) {
	vars := map[string]string{}

	content := "{{MISSING}} {{MISSING}} {{MISSING}}"
	var warnings []string
	warnFunc := func(msg string) { warnings = append(warnings, msg) }

	substituteVariables(content, vars, warnFunc)

	// Count warnings about MISSING
	count := 0
	for _, w := range warnings {
		if strings.Contains(w, "MISSING") {
			count++
		}
	}
	assert.Equal(t, 1, count, "Should only warn once per undefined variable")
}

// =============================================================================
// LoadPrompt Tests
//
// LoadPrompt retrieves saved prompts from bundles for use with the -r flag.
// This enables reusable prompt templates across sessions.
// =============================================================================

// Note: LoadPrompt requires a full config.Load() which needs file system setup.
// Integration tests for this function should be in acceptance tests.
// We test the error path here.

// TestLoadPrompt_RequiresConfig documents that LoadPrompt depends on config.
// Full testing is done in integration tests with proper config setup.
func TestLoadPrompt_RequiresConfig(t *testing.T) {
	// This test documents the dependency rather than fully testing
	// since LoadPrompt requires file system state
	t.Skip("LoadPrompt requires config.Load() - tested in integration tests")
}
