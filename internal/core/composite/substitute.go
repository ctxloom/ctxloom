package composite

import (
	"fmt"

	"github.com/cbroglie/mustache"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// substituteVariables applies mustache variable substitution to a fragment.
//
// An unresolved PLAIN variable tag renders VERBATIM — the literal source
// text the author wrote (`{{ARGS}}` stays `{{ARGS}}`) — instead of
// vanishing, and is reported through report; verbatim rendering makes the
// mistake visible in the output itself. See undefinedPlainVariableLiterals
// for the mechanism and the section/inverted-section idiom it deliberately
// leaves untouched. A template that does not parse or render is served as
// written and reported.
func substituteVariables(content string, vars map[string]string, report func(string)) string {
	tmpl, err := mustache.ParseString(content)
	if err != nil {
		report(fmt.Sprintf("failed to parse template: %v", err))
		return content
	}

	checkTags(tmpl.Tags(), vars, collections.NewSet[string](), report)

	data := make(map[string]interface{}, len(vars))
	for k, v := range vars {
		data[k] = v
	}
	// Pre-seed the data map with the literal source text for any name found
	// undefined AND used only as a plain variable tag — never for a name used
	// as a section/inverted-section anywhere in the template, which must stay
	// governed by key-absence so the presence-toggle idiom is untouched.
	for name, literal := range undefinedPlainVariableLiterals(tmpl.Tags(), vars) {
		data[name] = literal
	}

	rendered, err := tmpl.Render(data)
	if err != nil {
		report(fmt.Sprintf("failed to render template: %v", err))
		return content
	}
	return rendered
}

// undefinedPlainVariableLiterals walks the parsed tag tree and returns, for
// every name that is undefined in vars and used as a plain VARIABLE tag
// (never as a SECTION or INVERTED_SECTION anywhere in the tree), the literal
// source text to substitute verbatim: "{{name}}". This covers {{name}},
// {{{name}}}, and {{&name}} alike — the library's Tag.Type() does not
// distinguish raw output from escaped output, so "{{name}}" is used
// uniformly.
//
// The section exclusion is deliberately tree-wide and by name, not by
// occurrence: a name that appears as a section/inverted-section tag
// ANYWHERE is dropped from the result entirely. Seeding a non-empty literal
// for that name would make mustache treat it as truthy, silently flipping
// the polarity of every `{{#name}}...{{/name}}` / `{{^name}}...{{/name}}`
// block using it — a sanctioned idiom (e.g. `{{#DEBUG}}...{{/DEBUG}}`). In
// that rare collision the plain-variable occurrence renders empty.
func undefinedPlainVariableLiterals(tags []mustache.Tag, vars map[string]string) map[string]string {
	sectionNames := collections.NewSet[string]()
	literals := make(map[string]string)

	var walk func([]mustache.Tag)
	walk = func(tags []mustache.Tag) {
		for _, tag := range tags {
			name := tag.Name()
			switch tag.Type() {
			case mustache.Section, mustache.InvertedSection:
				sectionNames.Add(name)
			case mustache.Variable:
				if _, ok := vars[name]; !ok {
					if _, already := literals[name]; !already {
						literals[name] = "{{" + name + "}}"
					}
				}
			}
			if hasChildTags(tag.Type()) {
				if children := tag.Tags(); len(children) > 0 {
					walk(children)
				}
			}
		}
	}
	walk(tags)

	for name := range sectionNames {
		delete(literals, name)
	}
	return literals
}

// hasChildTags reports whether a tag type can contain nested tags (sections).
func hasChildTags(t mustache.TagType) bool {
	return t == mustache.Section || t == mustache.InvertedSection
}

// checkTags recursively walks mustache tags to find undefined variables,
// reporting each name once.
func checkTags(tags []mustache.Tag, vars map[string]string, seen collections.Set[string], report func(string)) {
	for _, tag := range tags {
		name := tag.Name()
		tagType := tag.Type()
		// A tag references a variable name when it is a plain variable
		// (which covers both escaped {{name}} and raw {{{name}}}/{{&name}})
		// or a section tag, which keys off a variable.
		referencesVariable := tagType == mustache.Variable || tagType == mustache.Section || tagType == mustache.InvertedSection
		if referencesVariable && !seen.Has(name) {
			seen.Add(name)
			if _, exists := vars[name]; !exists {
				report(fmt.Sprintf("undefined variable: {{%s}}", name))
			}
		}
		if hasChildTags(tagType) {
			if children := tag.Tags(); len(children) > 0 {
				checkTags(children, vars, seen, report)
			}
		}
	}
}
