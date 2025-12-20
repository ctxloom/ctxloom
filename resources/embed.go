// Package resources provides embedded static files for SCM.
package resources

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mlcm/internal/fsys"
)

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
// It preserves the directory structure and skips files that already exist.
// Fragment files are set to read-only to protect from accidental edits.
func CopyFragments(destDir string) error {
	return fs.WalkDir(fragmentsFS, "context-fragments", func(path string, d fs.DirEntry, err error) error {
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

		// Skip if file already exists
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Read and copy file as-is
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		return fsys.WriteProtected(destPath, data)
	})
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
func CopySelectedFragments(destDir string, fragmentNames []string) error {
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

		// Skip if file already exists
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Read and copy file as-is
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		return fsys.WriteProtected(destPath, data)
	})

	if err != nil {
		return err
	}

	// Warn about missing context-fragments
	for name := range allowed {
		if !found[name] {
			fmt.Fprintf(os.Stderr, "Warning: embedded fragment not found: %s\n", name)
		}
	}

	return nil
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
