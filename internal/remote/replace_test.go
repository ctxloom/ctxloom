package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceManager_AddAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create a local file to replace with
	localFile := filepath.Join(tmpDir, "local.yaml")
	if err := os.WriteFile(localFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	manager, err := NewReplaceManager(configPath)
	if err != nil {
		t.Fatalf("NewReplaceManager failed: %v", err)
	}

	// Add replace
	if err := manager.Add("alice/security", localFile); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Get replace
	path, ok := manager.Get("alice/security")
	if !ok {
		t.Fatal("expected to find replace")
	}
	if path != localFile {
		t.Errorf("path = %q, want %q", path, localFile)
	}

	// IsReplaced
	if !manager.IsReplaced("alice/security") {
		t.Error("expected IsReplaced to return true")
	}
	if manager.IsReplaced("bob/other") {
		t.Error("expected IsReplaced to return false for unknown ref")
	}
}

func TestReplaceManager_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	localFile := filepath.Join(tmpDir, "local.yaml")
	if err := os.WriteFile(localFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	manager, err := NewReplaceManager(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Add and remove
	_ = manager.Add("alice/security", localFile)
	if err := manager.Remove("alice/security"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify removed
	if manager.IsReplaced("alice/security") {
		t.Error("expected replace to be removed")
	}

	// Remove non-existent
	if err := manager.Remove("bob/other"); err == nil {
		t.Error("expected error removing non-existent replace")
	}
}

func TestReplaceManager_List(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	localFile := filepath.Join(tmpDir, "local.yaml")
	_ = os.WriteFile(localFile, []byte("test"), 0644)

	manager, err := NewReplaceManager(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Empty list
	list := manager.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}

	// Add items
	_ = manager.Add("alice/security", localFile)
	_ = manager.Add("bob/other", localFile)

	list = manager.List()
	if len(list) != 2 {
		t.Errorf("expected 2 items, got %d", len(list))
	}
}

func TestReplaceManager_LoadReplaced(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	content := []byte("test content here")
	localFile := filepath.Join(tmpDir, "local.yaml")
	if err := os.WriteFile(localFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	manager, err := NewReplaceManager(configPath)
	if err != nil {
		t.Fatal(err)
	}

	_ = manager.Add("alice/security", localFile)

	// Load replaced content
	loaded, err := manager.LoadReplaced("alice/security")
	if err != nil {
		t.Fatalf("LoadReplaced failed: %v", err)
	}

	if string(loaded) != string(content) {
		t.Errorf("content = %q, want %q", string(loaded), string(content))
	}

	// Load non-replaced
	_, err = manager.LoadReplaced("bob/other")
	if err == nil {
		t.Error("expected error loading non-replaced ref")
	}
}

func TestReplaceManager_AddNonExistentPath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	manager, err := NewReplaceManager(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Try to add non-existent path
	err = manager.Add("alice/security", "/nonexistent/path/file.yaml")
	if err == nil {
		t.Error("expected error adding non-existent path")
	}
}

func TestReplaceManager_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	localFile := filepath.Join(tmpDir, "local.yaml")
	_ = os.WriteFile(localFile, []byte("test"), 0644)

	// Create manager and add replace
	manager1, _ := NewReplaceManager(configPath)
	_ = manager1.Add("alice/security", localFile)

	// Create new manager and verify persistence
	manager2, _ := NewReplaceManager(configPath)
	if !manager2.IsReplaced("alice/security") {
		t.Error("expected replace to persist across manager instances")
	}

	path, ok := manager2.Get("alice/security")
	if !ok || path != localFile {
		t.Errorf("persisted path = %q, want %q", path, localFile)
	}
}
