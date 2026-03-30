package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/SophisticatedContextManager/scm/internal/remote"
)

// Profile represents a named collection of fragments, bundles, and configuration.
// Profiles are stored as YAML files in .scm/profiles/<name>.yaml
//
// # Content Reference Syntax
//
// Profiles use a standardized path syntax to reference content:
//
//	bundle-name                      # Entire bundle (all fragments, prompts, MCP)
//	bundle-name:fragments/name       # Specific fragment from bundle
//	bundle-name:prompts/name         # Specific prompt from bundle
//	bundle-name:mcp                  # MCP server from bundle
//	remote/bundle-name:fragments/x   # Fragment from remote bundle
//
// Legacy syntax (for backwards compatibility):
//
//	fragment-name                    # Standalone fragment file
type Profile struct {
	Name        string            `yaml:"-"`                      // Derived from filename
	Path        string            `yaml:"-"`                      // Full path to the file
	Description string            `yaml:"description,omitempty"`
	Default     bool              `yaml:"default,omitempty"`      // Whether this is a default profile
	Parents     []string          `yaml:"parents,omitempty"`      // Parent profiles to inherit from
	Tags        []string          `yaml:"tags,omitempty"`         // Fragment tags to include

	// Bundles are content references using standardized path syntax
	// Examples: "go-development", "go-development#fragments/testing", "github/security#mcp"
	// Full URLs: "https://github.com/user/repo@v1/bundles/name"
	Bundles []string `yaml:"bundles,omitempty"`

	Variables map[string]string `yaml:"variables,omitempty"`

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `yaml:"exclude_fragments,omitempty"`
	ExcludePrompts   []string `yaml:"exclude_prompts,omitempty"`
	ExcludeMCP       []string `yaml:"exclude_mcp,omitempty"`
}

// ContentRef represents a parsed content reference.
type ContentRef struct {
	Raw      string // Original reference string
	Remote   string // Remote name or URL (empty for local)
	Bundle   string // Bundle name
	ItemType string // "fragments", "prompts", "mcp", or empty for entire bundle
	ItemName string // Item name within the bundle
	IsURL    bool   // True if Remote is a full URL
}

// ParseContentRef parses a content reference string.
// Uses # (hash) to access items within a bundle, similar to URL fragment syntax.
//
// Formats:
//
//	bundle-name                                    -> Bundle: bundle-name
//	bundle-name#fragments/name                     -> Bundle: bundle-name, ItemType: fragments, ItemName: name
//	bundle-name#prompts/name                       -> Bundle: bundle-name, ItemType: prompts, ItemName: name
//	bundle-name#mcp/name                           -> Bundle: bundle-name, ItemType: mcp, ItemName: name
//	remote/bundle-name#fragments/x                 -> Remote: remote, Bundle: bundle-name, ItemType: fragments, ItemName: x
//	https://github.com/user/repo                   -> Remote: https://github.com/user/repo (URL), Bundle: repo
//	https://github.com/user/repo#fragments/x       -> Remote: https://github.com/user/repo (URL), Bundle: repo, ItemType: fragments, ItemName: x
//	git@github.com:user/repo                       -> Remote: git@github.com:user/repo (URL), Bundle: repo
//	git@github.com:user/repo#fragments/x           -> Remote: git@github.com:user/repo (URL), Bundle: repo, ItemType: fragments, ItemName: x
func ParseContentRef(ref string) ContentRef {
	result := ContentRef{Raw: ref}

	// Find # separator for item path (works for all formats)
	hashIdx := strings.Index(ref, "#")
	var bundlePart, itemPath string
	if hashIdx != -1 {
		bundlePart = ref[:hashIdx]
		itemPath = ref[hashIdx+1:]
	} else {
		bundlePart = ref
	}

	// Check for URL schemes
	if strings.HasPrefix(bundlePart, "https://") || strings.HasPrefix(bundlePart, "http://") {
		result.IsURL = true
		result.Remote = bundlePart
		result.Bundle = extractBundleFromURL(bundlePart)
		if itemPath != "" {
			parseItemPath(itemPath, &result)
		}
		return result
	}

	// Check for git@ SSH URL format
	if strings.HasPrefix(bundlePart, "git@") {
		result.IsURL = true
		result.Remote = bundlePart
		result.Bundle = extractBundleFromGitURL(bundlePart)
		if itemPath != "" {
			parseItemPath(itemPath, &result)
		}
		return result
	}

	// Standard format: remote/bundle or bundle
	slashIdx := strings.Index(bundlePart, "/")
	if slashIdx != -1 {
		result.Remote = bundlePart[:slashIdx]
		result.Bundle = bundlePart[slashIdx+1:]
	} else {
		result.Bundle = bundlePart
	}

	// Parse item path if present
	if itemPath != "" {
		parseItemPath(itemPath, &result)
	}

	return result
}

// extractBundleFromURL extracts the bundle name from a URL
// https://github.com/user/repo -> repo
// https://github.com/user/repo.git -> repo
func extractBundleFromURL(url string) string {
	// Remove trailing .git if present
	url = strings.TrimSuffix(url, ".git")

	// Get the last path segment
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		return url[idx+1:]
	}
	return url
}

// extractBundleFromGitURL extracts the bundle name from a git@ URL
// git@github.com:user/repo -> repo
// git@github.com:user/repo.git -> repo
func extractBundleFromGitURL(url string) string {
	// Remove trailing .git if present
	url = strings.TrimSuffix(url, ".git")

	// Get the last path segment after :
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		return url[idx+1:]
	}
	// If no /, try after :
	if idx := strings.LastIndex(url, ":"); idx != -1 {
		return url[idx+1:]
	}
	return url
}

// parseItemPath parses the item path portion (fragments/name, prompts/name, mcp, mcp/name)
func parseItemPath(itemPath string, result *ContentRef) {
	if itemPath == "" {
		return
	}
	if slashIdx := strings.Index(itemPath, "/"); slashIdx != -1 {
		result.ItemType = itemPath[:slashIdx]
		result.ItemName = itemPath[slashIdx+1:]
	} else {
		// Just a type with no name (e.g., "mcp" for all MCP servers)
		result.ItemType = itemPath
	}
}

// IsBundle returns true if this references an entire bundle (no specific item).
func (r ContentRef) IsBundle() bool {
	return r.ItemType == ""
}

// IsFragment returns true if this references a specific fragment.
func (r ContentRef) IsFragment() bool {
	return r.ItemType == "fragments"
}

// IsPrompt returns true if this references a specific prompt.
func (r ContentRef) IsPrompt() bool {
	return r.ItemType == "prompts"
}

// IsMCP returns true if this references an MCP server.
func (r ContentRef) IsMCP() bool {
	return r.ItemType == "mcp"
}

// BundlePath returns the bundle path (remote/bundle, URL, or just bundle).
func (r ContentRef) BundlePath() string {
	if r.IsURL {
		return r.Remote
	}
	if r.Remote != "" {
		return r.Remote + "/" + r.Bundle
	}
	return r.Bundle
}

// LocalBundlePath converts a URL reference to the local storage path.
// For example:
//   - https://github.com/user/repo@v1/bundles/name -> github.com/user/repo/name
//   - github/name -> github/name (unchanged)
//   - name -> name (unchanged)
func (r ContentRef) LocalBundlePath() string {
	if !r.IsURL {
		// Not a URL, return as-is
		if r.Remote != "" {
			return r.Remote + "/" + r.Bundle
		}
		return r.Bundle
	}

	// Parse URL to extract components
	url := r.Remote

	// Remove scheme (https:// or http://)
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")

	// Remove @version if present (e.g., @v1)
	if atIdx := strings.Index(url, "@"); atIdx != -1 {
		url = url[:atIdx]
	}

	// Remove /bundles/ or /profiles/ suffix and keep bundle name
	// The r.Bundle already contains just the bundle name from extractBundleFromURL
	return url + "/" + r.Bundle
}


// Loader handles loading profiles from .scm/profiles directories.
type Loader struct {
	dirs []string
	fs   afero.Fs
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// NewLoader creates a new profile loader.
func NewLoader(dirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		dirs: dirs,
		fs:   afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// List returns all available profiles (searches subdirectories recursively).
func (l *Loader) List() ([]*Profile, error) {
	var profiles []*Profile
	seen := make(map[string]bool)

	for _, dir := range l.dirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil || !exists {
			continue
		}

		err = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip directories we can't read
			}
			if info.IsDir() {
				return nil
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				return nil
			}

			// Use relative path from dir as profile name (e.g., "github/go-developer")
			relPath, _ := filepath.Rel(dir, path)
			profileName := strings.TrimSuffix(strings.TrimSuffix(relPath, ".yaml"), ".yml")
			// Normalize path separators to forward slashes for consistency
			profileName = filepath.ToSlash(profileName)

			if seen[profileName] {
				return nil // First directory wins
			}
			seen[profileName] = true

			profile, err := l.loadFile(path)
			if err != nil {
				return nil // Skip profiles that fail to load
			}
			profile.Name = profileName
			profiles = append(profiles, profile)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan profiles directory %s: %w", dir, err)
		}
	}

	// Sort by name
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})

	return profiles, nil
}

// GetDefaults returns the names of profiles that have default: true.
func (l *Loader) GetDefaults() []string {
	profiles, err := l.List()
	if err != nil {
		return nil
	}

	var defaults []string
	for _, p := range profiles {
		if p.Default {
			defaults = append(defaults, p.Name)
		}
	}
	return defaults
}

// Load loads a profile by name (supports subdirectory paths like "github/profile-name").
func (l *Loader) Load(name string) (*Profile, error) {
	// Convert forward slashes to OS-specific separator for file lookup
	osName := filepath.FromSlash(name)

	for _, dir := range l.dirs {
		for _, ext := range []string{".yaml", ".yml"} {
			path := filepath.Join(dir, osName+ext)
			if _, err := l.fs.Stat(path); err == nil {
				profile, err := l.loadFile(path)
				if err != nil {
					return nil, err
				}
				profile.Name = name
				return profile, nil
			}
		}
	}
	return nil, fmt.Errorf("profile not found: %s", name)
}

// Exists checks if a profile exists.
func (l *Loader) Exists(name string) bool {
	_, err := l.Load(name)
	return err == nil
}

func (l *Loader) loadFile(path string) (*Profile, error) {
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, err
	}

	var profile Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	profile.Path = path
	return &profile, nil
}

// Save saves a profile to disk.
func (l *Loader) Save(profile *Profile) error {
	if len(l.dirs) == 0 {
		return fmt.Errorf("no profiles directory configured")
	}

	// Use first directory for writes
	dir := l.dirs[0]
	if err := l.fs.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create profiles directory: %w", err)
	}

	path := filepath.Join(dir, profile.Name+".yaml")

	// Create a copy without Name and Path for serialization
	toSave := *profile
	toSave.Name = ""
	toSave.Path = ""

	data, err := yaml.Marshal(&toSave)
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	if err := afero.WriteFile(l.fs, path, data, 0644); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	profile.Path = path
	return nil
}

// Delete removes a profile file.
func (l *Loader) Delete(name string) error {
	profile, err := l.Load(name)
	if err != nil {
		return err
	}
	return l.fs.Remove(profile.Path)
}

// GetProfileDirs returns profile directories from SCM paths.
func GetProfileDirs(scmPaths []string) []string {
	var dirs []string
	for _, scmPath := range scmPaths {
		profileDir := filepath.Join(scmPath, "profiles")
		if info, err := os.Stat(profileDir); err == nil && info.IsDir() {
			dirs = append(dirs, profileDir)
		}
	}
	return dirs
}

// maxProfileDepth prevents stack overflow from deeply nested or malformed configurations.
// This matches the limit used in config.ResolveProfile for consistency.
const maxProfileDepth = 64

// toLocalProfileName converts a profile reference to its local name.
// For URL references (https://, git@, file://), it parses the reference
// and returns the local path format (e.g., "github.com/owner/repo/name").
// For simple references, it returns the name unchanged.
func toLocalProfileName(name string) string {
	// Check if this is a URL reference
	if !strings.HasPrefix(name, "https://") &&
		!strings.HasPrefix(name, "http://") &&
		!strings.HasPrefix(name, "git@") &&
		!strings.HasPrefix(name, "file://") {
		// Not a URL, return as-is
		return name
	}

	// Parse the URL reference
	ref, err := remote.ParseReference(name)
	if err != nil {
		// If parsing fails, return the original name
		// (the Load call will fail with a descriptive error)
		return name
	}

	// Convert to local profile name: {remoteName}/{path}
	// LocalRemoteName() returns something like "github.com/owner/repo"
	// Path is the profile name like "go-developer"
	return ref.LocalRemoteName() + "/" + ref.Path
}

// ResolveProfile resolves a profile including its parents, returning all referenced items.
// Returns bundles, tags, and variables.
// Uses the same algorithm as config.ResolveProfile for consistency:
// - Clones visited set for each parent to handle diamond inheritance correctly
// - Enforces depth limit to prevent stack overflow
func (l *Loader) ResolveProfile(name string, visited map[string]bool) (*ResolvedProfile, error) {
	return l.resolveProfileRecursive(name, visited, 0)
}

func (l *Loader) resolveProfileRecursive(name string, visited map[string]bool, depth int) (*ResolvedProfile, error) {
	// Check depth limit (consistent with config.ResolveProfile)
	if depth > maxProfileDepth {
		return nil, fmt.Errorf("profile inheritance depth exceeds maximum (%d): possible misconfiguration", maxProfileDepth)
	}

	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[name] {
		return nil, fmt.Errorf("circular profile reference: %s", name)
	}
	visited[name] = true

	profile, err := l.Load(name)
	if err != nil {
		return nil, err
	}

	resolved := &ResolvedProfile{
		Variables: make(map[string]string),
	}

	// Resolve parents first (depth-first)
	// Clone visited map for each parent to handle diamond inheritance correctly.
	// This allows shared ancestors to be resolved through different paths.
	for _, parent := range profile.Parents {
		// Convert URL references to local profile names
		localParentName := toLocalProfileName(parent)

		// Clone visited map for this parent branch
		parentVisited := cloneVisited(visited)
		parentResolved, err := l.resolveProfileRecursive(localParentName, parentVisited, depth+1)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve parent %s: %w", parent, err)
		}
		resolved.Merge(parentResolved)
	}

	// Then apply this profile's settings (overrides parents)
	resolved.Bundles = appendUnique(resolved.Bundles, profile.Bundles...)
	resolved.Tags = appendUnique(resolved.Tags, profile.Tags...)
	for k, v := range profile.Variables {
		resolved.Variables[k] = v
	}

	return resolved, nil
}

// cloneVisited creates a copy of the visited map for branch isolation.
func cloneVisited(visited map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(visited))
	for k, v := range visited {
		clone[k] = v
	}
	return clone
}

// ResolvedProfile contains the fully resolved contents of a profile after parent inheritance.
type ResolvedProfile struct {
	Bundles   []string // All bundle references
	Tags      []string
	Variables map[string]string
}

// Merge adds items from another resolved profile.
func (r *ResolvedProfile) Merge(other *ResolvedProfile) {
	r.Bundles = appendUnique(r.Bundles, other.Bundles...)
	r.Tags = appendUnique(r.Tags, other.Tags...)
	for k, v := range other.Variables {
		if _, exists := r.Variables[k]; !exists {
			r.Variables[k] = v
		}
	}
}

func appendUnique(slice []string, items ...string) []string {
	seen := make(map[string]bool)
	for _, s := range slice {
		seen[s] = true
	}
	for _, item := range items {
		if !seen[item] {
			slice = append(slice, item)
			seen[item] = true
		}
	}
	return slice
}
