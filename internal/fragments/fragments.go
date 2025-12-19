package fragments

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cbroglie/mustache"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"

	"mlcm/internal/logging"
)

// Fragment represents a parsed context fragment file.
// Variables can be used across fragments and in prompts using mustache syntax: {{variable_name}}
type Fragment struct {
	Name      string
	Context   string
	Variables map[string]string
}

// Loader finds and loads context fragments from .mlcm directories.
type Loader struct {
	searchDirs       []string
	warnFunc         func(string)
	suppressWarnings bool
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithWarnFunc sets a function to call when warnings occur.
func WithWarnFunc(fn func(string)) LoaderOption {
	return func(l *Loader) {
		l.warnFunc = fn
	}
}

// WithSuppressWarnings disables warning output.
func WithSuppressWarnings(suppress bool) LoaderOption {
	return func(l *Loader) {
		l.suppressWarnings = suppress
	}
}

// NewLoader creates a new fragment loader with the given search directories.
// Directories are searched in order; first match wins.
func NewLoader(searchDirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs: searchDirs,
		warnFunc: func(msg string) {
			fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
		},
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func (l *Loader) warn(msg string) {
	if !l.suppressWarnings && l.warnFunc != nil {
		l.warnFunc(msg)
	}
}

// Find locates a fragment by name across all search directories.
// Returns the full path to the fragment file.
//
// Naming conventions supported:
//   - Slash paths: "testing/tdd" finds "testing/tdd.md" (forward or back slashes)
//   - Basename only: "tdd" finds it in any subdirectory (first match wins)
func (l *Loader) Find(name string) (string, error) {
	// Normalize path separators for cross-platform support
	name = filepath.FromSlash(name)

	candidates := []string{
		name,
		name + ".md",
	}

	// First try direct path lookup (including subdirectory paths)
	for _, dir := range l.searchDirs {
		for _, candidate := range candidates {
			path := filepath.Join(dir, candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}

	// If not found directly, walk directories to find by basename
	baseName := filepath.Base(name)
	baseNameMd := baseName + ".md"

	for _, dir := range l.searchDirs {
		var found string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			fileName := d.Name()
			if fileName == baseName || fileName == baseNameMd {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}

	return "", fmt.Errorf("fragment not found: %s", name)
}

// Load reads and parses a fragment by name.
func (l *Loader) Load(name string) (*Fragment, error) {
	path, err := l.Find(name)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read fragment %s: %w", name, err)
	}

	frag, err := parseMarkdownFragment(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fragment %s: %w", name, err)
	}
	frag.Name = name

	return frag, nil
}

// parseMarkdownFragment parses a markdown fragment file using goldmark.
// Expected format:
//
//	## Context
//	Content here with {{variables}}
//
//	## Variables
//	```yaml
//	project_name: SCM
//	language: Go
//	```
func parseMarkdownFragment(source []byte) (*Fragment, error) {
	frag := &Fragment{
		Variables: make(map[string]string),
	}

	// Parse the markdown into an AST
	parser := goldmark.DefaultParser()
	reader := text.NewReader(source)
	doc := parser.Parse(reader)

	// Collect h2 headings with their byte positions
	type headingInfo struct {
		name         string
		contentStart int // Position after the heading line
		headingStart int // Position where the heading starts
	}
	var headings []headingInfo

	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		if heading, ok := n.(*ast.Heading); ok && heading.Level == 2 {
			// Get heading text
			var headingText bytes.Buffer
			for child := heading.FirstChild(); child != nil; child = child.NextSibling() {
				if textNode, ok := child.(*ast.Text); ok {
					headingText.Write(textNode.Segment.Value(source))
				}
			}

			name := strings.ToLower(strings.TrimSpace(headingText.String()))

			// Find where the heading starts and where content begins
			// ATX headings don't have Lines(), so we need to find position from children
			headingStart := len(source)
			contentStart := 0

			// The heading node itself tracks its position via first child segment
			if heading.FirstChild() != nil {
				if textNode, ok := heading.FirstChild().(*ast.Text); ok {
					// Heading starts a few chars before the text (for "## ")
					headingStart = textNode.Segment.Start - heading.Level - 1
					if headingStart < 0 {
						headingStart = 0
					}
				}
			}

			// Content starts after the heading - find next newline after heading text
			if heading.FirstChild() != nil {
				if textNode, ok := heading.FirstChild().(*ast.Text); ok {
					contentStart = textNode.Segment.Stop
					// Skip to end of line
					for contentStart < len(source) && source[contentStart] != '\n' {
						contentStart++
					}
					if contentStart < len(source) {
						contentStart++ // Skip the newline
					}
				}
			}

			headings = append(headings, headingInfo{
				name:         name,
				headingStart: headingStart,
				contentStart: contentStart,
			})
		}

		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}

	// Extract section contents
	sections := make(map[string][]byte)
	for i, h := range headings {
		var contentEnd int
		if i+1 < len(headings) {
			contentEnd = headings[i+1].headingStart
		} else {
			contentEnd = len(source)
		}

		if h.contentStart <= contentEnd {
			content := bytes.TrimSpace(source[h.contentStart:contentEnd])
			sections[h.name] = content
		}
	}

	// Process context section
	if contextContent, ok := sections["context"]; ok {
		frag.Context = string(contextContent)
	}

	// Process variables section
	if varsContent, ok := sections["variables"]; ok {
		vars, err := parseVariables(varsContent)
		if err != nil {
			return nil, fmt.Errorf("failed to parse variables: %w", err)
		}
		frag.Variables = vars
	}

	// If no sections found, treat entire content as context
	if len(sections) == 0 {
		frag.Context = strings.TrimSpace(string(source))
	}

	return frag, nil
}

// parseVariables extracts variables from a Variables section.
// Handles both fenced code blocks and raw YAML/JSON.
func parseVariables(content []byte) (map[string]string, error) {
	vars := make(map[string]string)

	// Parse the content to find code blocks
	parser := goldmark.DefaultParser()
	reader := text.NewReader(content)
	doc := parser.Parse(reader)

	var codeContent []byte

	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		if fenced, ok := n.(*ast.FencedCodeBlock); ok {
			// Extract code block content
			var buf bytes.Buffer
			lines := fenced.Lines()
			for i := 0; i < lines.Len(); i++ {
				line := lines.At(i)
				buf.Write(line.Value(content))
			}
			codeContent = buf.Bytes()
			return ast.WalkStop, nil
		}

		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}

	// Use code block content if found, otherwise use raw content
	parseContent := codeContent
	if parseContent == nil {
		parseContent = content
	}

	// Parse as YAML (which also handles JSON)
	var rawVars map[string]interface{}
	if err := yaml.Unmarshal(parseContent, &rawVars); err != nil {
		return nil, fmt.Errorf("invalid YAML/JSON: %w", err)
	}

	// Convert to string map
	for k, v := range rawVars {
		vars[k] = fmt.Sprintf("%v", v)
	}

	return vars, nil
}

// LoadMultiple loads multiple fragments, merges their variables,
// concatenates their context, and applies mustache templating.
func (l *Loader) LoadMultiple(names []string) (string, error) {
	return l.LoadMultipleWithVars(names, nil)
}

// LoadMultipleWithVars loads multiple fragments with additional variables.
// The extraVars are merged first, then fragment variables override them.
// Missing fragments are warned about but do not cause failure.
func (l *Loader) LoadMultipleWithVars(names []string, extraVars map[string]string) (string, error) {
	var frags []*Fragment

	// Load all fragments
	for _, name := range names {
		frag, err := l.Load(name)
		if err != nil {
			l.warn(fmt.Sprintf("fragment not found: %s", name))
			continue
		}
		frags = append(frags, frag)
	}

	// Start with extra vars (persona vars)
	variables := make(map[string]string)
	variableSource := make(map[string]string)
	for k, v := range extraVars {
		variables[k] = v
		variableSource[k] = "(persona)"
	}

	// Merge fragment variables (override extra vars)
	for _, frag := range frags {
		for k, v := range frag.Variables {
			if existingSource, exists := variableSource[k]; exists {
				l.warn(fmt.Sprintf("variable %q redefined: was set in %q, overwritten by %q", k, existingSource, frag.Name))
			}
			variables[k] = v
			variableSource[k] = frag.Name
		}
	}

	// Assemble context intelligently
	assembled := l.assembleContext(frags)

	// Apply mustache templating
	rendered, err := l.applyTemplate(assembled, variables)
	if err != nil {
		return "", fmt.Errorf("failed to apply template: %w", err)
	}

	return rendered, nil
}

// assembleContext intelligently combines fragment contexts.
// Groups related content and adds clear section boundaries.
func (l *Loader) assembleContext(frags []*Fragment) string {
	if len(frags) == 0 {
		return ""
	}

	var sections []string
	for _, frag := range frags {
		if frag.Context == "" {
			continue
		}
		sections = append(sections, strings.TrimSpace(frag.Context))
	}

	return strings.Join(sections, "\n\n---\n\n")
}

// applyTemplate applies mustache templating to the context using variables.
// It warns about any variables referenced in the template that aren't defined.
func (l *Loader) applyTemplate(template string, vars map[string]string) (string, error) {
	// Find all variable references in the template
	varPattern := regexp.MustCompile(`\{\{\s*([^}#/!>\s][^}]*?)\s*\}\}`)
	matches := varPattern.FindAllStringSubmatch(template, -1)

	// Check for undefined variables
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) > 1 {
			varName := strings.TrimSpace(match[1])
			// Skip mustache section markers
			if strings.HasPrefix(varName, "#") ||
				strings.HasPrefix(varName, "/") ||
				strings.HasPrefix(varName, "!") ||
				strings.HasPrefix(varName, ">") {
				continue
			}
			if !seen[varName] {
				seen[varName] = true
				if _, exists := vars[varName]; !exists {
					l.warn(fmt.Sprintf("undefined variable: {{%s}}", varName))
					logging.L().Warn(logging.MsgVariableUnexpanded,
						logging.VariableName(varName))
				}
			}
		}
	}

	data := make(map[string]interface{})
	for k, v := range vars {
		data[k] = v
	}

	rendered, err := mustache.Render(template, data)
	if err != nil {
		return "", err
	}

	return rendered, nil
}

// List returns all available fragment names in the search directories.
// Walks subdirectories recursively. Fragment names include relative paths
// from the search directory (e.g., "testing/tdd" for "testing/tdd.md").
func (l *Loader) List() ([]FragmentInfo, error) {
	var fragments []FragmentInfo
	seen := make(map[string]bool)

	for _, dir := range l.searchDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}

			if d.IsDir() {
				// Skip hidden directories
				if strings.HasPrefix(d.Name(), ".") && path != dir {
					return filepath.SkipDir
				}
				return nil
			}

			name := d.Name()
			if !strings.HasSuffix(name, ".md") {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}

			// Get relative path from search dir
			relPath, err := filepath.Rel(dir, path)
			if err != nil {
				return nil
			}

			// Fragment name is relative path without .md extension
			fragName := strings.TrimSuffix(relPath, ".md")

			if !seen[fragName] {
				seen[fragName] = true
				fragments = append(fragments, FragmentInfo{
					Name:     fragName,
					FileName: name,
					Path:     path,
					Source:   dir,
				})
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to walk directory %s: %w", dir, err)
		}
	}

	return fragments, nil
}

// FragmentInfo holds metadata about a fragment.
type FragmentInfo struct {
	Name     string
	FileName string
	Path     string
	Source   string
}

// ParseMarkdown parses markdown content as a fragment.
// This is useful for parsing generator output.
func ParseMarkdown(content string) (*Fragment, error) {
	return parseMarkdownFragment([]byte(content))
}
