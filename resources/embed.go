// Package resources provides embedded static files for SCM.
package resources

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:fragments
var fragmentsFS embed.FS

//go:embed config.yaml
var configFS embed.FS

// GetEmbeddedConfig returns the embedded default config.yaml content.
func GetEmbeddedConfig() ([]byte, error) {
	return configFS.ReadFile("config.yaml")
}

// CopyFragments copies all embedded fragments to the destination directory.
// It preserves the directory structure and skips files that already exist.
func CopyFragments(destDir string) error {
	return fs.WalkDir(fragmentsFS, "fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from "fragments" root
		relPath, err := filepath.Rel("fragments", path)
		if err != nil {
			return err
		}

		// Skip the root "fragments" directory itself
		if relPath == "." {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// Skip if file already exists
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Read embedded file
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		// Write file
		return os.WriteFile(destPath, data, 0644)
	})
}

// ListFragments returns all embedded fragment paths.
func ListFragments() ([]string, error) {
	var paths []string
	err := fs.WalkDir(fragmentsFS, "fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			relPath, _ := filepath.Rel("fragments", path)
			paths = append(paths, relPath)
		}
		return nil
	})
	return paths, err
}

// CopySelectedFragments copies only the specified fragments to the destination directory.
// fragmentNames should be in the format "category/name" (without .md extension).
// Missing fragments are warned about but do not cause failure.
func CopySelectedFragments(destDir string, fragmentNames []string) error {
	// Build a set of allowed fragments for quick lookup
	allowed := make(map[string]bool)
	for _, name := range fragmentNames {
		// Normalize: add .md if missing
		if !strings.HasSuffix(name, ".md") {
			name = name + ".md"
		}
		allowed[name] = true
	}

	// Track which fragments were found
	found := make(map[string]bool)

	err := fs.WalkDir(fragmentsFS, "fragments", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from "fragments" root
		relPath, err := filepath.Rel("fragments", path)
		if err != nil {
			return err
		}

		// Skip the root "fragments" directory itself
		if relPath == "." {
			return nil
		}

		destPath := filepath.Join(destDir, relPath)

		if d.IsDir() {
			// Only create directory if it contains allowed fragments
			return nil // Directories are created as needed when copying files
		}

		// Check if this fragment is in the allowed set
		if !allowed[relPath] {
			return nil // Skip this fragment
		}

		// Mark as found
		found[relPath] = true

		// Skip if file already exists
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Read embedded file
		data, err := fragmentsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		// Write file
		return os.WriteFile(destPath, data, 0644)
	})

	if err != nil {
		return err
	}

	// Warn about missing fragments
	for name := range allowed {
		if !found[name] {
			fmt.Fprintf(os.Stderr, "Warning: embedded fragment not found: %s\n", name)
		}
	}

	return nil
}
