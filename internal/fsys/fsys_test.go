package fsys

import (
	"io/fs"
	"testing"
)

func TestMapFS_AddFile(t *testing.T) {
	mfs := NewMapFS()
	mfs.AddFile("/test/file.txt", []byte("content"))

	// File should exist
	if _, ok := mfs.Files["/test/file.txt"]; !ok {
		t.Error("expected file to exist")
	}

	// Parent directory should exist
	if !mfs.Dirs["/test"] {
		t.Error("expected parent directory to exist")
	}
}

func TestMapFS_ReadFile(t *testing.T) {
	mfs := NewMapFS()
	mfs.AddFile("/test.txt", []byte("hello world"))

	content, err := mfs.ReadFile("/test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(content))
	}

	// Non-existent file
	_, err = mfs.ReadFile("/nonexistent.txt")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestMapFS_Stat(t *testing.T) {
	mfs := NewMapFS()
	mfs.AddFile("/file.txt", []byte("content"))
	mfs.AddDir("/mydir")

	// File stat
	info, err := mfs.Stat("/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name() != "file.txt" {
		t.Errorf("expected name 'file.txt', got %q", info.Name())
	}
	if info.IsDir() {
		t.Error("expected file, not directory")
	}

	// Directory stat
	info, err = mfs.Stat("/mydir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}

	// Non-existent
	_, err = mfs.Stat("/nonexistent")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestMapFS_WalkDir(t *testing.T) {
	mfs := NewMapFS()
	mfs.AddDir("/root")
	mfs.AddFile("/root/file1.txt", []byte("1"))
	mfs.AddFile("/root/subdir/file2.txt", []byte("2"))

	var visited []string
	err := mfs.WalkDir("/root", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, path)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have visited root, file1, subdir, file2
	if len(visited) < 3 {
		t.Errorf("expected at least 3 entries, got %d: %v", len(visited), visited)
	}
}

func TestMapFS_WalkDir_NonExistent(t *testing.T) {
	mfs := NewMapFS()

	err := mfs.WalkDir("/nonexistent", func(path string, d fs.DirEntry, err error) error {
		return nil
	})
	if err == nil {
		t.Error("expected error for non-existent root")
	}
}

func TestOSFileSystem(t *testing.T) {
	osfs := OS()

	// Just verify it doesn't panic and returns expected types
	if osfs == nil {
		t.Error("expected non-nil filesystem")
	}

	// Stat on non-existent should error
	_, err := osfs.Stat("/this/path/should/not/exist/ever/12345")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}
