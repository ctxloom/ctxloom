// Package markdown provides utilities for building markdown documents.
package markdown

import (
	"fmt"
	"strings"
)

// Builder constructs markdown documents programmatically.
type Builder struct {
	sections []section
}

type section struct {
	level   int
	heading string
	content []string
}

// New creates a new markdown Builder.
func New() *Builder {
	return &Builder{}
}

// H1 adds a level 1 heading.
func (b *Builder) H1(text string) *Builder {
	b.sections = append(b.sections, section{level: 1, heading: text})
	return b
}

// H2 adds a level 2 heading.
func (b *Builder) H2(text string) *Builder {
	b.sections = append(b.sections, section{level: 2, heading: text})
	return b
}

// H3 adds a level 3 heading.
func (b *Builder) H3(text string) *Builder {
	b.sections = append(b.sections, section{level: 3, heading: text})
	return b
}

// P adds a paragraph to the current section.
func (b *Builder) P(text string) *Builder {
	if len(b.sections) == 0 {
		b.sections = append(b.sections, section{})
	}
	b.sections[len(b.sections)-1].content = append(
		b.sections[len(b.sections)-1].content,
		text,
	)
	return b
}

// Text adds text without paragraph wrapping.
func (b *Builder) Text(text string) *Builder {
	return b.P(text)
}

// Bullet adds a bullet point.
func (b *Builder) Bullet(text string) *Builder {
	return b.P("- " + text)
}

// BulletBold adds a bullet with bold label and value.
func (b *Builder) BulletBold(label, value string) *Builder {
	return b.P(fmt.Sprintf("- **%s**: %s", label, value))
}

// CodeBlock adds a fenced code block.
func (b *Builder) CodeBlock(language, content string) *Builder {
	block := fmt.Sprintf("```%s\n%s\n```", language, content)
	return b.P(block)
}

// Bold returns bold text.
func Bold(text string) string {
	return "**" + text + "**"
}

// Italic returns italic text.
func Italic(text string) string {
	return "*" + text + "*"
}

// Code returns inline code.
func Code(text string) string {
	return "`" + text + "`"
}

// Link returns a markdown link.
func Link(text, url string) string {
	return fmt.Sprintf("[%s](%s)", text, url)
}

// String renders the markdown document.
func (b *Builder) String() string {
	var parts []string

	for _, s := range b.sections {
		var sectionParts []string

		// Add heading if present
		if s.heading != "" {
			prefix := strings.Repeat("#", s.level)
			sectionParts = append(sectionParts, prefix+" "+s.heading)
		}

		// Add content
		if len(s.content) > 0 {
			sectionParts = append(sectionParts, strings.Join(s.content, "\n"))
		}

		if len(sectionParts) > 0 {
			parts = append(parts, strings.Join(sectionParts, "\n\n"))
		}
	}

	return strings.Join(parts, "\n\n") + "\n"
}

// FragmentBuilder builds context fragment documents with Context and Variables sections.
type FragmentBuilder struct {
	context   *Builder
	variables map[string]string
}

// NewFragment creates a new fragment builder.
func NewFragment() *FragmentBuilder {
	return &FragmentBuilder{
		context:   New(),
		variables: make(map[string]string),
	}
}

// Context returns the context section builder.
func (f *FragmentBuilder) Context() *Builder {
	return f.context
}

// SetVar sets a variable.
func (f *FragmentBuilder) SetVar(key, value string) *FragmentBuilder {
	f.variables[key] = value
	return f
}

// SetVars sets multiple variables.
func (f *FragmentBuilder) SetVars(vars map[string]string) *FragmentBuilder {
	for k, v := range vars {
		f.variables[k] = v
	}
	return f
}

// String renders the complete fragment document.
func (f *FragmentBuilder) String() string {
	doc := New()

	// Context section
	doc.H2("Context")
	for _, s := range f.context.sections {
		for _, c := range s.content {
			doc.P(c)
		}
	}

	// Variables section
	if len(f.variables) > 0 {
		var yamlLines []string
		for k, v := range f.variables {
			yamlLines = append(yamlLines, fmt.Sprintf("%s: %s", k, v))
		}
		doc.H2("Variables")
		doc.CodeBlock("yaml", strings.Join(yamlLines, "\n"))
	}

	return doc.String()
}
