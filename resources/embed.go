// Package resources provides embedded static files for SCM.
package resources

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:fragments
var fragmentsFS embed.FS

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
