// Package resources provides embedded static files for SCM.
package resources

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mlcm/internal/fsys"
)

// CopyResult tracks what happened during a copy operation.
type CopyResult struct {
	Added     []string // Files that were newly created
	Updated   []string // Files that existed but were updated
	Unchanged []string // Files that existed with same content
}

// Merge combines another CopyResult into this one.
func (r *CopyResult) Merge(other *CopyResult) {
	if other == nil {
		return
	}
	r.Added = append(r.Added, other.Added...)
	r.Updated = append(r.Updated, other.Updated...)
	r.Unchanged = append(r.Unchanged, other.Unchanged...)
}

// Total returns the total number of files processed.
func (r *CopyResult) Total() int {
	return len(r.Added) + len(r.Updated) + len(r.Unchanged)
}

// copyStatus indicates what happened when copying a file.
type copyStatus int

const (
	copyStatusAdded copyStatus = iota
	copyStatusUpdated
	copyStatusUnchanged
)

// ProjectHeader is the comment prepended to files copied to project directories.
const ProjectHeader = `# ┌─────────────────────────────────────────────────────────────────────────────┐
# │ DO NOT EDIT - Changes will be overwritten on next 'mlcm init'              │
# │ To customize: edit ~/.mlcm/context-fragments/... then re-run 'mlcm init'   │
# └─────────────────────────────────────────────────────────────────────────────┘
`

// copyFileWithStatus copies data to destPath and returns the status.
// If header is non-empty and file is YAML, prepends the header.
func copyFileWithStatus(destPath string, data []byte, header string) (copyStatus, error) {
	// Prepend header to YAML files if specified
	finalData := data
	if header != "" && (strings.HasSuffix(destPath, ".yaml") || strings.HasSuffix(destPath, ".yml")) {
		finalData = append([]byte(header), data...)
	}

	// Check if destination exists and compare content
	existing, err := os.ReadFile(destPath)
	if err == nil {
		// File exists, check if content is the same
		if bytes.Equal(existing, finalData) {
			return copyStatusUnchanged, nil
		}
		// Content differs, will update
		if err := fsys.WriteProtected(destPath, finalData); err != nil {
			return 0, err
		}
		return copyStatusUpdated, nil
	}

	// File doesn't exist, create it
	if err := fsys.WriteProtected(destPath, finalData); err != nil {
		return 0, err
	}
	return copyStatusAdded, nil
}

//go:embed all:context-fragments
var fragmentsFS embed.FS

//go:embed all:prompts
var promptsFS embed.FS

//go:embed config.yaml
var configFS embed.FS

// yamlFragment represents the YAML structure of a fragment file.
type yamlFragment struct {
	Version   string            `yaml:"version,omitempty"`
	Author    string            `yaml:"author,omitempty"`
	Tags      []string          `yaml:"tags,omitempty"`
	Variables []string          `yaml:"variables,omitempty"`
	Content   string            `yaml:"content"`
	VarValues map[string]string `yaml:"var_values,omitempty"`
}

// GetEmbeddedConfig returns the embedded default config.yaml content.
func GetEmbeddedConfig() ([]byte, error) {
	return configFS.ReadFile("config.yaml")
}

// GetFragmentSchema returns the embedded JSON schema for fragment validation.
func GetFragmentSchema() ([]byte, error) {
	return fragmentsFS.ReadFile("context-fragments/standards/fragment-schema.json")
}

// CopyFragments copies embedded context-fragments to the destination directory.
//
// Fragments are copied to the project directory to ensure all developers
// working on the project use the same context - providing reproducibility.
//
// If filter is non-nil, only fragments matching the filter are copied.
// Filter entries are paths like "style/direct" (without extension).
// If header is non-empty, it is prepended to YAML files.
func CopyFragments(destDir string, filter []string, header string) (*CopyResult, error) {
	result := &CopyResult{}

	// Build filter set if provided
	var allowed map[string]bool
	var found map[string]bool
	if len(filter) > 0 {
		allowed = make(map[string]bool)
		found = make(map[string]bool)
		for _, name := range filter {
			name = strings.TrimSuffix(name, ".yaml")
			name = strings.TrimSuffix(name, ".yml")
			allowed[name] = true
		}
	}

	err := fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel("context-fragments", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			if allowed == nil {
				return os.MkdirAll(filepath.Join(destDir, relPath), 0755)
			}
			return nil // Directories created as needed when filtering
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		// Apply filter if set
		if allowed != nil {
			baseName := strings.TrimSuffix(relPath, ".yaml")
			baseName = strings.TrimSuffix(baseName, ".yml")
			if !allowed[baseName] {
				return nil
			}
			found[baseName] = true
		}

		destPath := filepath.Join(destDir, relPath)
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		status, err := copyFileWithStatus(destPath, data, header)
		if err != nil {
			return err
		}

		switch status {
		case copyStatusAdded:
			result.Added = append(result.Added, relPath)
		case copyStatusUpdated:
			result.Updated = append(result.Updated, relPath)
		case copyStatusUnchanged:
			result.Unchanged = append(result.Unchanged, relPath)
		}
		return nil
	})

	if err != nil {
		return result, err
	}

	// Warn about missing fragments when filtering
	for name := range allowed {
		if !found[name] {
			fmt.Fprintf(os.Stderr, "Warning: embedded fragment not found: %s\n", name)
		}
	}

	return result, nil
}

// ListPrompts returns all embedded prompt paths.
func ListPrompts() ([]string, error) {
	var paths []string
	err := fs.WalkDir(promptsFS, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			relPath, _ := filepath.Rel("prompts", path)
			paths = append(paths, relPath)
		}
		return nil
	})
	return paths, err
}

// GetPrompt returns the content of a specific embedded prompt.
// Prompts are stored as YAML files, and this returns the content field.
func GetPrompt(name string) ([]byte, error) {
	// Try YAML extensions first
	var data []byte
	var err error

	for _, ext := range []string{".yaml", ".yml"} {
		data, err = promptsFS.ReadFile(filepath.Join("prompts", name+ext))
		if err == nil {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("prompt not found: %s", name)
	}

	// Parse YAML and extract content
	var frag yamlFragment
	if err := yaml.Unmarshal(data, &frag); err != nil {
		return nil, fmt.Errorf("failed to parse prompt YAML: %w", err)
	}

	return []byte(strings.TrimSpace(frag.Content)), nil
}

// CopyPrompts copies all embedded prompts to the destination directory.
// Prompt files are set to read-only to protect from accidental edits.
func CopyPrompts(destDir string) error {
	return fs.WalkDir(promptsFS, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel("prompts", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(destDir, relPath), 0755)
		}

		name := d.Name()

		// Only copy .yaml and .yml files
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		// Skip if file already exists
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Read and copy file as-is
		data, err := promptsFS.ReadFile(path)
		if err != nil {
			return err
		}

		return fsys.WriteProtected(destPath, data)
	})
}

// CopyTaggedFragments copies embedded fragments with the specified tag to the destination directory.
// Fragment files are set to read-only to protect from accidental edits.
// If header is non-empty, it is prepended to YAML files.
func CopyTaggedFragments(destDir string, tag string, header string) (*CopyResult, error) {
	result := &CopyResult{}

	err := fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel("context-fragments", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(destDir, relPath), 0755)
		}

		name := d.Name()

		// Only process .yaml and .yml files
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		// Read file to check tags
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Parse YAML to check for tag
		var frag yamlFragment
		if err := yaml.Unmarshal(data, &frag); err != nil {
			return nil // Skip files that don't parse
		}

		// Check if fragment has the required tag
		hasTag := false
		for _, t := range frag.Tags {
			if strings.EqualFold(t, tag) {
				hasTag = true
				break
			}
		}
		if !hasTag {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		status, err := copyFileWithStatus(destPath, data, header)
		if err != nil {
			return err
		}

		switch status {
		case copyStatusAdded:
			result.Added = append(result.Added, relPath)
		case copyStatusUpdated:
			result.Updated = append(result.Updated, relPath)
		case copyStatusUnchanged:
			result.Unchanged = append(result.Unchanged, relPath)
		}
		return nil
	})

	return result, err
}

// CopyTaggedPrompts copies embedded prompts with the specified tag to the destination directory.
// Prompt files are set to read-only to protect from accidental edits.
// If header is non-empty, it is prepended to YAML files.
func CopyTaggedPrompts(destDir string, tag string, header string) (*CopyResult, error) {
	result := &CopyResult{}

	err := fs.WalkDir(promptsFS, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel("prompts", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(destDir, relPath), 0755)
		}

		name := d.Name()

		// Only process .yaml and .yml files
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		// Read file to check tags
		data, err := promptsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Parse YAML to check for tag
		var frag yamlFragment
		if err := yaml.Unmarshal(data, &frag); err != nil {
			return nil // Skip files that don't parse
		}

		// Check if prompt has the required tag
		hasTag := false
		for _, t := range frag.Tags {
			if strings.EqualFold(t, tag) {
				hasTag = true
				break
			}
		}
		if !hasTag {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		status, err := copyFileWithStatus(destPath, data, header)
		if err != nil {
			return err
		}

		switch status {
		case copyStatusAdded:
			result.Added = append(result.Added, relPath)
		case copyStatusUpdated:
			result.Updated = append(result.Updated, relPath)
		case copyStatusUnchanged:
			result.Unchanged = append(result.Unchanged, relPath)
		}
		return nil
	})

	return result, err
}
