// Package bundles provides types and utilities for ctxloom bundles.
// Bundles are the primary content unit that group related fragments,
// prompts, and MCP server configurations with a single version.
package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/collections"
	"github.com/ctxloom/ctxloom/internal/errs"
)

// gitHostPattern matches paths that start with a git hosting domain.
// Examples: github.com/owner/repo, gitlab.com/group/project
var gitHostPattern = regexp.MustCompile(`^(github\.com|gitlab\.com|bitbucket\.org)/[^/]+/([^/]+)(?:/(.+))?$`)

// NormalizeBundleName converts a bundle path to its canonical repo/bundle format.
// This ensures consistent naming regardless of how the bundle was installed.
//
// Input formats:
//   - github.com/owner/ctxloom-github/go-development → ctxloom-github/go-development
//   - gitlab.com/group/repo/core → repo/core
//   - ctxloom-github/go-development → ctxloom-github/go-development (unchanged)
//   - local-bundle → local-bundle (unchanged)
//
// The canonical format is: repo-name/bundle-path
func NormalizeBundleName(name string) string {
	if name == "" {
		return name
	}

	// Check if path starts with a git hosting domain
	if matches := gitHostPattern.FindStringSubmatch(name); matches != nil {
		// matches[2] = repo name, matches[3] = remaining path (may be empty)
		repoName := matches[2]
		remainingPath := matches[3]
		if remainingPath != "" {
			return repoName + "/" + remainingPath
		}
		return repoName
	}

	// Already in canonical format or local bundle
	return name
}

// Bundle represents a versioned collection of related content.
// All items within a bundle share the same version.
// Each fragment and prompt is distilled individually with bundle context.
type Bundle struct {
	// Metadata
	Version      string   `yaml:"version"`
	Tags         []string `yaml:"tags,omitempty"`
	Author       string   `yaml:"author,omitempty"`
	Description  string   `yaml:"description,omitempty"`
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup instructions, shown on install

	// Content maps (keyed by name)
	Fragments map[string]BundleFragment `yaml:"fragments,omitempty"`
	Prompts   map[string]BundlePrompt   `yaml:"prompts,omitempty"`
	MCP       map[string]BundleMCP      `yaml:"mcp,omitempty"` // MCP servers

	// Internal fields (not serialized)
	Name string `yaml:"-"` // Bundle name (from path)
	Path string `yaml:"-"` // File path for saving
}

// BundleMCP defines an MCP server within a bundle.
type BundleMCP struct {
	Command      string            `yaml:"command"`
	Args         []string          `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	Notes        string            `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
}

// BundleFragment defines a fragment within a bundle.
type BundleFragment struct {
	Tags         []string `yaml:"tags,omitempty"` // Additional tags (merged with bundle tags)
	Notes        string   `yaml:"notes,omitempty"` // Human-readable notes, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
	Content      string   `yaml:"content"`
	ContentHash  string   `yaml:"content_hash,omitempty"`
	Distilled    string   `yaml:"distilled,omitempty"`
	DistilledBy  string   `yaml:"distilled_by,omitempty"`
	NoDistill    bool     `yaml:"no_distill,omitempty"`
}

// BundlePrompt defines a prompt within a bundle.
type BundlePrompt struct {
	Description string   `yaml:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	Notes       string   `yaml:"notes,omitempty"` // Human-readable notes, not sent to AI
	Installation string        `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
	Content      string        `yaml:"content"`
	ContentHash  string        `yaml:"content_hash,omitempty"`
	Distilled    string        `yaml:"distilled,omitempty"`
	DistilledBy  string        `yaml:"distilled_by,omitempty"`
	NoDistill    bool          `yaml:"no_distill,omitempty"`
	Plugins      PluginsConfig `yaml:"plugins,omitempty"` // Plugin-specific settings (e.g., claude-code skill config)
}

// ComputeContentHash computes SHA256 hash of the content.
func (f *BundleFragment) ComputeContentHash() string {
	h := sha256.Sum256([]byte(f.Content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// NeedsDistill returns true if this fragment needs distillation.
func (f *BundleFragment) NeedsDistill() bool {
	if f.NoDistill {
		return false
	}
	if f.Distilled == "" {
		return true
	}
	if f.ContentHash == "" {
		return true
	}
	return f.ContentHash != f.ComputeContentHash()
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (f *BundleFragment) EffectiveContent(preferDistilled bool) string {
	if preferDistilled && f.Distilled != "" && !f.NoDistill {
		return f.Distilled
	}
	return f.Content
}

// ComputeContentHash computes SHA256 hash of the content.
func (p *BundlePrompt) ComputeContentHash() string {
	h := sha256.Sum256([]byte(p.Content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// NeedsDistill returns true if this prompt needs distillation.
func (p *BundlePrompt) NeedsDistill() bool {
	if p.NoDistill {
		return false
	}
	if p.Distilled == "" {
		return true
	}
	if p.ContentHash == "" {
		return true
	}
	return p.ContentHash != p.ComputeContentHash()
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (p *BundlePrompt) EffectiveContent(preferDistilled bool) string {
	if preferDistilled && p.Distilled != "" && !p.NoDistill {
		return p.Distilled
	}
	return p.Content
}

// HasMCP returns true if bundle includes any MCP servers.
func (b *Bundle) HasMCP() bool {
	return len(b.MCP) > 0
}

// MCPCount returns the number of MCP servers in the bundle.
func (b *Bundle) MCPCount() int {
	return len(b.MCP)
}

// MCPNames returns sorted MCP server names.
func (b *Bundle) MCPNames() []string {
	names := make([]string, 0, len(b.MCP))
	for name := range b.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FragmentCount returns the number of fragments in the bundle.
func (b *Bundle) FragmentCount() int {
	return len(b.Fragments)
}

// PromptCount returns the number of prompts in the bundle.
func (b *Bundle) PromptCount() int {
	return len(b.Prompts)
}

// FragmentNames returns sorted fragment names.
func (b *Bundle) FragmentNames() []string {
	names := make([]string, 0, len(b.Fragments))
	for name := range b.Fragments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PromptNames returns sorted prompt names.
func (b *Bundle) PromptNames() []string {
	names := make([]string, 0, len(b.Prompts))
	for name := range b.Prompts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AllTags returns all unique tags from bundle and its contents.
func (b *Bundle) AllTags() []string {
	tagSet := collections.NewSet[string]()
	tagSet.AddAll(b.Tags...)
	for _, f := range b.Fragments {
		tagSet.AddAll(f.Tags...)
	}
	for _, p := range b.Prompts {
		tagSet.AddAll(p.Tags...)
	}

	tags := tagSet.Items()
	sort.Strings(tags)
	return tags
}

// Save writes the bundle back to its file path.
func (b *Bundle) Save() error {
	if b.Path == "" {
		return fmt.Errorf("bundle has no path set")
	}

	data, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("failed to marshal bundle: %w", err)
	}

	return os.WriteFile(b.Path, data, 0644)
}

// AssembledContent combines all fragment content with separators.
func (b *Bundle) AssembledContent(preferDistilled bool) string {
	var parts []string

	for _, name := range b.FragmentNames() {
		frag := b.Fragments[name]
		content := frag.EffectiveContent(preferDistilled)
		parts = append(parts, strings.TrimSpace(content))
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// Loader finds and loads bundles from search directories.
// Loader is safe for concurrent use.
type Loader struct {
	searchDirs      []string
	preferDistilled bool
	fs              afero.Fs
	mu              sync.RWMutex       // Protects cache
	cache           map[string]*Bundle // Cache of loaded bundles by path
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// NewLoader creates a bundle loader.
// The loader caches loaded bundles in memory to avoid redundant disk reads.
func NewLoader(searchDirs []string, preferDistilled bool, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs:      searchDirs,
		preferDistilled: preferDistilled,
		fs:              afero.NewOsFs(),
		cache:           make(map[string]*Bundle),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Load reads a bundle by name.
// Name can be:
// - Simple name: "go-tools" -> searches for go-tools.yaml or go-tools/bundle.yaml
// - Remote-qualified: "alice/go-tools" -> searches in alice/ subdirectory
func (l *Loader) Load(name string) (*Bundle, error) {
	path, err := l.Find(name)
	if err != nil {
		return nil, err
	}
	return l.LoadFile(path)
}

// Find locates a bundle file by name (supports paths with slashes like "github.com/user/repo/bundle").
func (l *Loader) Find(name string) (string, error) {
	// Security: validate name
	if err := validateBundleName(name); err != nil {
		return "", err
	}

	// Convert forward slashes to OS-specific separator
	osName := filepath.FromSlash(name)

	for _, dir := range l.searchDirs {
		// Try direct path: name.yaml
		path := filepath.Join(dir, osName+".yaml")
		if _, err := l.fs.Stat(path); err == nil {
			return path, nil
		}

		// Try directory path: name/bundle.yaml
		path = filepath.Join(dir, osName, "bundle.yaml")
		if _, err := l.fs.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
}

// LoadFile reads a bundle from a specific path.
// Results are cached to avoid redundant disk reads when the same bundle
// is referenced multiple times (e.g., by multiple profiles).
// This method is safe for concurrent use.
func (l *Loader) LoadFile(path string) (*Bundle, error) {
	// Check cache first (read lock)
	l.mu.RLock()
	if cached, ok := l.cache[path]; ok {
		l.mu.RUnlock()
		return cached, nil
	}
	l.mu.RUnlock()

	// Load from disk (no lock held during I/O)
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	bundle, err := ParseBundle(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse bundle %s: %w", path, err)
	}

	bundle.Path = path
	bundle.Name = extractBundleName(path)

	// Cache for future loads (write lock)
	l.mu.Lock()
	// Double-check in case another goroutine cached it while we were loading
	if cached, ok := l.cache[path]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.cache[path] = bundle
	l.mu.Unlock()

	return bundle, nil
}

// List returns all available bundles.
func (l *Loader) List() ([]*BundleInfo, error) {
	var bundles []*BundleInfo
	seen := collections.NewSet[string]()

	// Search bundle directories recursively
	for _, dir := range l.searchDirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil || !exists {
			continue
		}

		_ = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip directories we can't read
			}
			if info.IsDir() {
				// Check for bundle.yaml in directories
				bundlePath := filepath.Join(path, "bundle.yaml")
				if _, err := l.fs.Stat(bundlePath); err == nil {
					relPath, _ := filepath.Rel(dir, path)
					bundleName := NormalizeBundleName(filepath.ToSlash(relPath))
					if seen.Has(bundleName) {
						return nil
					}
					bundleInfo, err := l.loadBundleInfo(bundlePath, bundleName)
					if err == nil {
						bundles = append(bundles, bundleInfo)
						seen.Add(bundleName)
					}
				}
				return nil
			}

			name := info.Name()
			// Check for .yaml files (bundle files)
			if strings.HasSuffix(name, ".yaml") && name != "bundle.yaml" {
				relPath, _ := filepath.Rel(dir, path)
				bundleName := NormalizeBundleName(strings.TrimSuffix(filepath.ToSlash(relPath), ".yaml"))
				if seen.Has(bundleName) {
					return nil
				}
				bundleInfo, err := l.loadBundleInfo(path, bundleName)
				if err == nil {
					bundles = append(bundles, bundleInfo)
					seen.Add(bundleName)
				}
			}
			return nil
		})
	}

	// Sort by name
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].Name < bundles[j].Name
	})

	return bundles, nil
}

// BundleInfo holds metadata about a bundle without loading full content.
type BundleInfo struct {
	Name          string
	Path          string
	Version       string
	Description   string
	Tags          []string
	FragmentCount int
	PromptCount   int
	MCPCount      int
}

func (l *Loader) loadBundleInfo(path, name string) (*BundleInfo, error) {
	bundle, err := l.LoadFile(path)
	if err != nil {
		return nil, err
	}

	return &BundleInfo{
		Name:          name,
		Path:          path,
		Version:       bundle.Version,
		Description:   bundle.Description,
		Tags:          bundle.Tags,
		FragmentCount: bundle.FragmentCount(),
		PromptCount:   bundle.PromptCount(),
		MCPCount:      bundle.MCPCount(),
	}, nil
}

// ParseBundle parses raw YAML into a Bundle.
func ParseBundle(data []byte) (*Bundle, error) {
	var bundle Bundle
	if err := yaml.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid bundle YAML: %w", err)
	}

	// Initialize maps if nil
	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]BundleFragment)
	}
	if bundle.Prompts == nil {
		bundle.Prompts = make(map[string]BundlePrompt)
	}
	if bundle.MCP == nil {
		bundle.MCP = make(map[string]BundleMCP)
	}

	return &bundle, nil
}

// validateBundleName checks for path traversal and invalid characters.
func validateBundleName(name string) error {
	if name == "" {
		return fmt.Errorf("empty bundle name")
	}

	// Check for null bytes first (before any path operations)
	if strings.ContainsAny(name, "\x00") {
		return fmt.Errorf("invalid bundle name: null bytes not allowed")
	}

	// Normalize path first
	cleaned := filepath.Clean(name)

	// Check for traversal after cleaning (catches "....", "foo/../bar", etc.)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return fmt.Errorf("invalid bundle name: path traversal not allowed")
	}

	return nil
}

// extractBundleName extracts bundle name from file path.
func extractBundleName(path string) string {
	base := filepath.Base(path)

	// If it's bundle.yaml, use parent directory name
	if base == "bundle.yaml" {
		return filepath.Base(filepath.Dir(path))
	}

	// Otherwise use filename without extension
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// LoadedContent represents content loaded at runtime, ready to send to LLM.
// This is the runtime representation of fragments/prompts from bundles.
type LoadedContent struct {
	Name         string            // Full name (bundle/item)
	Version      string            // Bundle version
	Tags         []string          // Combined tags
	Content      string            // The actual content
	Installation string            // Setup/installation instructions for tooling
	IsDistilled  bool              // Whether distilled version was used
	DistilledBy  string            // Model that created distillation
	Exports      map[string]string // Exported variables (from generators)
	Plugins      PluginsConfig     // Plugin-specific settings
}

// ClaudeCodeConfig holds configuration for exporting prompts as Claude Code slash commands.
type ClaudeCodeConfig struct {
	Enabled      *bool    `yaml:"enabled"`       // nil = true (opt-out model)
	Description  string   `yaml:"description"`   // For /help display
	ArgumentHint string   `yaml:"argument_hint"` // Autocomplete hint
	AllowedTools []string `yaml:"allowed_tools"` // Tool restrictions
	Model        string   `yaml:"model"`         // Override model
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c ClaudeCodeConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// GeminiConfig holds configuration for exporting prompts as Gemini CLI slash commands.
type GeminiConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // For /help display
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c GeminiConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// LMPluginConfig holds LM plugin-specific settings.
type LMPluginConfig struct {
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code"`
	Gemini     GeminiConfig     `yaml:"gemini"`
}

// PluginsConfig holds plugin-specific settings.
type PluginsConfig struct {
	LM LMPluginConfig `yaml:"llm"`
}

// ContentInfo provides metadata about a fragment or prompt for listing.
type ContentInfo struct {
	Name      string
	FileName  string
	Path      string
	Source    string // "bundle:name" or legacy path
	Tags      []string
	Bundle    string // Bundle name this came from
	ItemType  string // "fragment" or "prompt"
}

// ListAllFragments returns info about all fragments across all bundles.
func (l *Loader) ListAllFragments() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	var infos []ContentInfo
	seen := collections.NewSet[string]()

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, frag := range bundle.Fragments {
			// Use bundleInfo.Name (full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundleInfo.Path,
				Source:   bundleInfo.Name,
				Tags:     append(bundle.Tags, frag.Tags...),
				Bundle:   bundleInfo.Name,
				ItemType: "fragment",
			})
		}
	}

	return infos, nil
}

// ListAllPrompts returns info about all prompts across all bundles.
func (l *Loader) ListAllPrompts() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	seen := collections.NewSet[string]()
	var infos []ContentInfo
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, prompt := range bundle.Prompts {
			// Use bundleInfo.Name (normalized full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundleInfo.Path,
				Source:   bundleInfo.Name,
				Tags:     append(bundle.Tags, prompt.Tags...),
				Bundle:   bundleInfo.Name,
				ItemType: "prompt",
			})
		}
	}

	return infos, nil
}

// GetFragment finds and loads a fragment by name.
// Name can be "fragment-name" (searches all bundles) or "bundle#fragments/name".
func (l *Loader) GetFragment(name string) (*LoadedContent, error) {
	// Check for # syntax: bundle#fragments/name
	if idx := strings.Index(name, "#"); idx != -1 {
		bundleName := name[:idx]
		itemPath := name[idx+1:]

		// Parse itemPath: "fragments/name"
		parts := strings.SplitN(itemPath, "/", 2)
		if len(parts) != 2 || parts[0] != "fragments" {
			return nil, fmt.Errorf("invalid fragment reference: %s", name)
		}
		fragName := parts[1]

		bundle, err := l.Load(bundleName)
		if err != nil {
			return nil, err
		}

		frag, ok := bundle.Fragments[fragName]
		if !ok {
			return nil, fmt.Errorf("fragment %q not found in bundle %q", fragName, bundleName)
		}

		return &LoadedContent{
			Name:         fmt.Sprintf("%s/%s", bundle.Name, fragName),
			Version:      bundle.Version,
			Tags:         append(bundle.Tags, frag.Tags...),
			Content:      frag.EffectiveContent(l.preferDistilled),
			Installation: frag.Installation,
			IsDistilled:  l.preferDistilled && frag.Distilled != "",
			DistilledBy:  frag.DistilledBy,
		}, nil
	}

	// Search all bundles for the fragment
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		if frag, ok := bundle.Fragments[name]; ok {
			return &LoadedContent{
				Name:         fmt.Sprintf("%s/%s", bundle.Name, name),
				Version:      bundle.Version,
				Tags:         append(bundle.Tags, frag.Tags...),
				Content:      frag.EffectiveContent(l.preferDistilled),
				Installation: frag.Installation,
				IsDistilled:  l.preferDistilled && frag.Distilled != "",
				DistilledBy:  frag.DistilledBy,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, name)
}

// GetPrompt finds and loads a prompt by name.
// Name can be "prompt-name" (searches all bundles) or "bundle#prompts/name".
func (l *Loader) GetPrompt(name string) (*LoadedContent, error) {
	// Check for # syntax: bundle#prompts/name
	if idx := strings.Index(name, "#"); idx != -1 {
		bundleName := name[:idx]
		itemPath := name[idx+1:]

		// Parse itemPath: "prompts/name"
		parts := strings.SplitN(itemPath, "/", 2)
		if len(parts) != 2 || parts[0] != "prompts" {
			return nil, fmt.Errorf("invalid prompt reference: %s", name)
		}
		promptName := parts[1]

		bundle, err := l.Load(bundleName)
		if err != nil {
			return nil, err
		}

		prompt, ok := bundle.Prompts[promptName]
		if !ok {
			return nil, fmt.Errorf("prompt %q not found in bundle %q", promptName, bundleName)
		}

		return &LoadedContent{
			Name:         fmt.Sprintf("%s/%s", bundle.Name, promptName),
			Version:      bundle.Version,
			Tags:         append(bundle.Tags, prompt.Tags...),
			Content:      prompt.EffectiveContent(l.preferDistilled),
			Installation: prompt.Installation,
			IsDistilled:  l.preferDistilled && prompt.Distilled != "",
			DistilledBy:  prompt.DistilledBy,
			Plugins:      prompt.Plugins,
		}, nil
	}

	// Search all bundles for the prompt
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		if prompt, ok := bundle.Prompts[name]; ok {
			return &LoadedContent{
				Name:         fmt.Sprintf("%s/%s", bundle.Name, name),
				Version:      bundle.Version,
				Tags:         append(bundle.Tags, prompt.Tags...),
				Content:      prompt.EffectiveContent(l.preferDistilled),
				Installation: prompt.Installation,
				IsDistilled:  l.preferDistilled && prompt.Distilled != "",
				DistilledBy:  prompt.DistilledBy,
				Plugins:      prompt.Plugins,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errs.ErrPromptNotFound, name)
}

// ListByTags returns fragments matching any of the given tags.
func (l *Loader) ListByTags(tags []string) ([]ContentInfo, error) {
	all, err := l.ListAllFragments()
	if err != nil {
		return nil, err
	}

	tagSet := collections.NewSetFrom(tags...)

	var matched []ContentInfo
	for _, info := range all {
		for _, t := range info.Tags {
			if tagSet.Has(t) {
				matched = append(matched, info)
				break
			}
		}
	}

	return matched, nil
}

// LoadMultiple loads multiple fragments by name and returns combined content.
// Returns the content, the names of fragments that were successfully loaded, and any error.
func (l *Loader) LoadMultiple(names []string) (string, []string, error) {
	var parts []string
	var loaded []string

	for _, name := range names {
		content, err := l.GetFragment(name)
		if err != nil {
			// Skip not found, continue with others
			continue
		}
		parts = append(parts, strings.TrimSpace(content.Content))
		loaded = append(loaded, name)
	}

	return strings.Join(parts, "\n\n---\n\n"), loaded, nil
}
