// Package prompts provides YAML-based prompt loading and tag-based filtering.
package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/collections"
)

// Prompt represents a YAML-based prompt with metadata and content.
type Prompt struct {
	Name      string   `yaml:"-"`                    // Set from filename, not in YAML
	Tags      []string `yaml:"tags,omitempty"`       // Tags for filtering/categorization
	Variables []string `yaml:"variables,omitempty"`  // Variable names used in content
	Content   string   `yaml:"content"`              // Markdown content
}

// PromptInfo holds metadata about a prompt file.
type PromptInfo struct {
	Name      string
	Tags      []string
	Variables []string
	Path      string
	Source    string
}

// Loader finds and loads prompts from directories.
type Loader struct {
	searchDirs []string
	fs         afero.Fs
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// NewLoader creates a new prompt loader with the given search directories.
func NewLoader(searchDirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs: searchDirs,
		fs:         afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Find locates a prompt by name across all search directories.
// Supports both .yaml and .md extensions for backwards compatibility.
func (l *Loader) Find(name string) (string, error) {
	name = filepath.FromSlash(name)

	// Try YAML first, then markdown for backwards compatibility
	extensions := []string{".yaml", ".yml", ".md"}

	for _, dir := range l.searchDirs {
		for _, ext := range extensions {
			// Try with extension
			path := filepath.Join(dir, name+ext)
			if info, err := l.fs.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
			// Try without adding extension (in case name already has one)
			path = filepath.Join(dir, name)
			if info, err := l.fs.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}

	// Walk directories to find by basename
	baseName := filepath.Base(name)
	for _, dir := range l.searchDirs {
		var found string
		_ = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			fileName := info.Name()
			fileBase := strings.TrimSuffix(fileName, filepath.Ext(fileName))
			if fileBase == baseName || fileName == baseName {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}

	return "", fmt.Errorf("prompt not found: %s", name)
}

// Load reads and parses a prompt by name.
func (l *Loader) Load(name string) (*Prompt, error) {
	path, err := l.Find(name)
	if err != nil {
		return nil, err
	}

	return l.LoadFile(path)
}

// LoadFile reads and parses a prompt from a file path.
func (l *Loader) LoadFile(path string) (*Prompt, error) {
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read prompt: %w", err)
	}

	return ParsePrompt(data, path)
}

// ParsePrompt parses prompt data, handling both YAML and legacy markdown formats.
func ParsePrompt(data []byte, path string) (*Prompt, error) {
	ext := strings.ToLower(filepath.Ext(path))

	// Handle legacy markdown files
	if ext == ".md" {
		return parseLegacyMarkdown(data, path)
	}

	// Parse as YAML
	var prompt Prompt
	if err := yaml.Unmarshal(data, &prompt); err != nil {
		return nil, fmt.Errorf("failed to parse prompt YAML: %w", err)
	}

	// Set name from filename if not specified
	if prompt.Name == "" {
		base := filepath.Base(path)
		prompt.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	return &prompt, nil
}

// parseLegacyMarkdown handles old-style markdown prompts for backwards compatibility.
func parseLegacyMarkdown(data []byte, path string) (*Prompt, error) {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	return &Prompt{
		Name:    name,
		Content: strings.TrimSpace(string(data)),
	}, nil
}

// List returns all available prompts across all search directories.
func (l *Loader) List() ([]PromptInfo, error) {
	var prompts []PromptInfo
	seen := collections.NewSet[string]()

	for _, dir := range l.searchDirs {
		err := afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}

			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") && path != dir {
					return filepath.SkipDir
				}
				return nil
			}

			name := info.Name()
			ext := strings.ToLower(filepath.Ext(name))

			// Accept .yaml, .yml, and .md files
			if ext != ".yaml" && ext != ".yml" && ext != ".md" {
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

			// Prompt name is relative path without extension
			promptName := strings.TrimSuffix(relPath, ext)

			if seen.Has(promptName) {
				return nil
			}
			seen.Add(promptName)

			// Load prompt to get metadata
			prompt, err := l.LoadFile(path)
			if err != nil {
				// Still include it but with limited info
				prompts = append(prompts, PromptInfo{
					Name:   promptName,
					Path:   path,
					Source: dir,
				})
				return nil
			}

			prompts = append(prompts, PromptInfo{
				Name:      promptName,
				Tags:      prompt.Tags,
				Variables: prompt.Variables,
				Path:      path,
				Source:    dir,
			})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to walk directory %s: %w", dir, err)
		}
	}

	return prompts, nil
}

// ListByTags returns prompts that have any of the specified tags.
// If tags is empty, returns all prompts.
func (l *Loader) ListByTags(tags []string) ([]PromptInfo, error) {
	all, err := l.List()
	if err != nil {
		return nil, err
	}

	if len(tags) == 0 {
		return all, nil
	}

	tagSet := collections.NewSet[string]()
	for _, t := range tags {
		tagSet.Add(strings.ToLower(t))
	}

	var filtered []PromptInfo
	for _, p := range all {
		for _, pt := range p.Tags {
			if tagSet.Has(strings.ToLower(pt)) {
				filtered = append(filtered, p)
				break
			}
		}
	}

	return filtered, nil
}

// LoadByTags loads all prompts matching any of the specified tags.
func (l *Loader) LoadByTags(tags []string) ([]*Prompt, error) {
	infos, err := l.ListByTags(tags)
	if err != nil {
		return nil, err
	}

	var prompts []*Prompt
	for _, info := range infos {
		prompt, err := l.LoadFile(info.Path)
		if err != nil {
			continue
		}
		prompts = append(prompts, prompt)
	}

	return prompts, nil
}

// CombinePrompts concatenates multiple prompts into a single content string.
func CombinePrompts(prompts []*Prompt) string {
	var parts []string
	for _, p := range prompts {
		if p.Content != "" {
			parts = append(parts, strings.TrimSpace(p.Content))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// HasTag checks if a prompt has a specific tag (case-insensitive).
func (p *Prompt) HasTag(tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range p.Tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

// HasAnyTag checks if a prompt has any of the specified tags.
func (p *Prompt) HasAnyTag(tags []string) bool {
	for _, t := range tags {
		if p.HasTag(t) {
			return true
		}
	}
	return false
}

// PromptTemplate generates a YAML template for a new prompt.
func PromptTemplate(name string) string {
	return fmt.Sprintf(`name: %s
description: ""
tags: []
content: |
  # %s

  Your prompt content here.
  Supports full markdown formatting.
`, name, toTitleCase(name))
}

// toTitleCase converts a hyphenated name to title case.
func toTitleCase(s string) string {
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	words := strings.Fields(s)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(string(word[0])) + strings.ToLower(word[1:])
		}
	}
	return strings.Join(words, " ")
}
