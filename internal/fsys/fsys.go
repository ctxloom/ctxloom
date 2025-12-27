// Package fsys provides filesystem abstractions for dependency injection and testing.
package fsys

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FS defines the filesystem operations required by the application.
// This interface enables dependency injection for testing.
type FS interface {
	// ReadFile reads the named file and returns its contents.
	ReadFile(name string) ([]byte, error)

	// Stat returns file info for the named file.
	Stat(name string) (fs.FileInfo, error)

	// WalkDir walks the file tree rooted at root, calling fn for each file or directory.
	WalkDir(root string, fn fs.WalkDirFunc) error
}

// OSFileSystem implements FS using the real filesystem.
type OSFileSystem struct{}

// OS returns a filesystem implementation using the real OS filesystem.
func OS() FS {
	return &OSFileSystem{}
}

// ReadFile reads the named file using os.ReadFile.
func (o *OSFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

// Stat returns file info using os.Stat.
func (o *OSFileSystem) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

// WalkDir walks the directory tree using filepath.WalkDir.
func (o *OSFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

// MapFS implements FS using an in-memory map for testing.
type MapFS struct {
	Files map[string][]byte
	Dirs  map[string]bool
}

// NewMapFS creates a new in-memory filesystem for testing.
func NewMapFS() *MapFS {
	return &MapFS{
		Files: make(map[string][]byte),
		Dirs:  make(map[string]bool),
	}
}

// AddFile adds a file to the in-memory filesystem.
func (m *MapFS) AddFile(path string, content []byte) {
	m.Files[path] = content
	// Ensure parent directories exist
	dir := filepath.Dir(path)
	for dir != "." && dir != "/" {
		m.Dirs[dir] = true
		dir = filepath.Dir(dir)
	}
}

// AddDir adds a directory to the in-memory filesystem.
func (m *MapFS) AddDir(path string) {
	m.Dirs[path] = true
}

// ReadFile reads a file from the in-memory filesystem.
func (m *MapFS) ReadFile(name string) ([]byte, error) {
	if content, ok := m.Files[name]; ok {
		return content, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Stat returns file info for a file in the in-memory filesystem.
func (m *MapFS) Stat(name string) (fs.FileInfo, error) {
	if _, ok := m.Files[name]; ok {
		return &mapFileInfo{name: filepath.Base(name), isDir: false}, nil
	}
	if m.Dirs[name] {
		return &mapFileInfo{name: filepath.Base(name), isDir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// WalkDir walks the in-memory filesystem.
func (m *MapFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	// Check if root exists
	if !m.Dirs[root] {
		if _, ok := m.Files[root]; !ok {
			return &fs.PathError{Op: "walk", Path: root, Err: fs.ErrNotExist}
		}
	}

	// Walk root first
	if m.Dirs[root] {
		if err := fn(root, &mapDirEntry{name: filepath.Base(root), isDir: true}, nil); err != nil {
			if err == filepath.SkipDir || err == filepath.SkipAll {
				return nil
			}
			return err
		}
	}

	// Collect and sort paths for deterministic iteration
	var paths []string
	for path := range m.Files {
		if len(path) > len(root) && path[:len(root)] == root && (path[len(root)] == '/' || root == ".") {
			paths = append(paths, path)
		}
	}
	for path := range m.Dirs {
		if path != root && len(path) > len(root) && path[:len(root)] == root && (path[len(root)] == '/' || root == ".") {
			paths = append(paths, path)
		}
	}

	// Sort paths for consistent ordering
	sortStrings(paths)

	for _, path := range paths {
		isDir := m.Dirs[path]
		entry := &mapDirEntry{name: filepath.Base(path), isDir: isDir}
		if err := fn(path, entry, nil); err != nil {
			if err == filepath.SkipDir {
				continue
			}
			if err == filepath.SkipAll {
				return nil
			}
			return err
		}
	}

	return nil
}

// sortStrings sorts a slice of strings in place (simple insertion sort for small slices).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// mapFileInfo implements fs.FileInfo for MapFS.
type mapFileInfo struct {
	name  string
	isDir bool
}

func (m *mapFileInfo) Name() string       { return m.name }
func (m *mapFileInfo) Size() int64        { return 0 }
func (m *mapFileInfo) Mode() fs.FileMode  { return 0644 }
func (m *mapFileInfo) ModTime() time.Time { return time.Time{} }
func (m *mapFileInfo) IsDir() bool        { return m.isDir }
func (m *mapFileInfo) Sys() any           { return nil }

// mapDirEntry implements fs.DirEntry for MapFS.
type mapDirEntry struct {
	name  string
	isDir bool
}

func (m *mapDirEntry) Name() string               { return m.name }
func (m *mapDirEntry) IsDir() bool                { return m.isDir }
func (m *mapDirEntry) Type() fs.FileMode          { return 0 }
func (m *mapDirEntry) Info() (fs.FileInfo, error) { return &mapFileInfo{name: m.name, isDir: m.isDir}, nil }

// MakeWritable makes a file writable if it exists.
// This is useful before overwriting read-only files.
// Errors are ignored if the file doesn't exist.
func MakeWritable(path string) {
	_ = os.Chmod(path, 0644)
}

// MakeReadOnly sets a file to read-only (0444).
// This protects copied/distilled fragments from accidental edits.
func MakeReadOnly(path string) error {
	return os.Chmod(path, 0444)
}

// ProtectedFile wraps an os.File to automatically manage write protection.
// On open: makes file writable (if exists)
// On close: makes file read-only (0444)
type ProtectedFile struct {
	*os.File
	path string
}

// CreateProtected creates a new file with automatic write protection on close.
// If the file exists and is read-only, it is made writable first.
func CreateProtected(path string) (*ProtectedFile, error) {
	// Make writable if exists
	MakeWritable(path)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	// Create/open file for writing
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	return &ProtectedFile{File: f, path: path}, nil
}

// OpenProtected opens an existing file for writing with automatic write protection on close.
// The file is made writable before opening.
func OpenProtected(path string, flag int, perm os.FileMode) (*ProtectedFile, error) {
	// Make writable if exists
	MakeWritable(path)

	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}

	return &ProtectedFile{File: f, path: path}, nil
}

// Close closes the file and makes it read-only.
func (p *ProtectedFile) Close() error {
	err := p.File.Close()
	if err != nil {
		return err
	}
	return MakeReadOnly(p.path)
}

// WriteProtected writes data to a file and makes it read-only.
// This is a convenience function for simple write-and-protect operations.
func WriteProtected(path string, data []byte) error {
	f, err := CreateProtected(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// EmbedFS wraps embed.FS to implement the FS interface.
// The root parameter specifies the subdirectory within the embed.FS to use as the root.
type EmbedFS struct {
	fs   embed.FS
	root string
}

// NewEmbedFS creates a new EmbedFS wrapper.
// root is the subdirectory within the embed.FS to use as the root for all operations.
func NewEmbedFS(efs embed.FS, root string) *EmbedFS {
	return &EmbedFS{fs: efs, root: root}
}

// ReadFile reads a file from the embedded filesystem.
func (e *EmbedFS) ReadFile(name string) ([]byte, error) {
	return e.fs.ReadFile(filepath.Join(e.root, name))
}

// Stat returns file info for a file in the embedded filesystem.
func (e *EmbedFS) Stat(name string) (fs.FileInfo, error) {
	f, err := e.fs.Open(filepath.Join(e.root, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// WalkDir walks the embedded filesystem.
func (e *EmbedFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	fullRoot := filepath.Join(e.root, root)
	return fs.WalkDir(e.fs, fullRoot, func(path string, d fs.DirEntry, err error) error {
		// Convert path back to be relative to our virtual root
		relPath, relErr := filepath.Rel(e.root, path)
		if relErr != nil {
			relPath = path
		}
		return fn(relPath, d, err)
	})
}
