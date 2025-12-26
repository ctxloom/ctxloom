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

// CopyFragments copies all embedded context-fragments to the destination directory.
// It preserves the directory structure and tracks what was copied.
// Fragment files are set to read-only to protect from accidental edits.
// If header is non-empty, it is prepended to YAML files.
func CopyFragments(destDir string, header string) (*CopyResult, error) {
	result := &CopyResult{}

	err := fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from "context-fragments" root
		relPath, err := filepath.Rel("context-fragments", path)
		if err != nil {
			return err
		}

		// Skip the root "context-fragments" directory itself
		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(destDir, relPath), 0755)
		}

		name := d.Name()

		// Only copy .yaml, .yml, .sha256, and .distilled.yaml files
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".sha256") {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		// Read file content
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

	return result, err
}

// ListFragments returns all embedded fragment paths.
func ListFragments() ([]string, error) {
	var paths []string
	err := fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			relPath, _ := filepath.Rel("context-fragments", path)
			paths = append(paths, relPath)
		}
		return nil
	})
	return paths, err
}

// CopySelectedFragments copies only the specified context-fragments to the destination directory.
// fragmentNames should be in the format "category/name" (without extension).
// Fragment files are set to read-only to protect from accidental edits.
// Missing context-fragments are warned about but do not cause failure.
// If header is non-empty, it is prepended to YAML files.
func CopySelectedFragments(destDir string, fragmentNames []string, header string) (*CopyResult, error) {
	result := &CopyResult{}

	// Build a set of allowed context-fragments for quick lookup (normalized without extension)
	allowed := make(map[string]bool)
	for _, name := range fragmentNames {
		// Strip any extension for comparison
		name = strings.TrimSuffix(name, ".yaml")
		name = strings.TrimSuffix(name, ".yml")
		allowed[name] = true
	}

	// Track which context-fragments were found
	found := make(map[string]bool)

	err := fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from "context-fragments" root
		relPath, err := filepath.Rel("context-fragments", path)
		if err != nil {
			return err
		}

		// Skip the root "context-fragments" directory itself
		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return nil // Directories are created as needed when copying files
		}

		name := d.Name()

		// Only process .yaml, .yml, and .sha256 files
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".sha256") {
			return nil
		}

		// Get base name without extension for comparison
		baseName := strings.TrimSuffix(relPath, ".sha256")
		baseName = strings.TrimSuffix(baseName, ".yaml")
		baseName = strings.TrimSuffix(baseName, ".yml")

		// For .distilled files, check against the non-distilled name
		checkName := strings.TrimSuffix(baseName, ".distilled")

		// Check if this fragment is in the allowed set
		if !allowed[checkName] {
			return nil // Skip this fragment
		}

		// Mark as found (use the non-distilled name)
		found[checkName] = true

		destPath := filepath.Join(destDir, relPath)

		// Read file content
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

	// Warn about missing context-fragments
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

		// Only process .yaml and .yml files (not .sha256 or .distilled.yaml)
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		if strings.HasSuffix(name, ".distilled.yaml") {
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
