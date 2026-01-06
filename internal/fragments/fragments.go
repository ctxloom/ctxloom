package fragments

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cbroglie/mustache"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/fsys"
	"github.com/benjaminabbitt/scm/internal/logging"
)

// Fragment represents a parsed YAML context fragment file.
//
// YAML format:
//
//	version: "1.0"
//	author: "username"
//	tags:
//	  - review
//	  - security
//	variables:
//	  - project_name
//	  - language
//	no_distill: true  # Optional: skip distillation for this fragment
//	content: |
//	  # Your markdown content here
//	content_hash: "sha256:abc123..."
//	distilled: |
//	  # Compressed content here
//	distilled_by: "claude-code"
type Fragment struct {
	Name        string            // Fragment name (from filename)
	Path        string            // File path (for saving back)
	Version     string            // Version string for the fragment
	Author      string            // Author of the fragment
	Tags        []string          // Tags for filtering/categorization
	Variables   []string          // Variable names used in content
	Exports     map[string]string // Exported variable values (populated by generators)
	Content     string            // Markdown content
	ContentHash string            // SHA256 hash of content (for change detection)
	Distilled   string            // Distilled/compressed version of content
	DistilledBy string            // LLM that performed the distillation (e.g., "claude-3-opus")
	NoDistill   bool              // If true, skip distillation for this fragment
}

// Loader finds and loads context fragments from .scm directories.
type Loader struct {
	searchDirs       []string
	fs               fsys.FS
	warnFunc         func(string)
	suppressWarnings bool
	preferDistilled  bool
	failOnMissing    bool
	missingFragments []string
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

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs fsys.FS) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// WithPreferDistilled sets whether to prefer .distilled.md versions.
func WithPreferDistilled(prefer bool) LoaderOption {
	return func(l *Loader) {
		l.preferDistilled = prefer
	}
}

// WithFailOnMissing sets whether to fail when fragments are not found.
func WithFailOnMissing(fail bool) LoaderOption {
	return func(l *Loader) {
		l.failOnMissing = fail
	}
}

// NewLoader creates a new fragment loader with the given search directories.
// Directories are searched in order; first match wins.
// By default, prefers distilled versions if available.
func NewLoader(searchDirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs:      searchDirs,
		fs:              fsys.OS(),
		preferDistilled: true, // Default to preferring distilled
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

// ErrInvalidName is returned when a fragment name contains invalid characters.
var ErrInvalidName = fmt.Errorf("invalid fragment name")

// validateName checks that a fragment name doesn't contain path traversal patterns.
func validateName(name string) error {
	// Reject empty names
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidName)
	}

	// Reject null bytes
	if strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: contains null byte", ErrInvalidName)
	}

	// Reject absolute paths
	if filepath.IsAbs(name) {
		return fmt.Errorf("%w: absolute paths not allowed", ErrInvalidName)
	}

	// Normalize and check for path traversal
	cleaned := filepath.Clean(name)
	if strings.HasPrefix(cleaned, "..") {
		return fmt.Errorf("%w: path traversal not allowed", ErrInvalidName)
	}

	// Check each component for hidden directories or traversal
	parts := strings.Split(filepath.ToSlash(cleaned), "/")
	for _, part := range parts {
		if part == ".." {
			return fmt.Errorf("%w: path traversal not allowed", ErrInvalidName)
		}
	}

	return nil
}

// supportedExtensions lists file extensions for fragments (in priority order).
var supportedExtensions = []string{".yaml", ".yml"}

// Find locates a fragment by name across all search directories.
// Returns the full path to the fragment file.
//
// Naming conventions supported:
//   - Slash paths: "testing/tdd" finds "testing/tdd.yaml"
//   - Basename only: "tdd" finds it in any subdirectory (first match wins)
func (l *Loader) Find(name string) (string, error) {
	// Validate name to prevent path traversal
	if err := validateName(name); err != nil {
		return "", err
	}

	// Normalize path separators for cross-platform support
	name = filepath.FromSlash(name)

	// Build candidates with all supported extensions
	var candidates []string
	candidates = append(candidates, name)
	for _, ext := range supportedExtensions {
		candidates = append(candidates, name+ext)
	}

	// First try direct path lookup (including subdirectory paths)
	for _, dir := range l.searchDirs {
		for _, candidate := range candidates {
			path := filepath.Join(dir, candidate)
			if info, err := l.fs.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
	}

	// If not found directly, walk directories to find by basename
	baseName := filepath.Base(name)
	var baseNames []string
	baseNames = append(baseNames, baseName)
	for _, ext := range supportedExtensions {
		baseNames = append(baseNames, baseName+ext)
	}

	for _, dir := range l.searchDirs {
		var found string
		l.fs.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			fileName := d.Name()
			for _, bn := range baseNames {
				if fileName == bn {
					found = path
					return filepath.SkipAll
				}
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
// If preferDistilled is true and the fragment has a distilled field, that content is used.
func (l *Loader) Load(name string) (*Fragment, error) {
	path, err := l.Find(name)
	if err != nil {
		return nil, err
	}

	data, err := l.fs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read fragment %s: %w", name, err)
	}

	var frag *Fragment

	// Check file type - YAML or plain markdown
	if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		// YAML fragment file
		frag, err = parseYAMLFragment(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse fragment %s: %w", name, err)
		}
		frag.Name = name
		frag.Path = path
	} else if strings.HasSuffix(path, ".md") {
		// Plain markdown file - content only, no metadata
		frag = &Fragment{
			Name:    name,
			Path:    path,
			Content: strings.TrimSpace(string(data)),
		}
	} else {
		return nil, fmt.Errorf("unsupported file type for fragment %s: %s", name, path)
	}

	return frag, nil
}

// EffectiveContent returns the content to use for the fragment.
// If preferDistilled is true and the fragment has distilled content, returns that.
// Otherwise returns the original content.
func (f *Fragment) EffectiveContent(preferDistilled bool) string {
	if preferDistilled && f.Distilled != "" {
		return f.Distilled
	}
	return f.Content
}

// ComputeContentHash computes a SHA-256 hash of the fragment's content.
// Returns the hash in format "sha256:<hex>".
func (f *Fragment) ComputeContentHash() string {
	h := sha256.New()
	h.Write([]byte(f.Content))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// NeedsDistill returns true if the fragment needs to be distilled.
// This is true if there's no distilled content, or if the content has changed
// since the last distillation (hash mismatch).
func (f *Fragment) NeedsDistill() bool {
	if f.Distilled == "" {
		return true
	}
	if f.ContentHash == "" {
		return true
	}
	return f.ContentHash != f.ComputeContentHash()
}

// Save writes the fragment back to its YAML file.
// The Path field must be set.
func (f *Fragment) Save() error {
	if f.Path == "" {
		return fmt.Errorf("fragment path not set")
	}

	// Build YAML node tree to control scalar styles
	root := &yaml.Node{Kind: yaml.MappingNode}

	addScalar := func(key, value string) {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value},
		)
	}

	addLiteralBlock := func(key, value string) {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value, Style: yaml.LiteralStyle},
		)
	}

	addSequence := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range values {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			seq,
		)
	}

	addMap := func(key string, values map[string]string) {
		if len(values) == 0 {
			return
		}
		m := &yaml.Node{Kind: yaml.MappingNode}
		for k, v := range values {
			m.Content = append(m.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: k},
				&yaml.Node{Kind: yaml.ScalarNode, Value: v},
			)
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			m,
		)
	}

	if f.Version != "" {
		addScalar("version", f.Version)
	}
	if f.Author != "" {
		addScalar("author", f.Author)
	}
	addSequence("tags", f.Tags)
	addSequence("variables", f.Variables)
	if f.NoDistill {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "no_distill"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: "true"},
		)
	}
	addLiteralBlock("content", f.Content)
	if f.ContentHash != "" {
		addScalar("content_hash", f.ContentHash)
	}
	if f.Distilled != "" {
		addLiteralBlock("distilled", f.Distilled)
	}
	if f.DistilledBy != "" {
		addScalar("distilled_by", f.DistilledBy)
	}
	addMap("exports", f.Exports)

	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal fragment: %w", err)
	}

	return fsys.WriteProtected(f.Path, data)
}

// parseYAMLFragment parses a YAML fragment file.
func parseYAMLFragment(data []byte) (*Fragment, error) {
	var yf yamlFragment
	if err := yaml.Unmarshal(data, &yf); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	frag := &Fragment{
		Version:     yf.Version,
		Author:      yf.Author,
		Tags:        yf.Tags,
		Variables:   yf.Variables,
		Content:     strings.TrimSpace(yf.Content),
		ContentHash: yf.ContentHash,
		Distilled:   strings.TrimSpace(yf.Distilled),
		DistilledBy: yf.DistilledBy,
		NoDistill:   yf.NoDistill,
	}

	// Copy exports if present (from generator output)
	if len(yf.Exports) > 0 {
		frag.Exports = yf.Exports
	}

	return frag, nil
}

// LoadMultiple loads multiple fragments, merges their variables,
// concatenates their context, and applies mustache templating.
func (l *Loader) LoadMultiple(names []string) (string, error) {
	return l.LoadMultipleWithVars(names, nil)
}

// LoadMultipleWithVars loads multiple fragments with additional variables.
// Variables are provided via extraVars (from profile config or generators).
// Missing fragments are warned about. If WithFailOnMissing is set, returns an error.
func (l *Loader) LoadMultipleWithVars(names []string, extraVars map[string]string) (string, error) {
	var frags []*Fragment
	l.missingFragments = nil // Reset missing fragments

	// Load all fragments
	for _, name := range names {
		frag, err := l.Load(name)
		if err != nil {
			l.missingFragments = append(l.missingFragments, name)
			l.warn(fmt.Sprintf("fragment not found: %s", name))
			continue
		}
		frags = append(frags, frag)
	}

	// Fail if any fragments were missing and failOnMissing is set
	if l.failOnMissing && len(l.missingFragments) > 0 {
		return "", fmt.Errorf("fragments not found: %s", strings.Join(l.missingFragments, ", "))
	}

	// Use provided variables
	variables := make(map[string]string)
	for k, v := range extraVars {
		variables[k] = v
	}

	// Assemble context intelligently
	assembled := l.assembleContext(frags)

	// Apply mustache templating
	rendered, err := l.applyTemplate(assembled, variables, "assembled")
	if err != nil {
		return "", fmt.Errorf("failed to apply template: %w", err)
	}

	return rendered, nil
}

// assembleContext intelligently combines fragment contexts.
// Groups related content and adds clear section boundaries.
// Uses distilled content when preferDistilled is true and available.
func (l *Loader) assembleContext(frags []*Fragment) string {
	if len(frags) == 0 {
		return ""
	}

	var sections []string
	for _, frag := range frags {
		content := frag.EffectiveContent(l.preferDistilled)
		if content == "" {
			continue
		}
		sections = append(sections, strings.TrimSpace(content))
	}

	return strings.Join(sections, "\n\n---\n\n")
}

// applyTemplate applies mustache templating to the context using variables.
// It warns about any variables referenced in the template that aren't defined.
// templateName is used for diagnostic messages to identify which template has issues.
func (l *Loader) applyTemplate(template string, vars map[string]string, templateName string) (string, error) {
	// Find all variable references in the template
	varPattern := regexp.MustCompile(`\{\{\s*([^}#/!>\s][^}]*?)\s*\}\}`)
	matches := varPattern.FindAllStringSubmatch(template, -1)

	// Check for undefined variables
	seen := collections.NewSet[string]()
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
			if !seen.Has(varName) {
				seen.Add(varName)
				if _, exists := vars[varName]; !exists {
					l.warn(fmt.Sprintf("undefined variable: {{%s}} in %s", varName, templateName))
					logging.L().Warn(logging.MsgVariableUnexpanded,
						logging.VariableName(varName),
						zap.String("template", templateName))
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
// from the search directory (e.g., "testing/tdd" for "testing/tdd.yaml").
func (l *Loader) List() ([]FragmentInfo, error) {
	var fragments []FragmentInfo
	seen := collections.NewSet[string]()

	for _, dir := range l.searchDirs {
		err := l.fs.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
			ext := strings.ToLower(filepath.Ext(name))

			// Accept .yaml and .yml files only
			if ext != ".yaml" && ext != ".yml" {
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

			// Fragment name is relative path without extension
			fragName := strings.TrimSuffix(relPath, ext)

			if !seen.Has(fragName) {
				seen.Add(fragName)

				// Load fragment to get metadata
				frag, err := l.Load(fragName)
				var tags []string
				var variables []string
				if err == nil {
					tags = frag.Tags
					variables = frag.Variables
				}

				fragments = append(fragments, FragmentInfo{
					Name:      fragName,
					FileName:  name,
					Path:      path,
					Source:    dir,
					Tags:      tags,
					Variables: variables,
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

// ListByTags returns fragments that have any of the specified tags.
// If tags is empty, returns all fragments.
func (l *Loader) ListByTags(tags []string) ([]FragmentInfo, error) {
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

	var filtered []FragmentInfo
	for _, f := range all {
		for _, ft := range f.Tags {
			if tagSet.Has(strings.ToLower(ft)) {
				filtered = append(filtered, f)
				break
			}
		}
	}

	return filtered, nil
}

// LoadByTags loads all fragments matching any of the specified tags.
func (l *Loader) LoadByTags(tags []string) ([]*Fragment, error) {
	infos, err := l.ListByTags(tags)
	if err != nil {
		return nil, err
	}

	var frags []*Fragment
	for _, info := range infos {
		frag, err := l.Load(info.Name)
		if err != nil {
			continue
		}
		frags = append(frags, frag)
	}

	return frags, nil
}

// FragmentInfo holds metadata about a fragment.
type FragmentInfo struct {
	Name      string
	FileName  string
	Path      string
	Source    string
	Tags      []string
	Variables []string
}

// yamlFragment is the structure for YAML-based fragment files.
type yamlFragment struct {
	Version     string            `yaml:"version,omitempty"`
	Author      string            `yaml:"author,omitempty"`
	Tags        []string          `yaml:"tags,omitempty"`
	Variables   []string          `yaml:"variables,omitempty"`
	Notes       string            `yaml:"notes,omitempty"`        // Human-readable notes (not sent to AI)
	Content     string            `yaml:"content"`
	ContentHash string            `yaml:"content_hash,omitempty"` // SHA256 hash of content
	Distilled   string            `yaml:"distilled,omitempty"`    // Distilled version of content
	DistilledBy string            `yaml:"distilled_by,omitempty"` // LLM that performed distillation
	NoDistill   bool              `yaml:"no_distill,omitempty"`   // If true, skip distillation
	Exports     map[string]string `yaml:"exports,omitempty"`      // For generator output
}

// ParseYAML parses YAML content as a fragment.
// This is useful for parsing generator output.
func ParseYAML(content string) (*Fragment, error) {
	frag, err := parseYAMLFragment([]byte(content))
	if err != nil {
		return nil, err
	}
	return frag, nil
}

// HasTag checks if a fragment has a specific tag (case-insensitive).
func (f *Fragment) HasTag(tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range f.Tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

// HasAnyTag checks if a fragment has any of the specified tags.
func (f *Fragment) HasAnyTag(tags []string) bool {
	for _, t := range tags {
		if f.HasTag(t) {
			return true
		}
	}
	return false
}

// CombineFragments concatenates multiple fragment contents with separators.
func CombineFragments(frags []*Fragment) string {
	var parts []string
	for _, f := range frags {
		if f.Content != "" {
			parts = append(parts, strings.TrimSpace(f.Content))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// LoadedFragment contains a fragment with its effective content and metadata.
// Used to pass structured fragment data to plugins.
type LoadedFragment struct {
	Name        string
	Version     string
	Tags        []string
	Content     string // Effective content (distilled or original, with variables applied)
	IsDistilled bool
	DistilledBy string
}

// LoadMultipleAsFragments loads multiple fragments and returns them with metadata.
// Unlike LoadMultipleWithVars, this preserves fragment boundaries and metadata.
func (l *Loader) LoadMultipleAsFragments(names []string, extraVars map[string]string) ([]*LoadedFragment, error) {
	var result []*LoadedFragment
	l.missingFragments = nil

	// Use provided variables
	variables := make(map[string]string)
	for k, v := range extraVars {
		variables[k] = v
	}

	for _, name := range names {
		frag, err := l.Load(name)
		if err != nil {
			l.missingFragments = append(l.missingFragments, name)
			l.warn(fmt.Sprintf("fragment not found: %s", name))
			continue
		}

		// Determine if we're using distilled content
		useDistilled := l.preferDistilled && frag.Distilled != ""
		content := frag.EffectiveContent(l.preferDistilled)

		// Apply mustache templating to content
		rendered, err := l.applyTemplate(content, variables, name)
		if err != nil {
			l.warn(fmt.Sprintf("failed to apply template to %s: %v", name, err))
			rendered = content // Fall back to unrendered content
		}

		loaded := &LoadedFragment{
			Name:        frag.Name,
			Version:     frag.Version,
			Tags:        frag.Tags,
			Content:     strings.TrimSpace(rendered),
			IsDistilled: useDistilled,
			DistilledBy: frag.DistilledBy,
		}
		result = append(result, loaded)
	}

	if l.failOnMissing && len(l.missingFragments) > 0 {
		return nil, fmt.Errorf("fragments not found: %s", strings.Join(l.missingFragments, ", "))
	}

	return result, nil
}
