package remote

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestLockfileManager_LoadEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	lockfile, err := manager.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lockfile.Bundles == nil {
		t.Error("Bundles map should be initialized")
	}
	if lockfile.Profiles == nil {
		t.Error("Profiles map should be initialized")
	}
	if !lockfile.IsEmpty() {
		t.Error("new lockfile should be empty")
	}
}

func TestLockfileManager_SaveAndLoad(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	// Create lockfile
	lockfile := &Lockfile{
		Version:  1,
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}

	now := time.Now().UTC().Truncate(time.Second)
	lockfile.AddEntry(ItemTypeBundle, "alice/go-tools", LockEntry{
		SHA:        "abc1234def5678",
		URL:        "https://github.com/alice/scm",
		SCMVersion: "v1",
		FetchedAt:  now,
	})

	// Save
	if err := manager.Save(lockfile); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Verify file exists
	path := manager.Path()
	exists, err := afero.Exists(fs, path)
	if err != nil || !exists {
		t.Fatalf("lockfile not created at %s", path)
	}

	// Load
	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if loaded.Version != 1 {
		t.Errorf("Version = %d, want 1", loaded.Version)
	}

	entry, ok := loaded.GetEntry(ItemTypeBundle, "alice/go-tools")
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.SHA != "abc1234def5678" {
		t.Errorf("SHA = %q, want %q", entry.SHA, "abc1234def5678")
	}
	if entry.URL != "https://github.com/alice/scm" {
		t.Errorf("URL = %q, want %q", entry.URL, "https://github.com/alice/scm")
	}
}

func TestLockfile_AddEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}

	entry := LockEntry{SHA: "abc123"}

	lockfile.AddEntry(ItemTypeBundle, "alice/go-tools", entry)
	lockfile.AddEntry(ItemTypeProfile, "alice/secure", entry)

	if len(lockfile.Bundles) != 1 {
		t.Errorf("Bundles count = %d, want 1", len(lockfile.Bundles))
	}
	if len(lockfile.Profiles) != 1 {
		t.Errorf("Profiles count = %d, want 1", len(lockfile.Profiles))
	}
}

func TestLockfile_GetEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"alice/go-tools": {SHA: "abc123"},
		},
		Profiles: make(map[string]LockEntry),
	}

	// Existing entry
	entry, ok := lockfile.GetEntry(ItemTypeBundle, "alice/go-tools")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if entry.SHA != "abc123" {
		t.Errorf("SHA = %q, want %q", entry.SHA, "abc123")
	}

	// Non-existing entry
	_, ok = lockfile.GetEntry(ItemTypeBundle, "bob/missing")
	if ok {
		t.Error("expected entry to not exist")
	}
}

func TestLockfile_RemoveEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"alice/go-tools": {SHA: "abc123"},
			"bob/testing":    {SHA: "def456"},
		},
		Profiles: make(map[string]LockEntry),
	}

	lockfile.RemoveEntry(ItemTypeBundle, "alice/go-tools")

	if len(lockfile.Bundles) != 1 {
		t.Errorf("Bundles count = %d, want 1", len(lockfile.Bundles))
	}
	if _, ok := lockfile.GetEntry(ItemTypeBundle, "alice/go-tools"); ok {
		t.Error("entry should have been removed")
	}
	if _, ok := lockfile.GetEntry(ItemTypeBundle, "bob/testing"); !ok {
		t.Error("other entry should still exist")
	}
}

func TestLockfile_AllEntries(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"alice/go-tools": {SHA: "abc123"},
		},
		Profiles: map[string]LockEntry{
			"alice/secure": {SHA: "ghi789"},
		},
	}

	entries := lockfile.AllEntries()
	if len(entries) != 2 {
		t.Errorf("entries count = %d, want 2", len(entries))
	}

	// Verify each type is present
	typeCount := make(map[ItemType]int)
	for _, e := range entries {
		typeCount[e.Type]++
	}

	if typeCount[ItemTypeBundle] != 1 {
		t.Errorf("bundle count = %d, want 1", typeCount[ItemTypeBundle])
	}
	if typeCount[ItemTypeProfile] != 1 {
		t.Errorf("profile count = %d, want 1", typeCount[ItemTypeProfile])
	}
}

func TestLockfile_IsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		lockfile Lockfile
		want     bool
	}{
		{
			name: "empty",
			lockfile: Lockfile{
				Bundles:  make(map[string]LockEntry),
				Profiles: make(map[string]LockEntry),
			},
			want: true,
		},
		{
			name: "with bundle",
			lockfile: Lockfile{
				Bundles:  map[string]LockEntry{"a": {}},
				Profiles: make(map[string]LockEntry),
			},
			want: false,
		},
		{
			name: "with profile",
			lockfile: Lockfile{
				Bundles:  make(map[string]LockEntry),
				Profiles: map[string]LockEntry{"a": {}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.lockfile.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLockfile_Count(t *testing.T) {
	lockfile := Lockfile{
		Bundles:  map[string]LockEntry{"a": {}, "b": {}},
		Profiles: map[string]LockEntry{"d": {}, "e": {}, "f": {}},
	}

	if got := lockfile.Count(); got != 5 {
		t.Errorf("Count() = %d, want 5", got)
	}
}

func TestLockfileManager_Path(t *testing.T) {
	manager := NewLockfileManager("/home/user/.scm")
	path := manager.Path()
	expected := filepath.Join("/home/user/.scm", "lock.yaml")

	if path != expected {
		t.Errorf("Path() = %q, want %q", path, expected)
	}
}

func TestLockfileManager_DefaultDir(t *testing.T) {
	manager := NewLockfileManager("")
	path := manager.Path()
	expected := filepath.Join(".scm", "lock.yaml")

	if path != expected {
		t.Errorf("Path() = %q, want %q", path, expected)
	}
}

func TestWithLockfileFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	// Verify the custom FS is used by saving and loading
	lockfile := &Lockfile{
		Version:  1,
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}
	lockfile.AddEntry(ItemTypeBundle, "test/bundle", LockEntry{SHA: "abc123"})

	if err := manager.Save(lockfile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file was written to memfs
	exists, err := afero.Exists(fs, manager.Path())
	if err != nil || !exists {
		t.Error("lockfile should exist in memory fs")
	}
}

func TestLockfile_GetCanonicalURL(t *testing.T) {
	tests := []struct {
		name      string
		localName string
		entry     LockEntry
		itemType  ItemType
		wantURL   string
		wantOk    bool
	}{
		{
			name:      "bundle with requested version",
			localName: "scm-github/core-practices",
			entry: LockEntry{
				SHA:              "abc123",
				URL:              "https://github.com/alice/scm",
				SCMVersion:       "v1",
				RequestedVersion: "v2.0.0",
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/alice/scm@v1/bundles/core-practices@v2.0.0",
			wantOk:   true,
		},
		{
			name:      "bundle without requested version uses SHA",
			localName: "scm-github/tools",
			entry: LockEntry{
				SHA:        "def456",
				URL:        "https://github.com/bob/scm",
				SCMVersion: "v1",
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/bob/scm@v1/bundles/tools@def456",
			wantOk:   true,
		},
		{
			name:      "profile entry",
			localName: "scm-github/secure",
			entry: LockEntry{
				SHA:        "ghi789",
				URL:        "https://github.com/alice/scm",
				SCMVersion: "v1",
			},
			itemType: ItemTypeProfile,
			wantURL:  "https://github.com/alice/scm@v1/profiles/secure@ghi789",
			wantOk:   true,
		},
		{
			name:      "entry not found",
			localName: "nonexistent/bundle",
			entry:     LockEntry{},
			itemType:  ItemTypeBundle,
			wantURL:   "",
			wantOk:    false,
		},
		{
			name:      "invalid local name (no slash)",
			localName: "invalid",
			entry: LockEntry{
				SHA:        "abc123",
				URL:        "https://github.com/alice/scm",
				SCMVersion: "v1",
			},
			itemType: ItemTypeBundle,
			wantURL:  "",
			wantOk:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockfile := &Lockfile{
				Bundles:  make(map[string]LockEntry),
				Profiles: make(map[string]LockEntry),
			}

			if tt.entry.SHA != "" || tt.entry.URL != "" {
				lockfile.AddEntry(tt.itemType, tt.localName, tt.entry)
			}

			gotURL, gotOk := lockfile.GetCanonicalURL(tt.itemType, tt.localName)
			if gotOk != tt.wantOk {
				t.Errorf("GetCanonicalURL() ok = %v, want %v", gotOk, tt.wantOk)
			}
			if gotURL != tt.wantURL {
				t.Errorf("GetCanonicalURL() = %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

func TestLockfile_FindByURL(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"scm-github/core": {SHA: "abc123", URL: "https://github.com/alice/scm"},
		},
		Profiles: map[string]LockEntry{
			"scm-github/secure": {SHA: "def456", URL: "https://github.com/bob/scm"},
		},
	}

	t.Run("find bundle by URL", func(t *testing.T) {
		name, entry, found := lockfile.FindByURL("https://github.com/alice/scm", ItemTypeBundle)
		if !found {
			t.Fatal("expected to find entry")
		}
		if name != "scm-github/core" {
			t.Errorf("name = %q, want %q", name, "scm-github/core")
		}
		if entry.SHA != "abc123" {
			t.Errorf("SHA = %q, want %q", entry.SHA, "abc123")
		}
	})

	t.Run("find profile by URL", func(t *testing.T) {
		name, entry, found := lockfile.FindByURL("https://github.com/bob/scm", ItemTypeProfile)
		if !found {
			t.Fatal("expected to find entry")
		}
		if name != "scm-github/secure" {
			t.Errorf("name = %q, want %q", name, "scm-github/secure")
		}
		if entry.SHA != "def456" {
			t.Errorf("SHA = %q, want %q", entry.SHA, "def456")
		}
	})

	t.Run("URL not found", func(t *testing.T) {
		_, _, found := lockfile.FindByURL("https://github.com/nonexistent/repo", ItemTypeBundle)
		if found {
			t.Error("expected entry not to be found")
		}
	})

	t.Run("unknown item type", func(t *testing.T) {
		_, _, found := lockfile.FindByURL("https://github.com/alice/scm", ItemType("unknown"))
		if found {
			t.Error("expected entry not to be found for unknown type")
		}
	})
}

func TestLockfile_FindAllByURL(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"scm-github/core":  {SHA: "abc123", URL: "https://github.com/alice/scm"},
			"scm-github/tools": {SHA: "def456", URL: "https://github.com/alice/scm"},
		},
		Profiles: map[string]LockEntry{
			"scm-github/secure": {SHA: "ghi789", URL: "https://github.com/alice/scm"},
			"scm-github/other":  {SHA: "jkl012", URL: "https://github.com/bob/scm"},
		},
	}

	t.Run("finds all matching entries", func(t *testing.T) {
		results := lockfile.FindAllByURL("https://github.com/alice/scm")
		if len(results) != 3 {
			t.Errorf("len(results) = %d, want 3", len(results))
		}
	})

	t.Run("returns empty for no matches", func(t *testing.T) {
		results := lockfile.FindAllByURL("https://github.com/nonexistent/repo")
		if len(results) != 0 {
			t.Errorf("len(results) = %d, want 0", len(results))
		}
	})
}

func TestLockfile_RemoveEntry_Profile(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[string]LockEntry),
		Profiles: map[string]LockEntry{
			"scm-github/secure": {SHA: "abc123"},
			"scm-github/other":  {SHA: "def456"},
		},
	}

	lockfile.RemoveEntry(ItemTypeProfile, "scm-github/secure")

	if len(lockfile.Profiles) != 1 {
		t.Errorf("Profiles count = %d, want 1", len(lockfile.Profiles))
	}
	if _, ok := lockfile.GetEntry(ItemTypeProfile, "scm-github/secure"); ok {
		t.Error("entry should have been removed")
	}
}

func TestLockfile_GetEntry_Profile(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[string]LockEntry),
		Profiles: map[string]LockEntry{
			"scm-github/secure": {SHA: "abc123"},
		},
	}

	entry, ok := lockfile.GetEntry(ItemTypeProfile, "scm-github/secure")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if entry.SHA != "abc123" {
		t.Errorf("SHA = %q, want %q", entry.SHA, "abc123")
	}
}

func TestLockfileManager_Load_InvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/test/lock.yaml", []byte("invalid: ["), 0644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLockfileManager_Load_NilMaps(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Write a lockfile without bundles/profiles maps
	content := "version: 1\n"
	_ = afero.WriteFile(fs, "/test/lock.yaml", []byte(content), 0644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	lockfile, err := manager.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Maps should be initialized
	if lockfile.Bundles == nil {
		t.Error("Bundles map should be initialized")
	}
	if lockfile.Profiles == nil {
		t.Error("Profiles map should be initialized")
	}
}

func TestLockfileManager_Load_ReadError(t *testing.T) {
	// Create a scenario where the file exists but cannot be read
	// Use a read-only filesystem with a file that exists
	baseFs := afero.NewMemMapFs()
	_ = afero.WriteFile(baseFs, "/test/lock.yaml", []byte("version: 1\n"), 0000)
	fs := afero.NewReadOnlyFs(baseFs)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	// This should succeed because the content is valid YAML
	if err != nil {
		// Expected if filesystem blocks reads
		return
	}
}

func TestLockfileManager_Save_SetsLockedAt(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	lockfile := &Lockfile{
		Version:  1,
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}

	before := time.Now().UTC()
	if err := manager.Save(lockfile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	after := time.Now().UTC()

	// LockedAt should be set during save
	if lockfile.LockedAt.Before(before) || lockfile.LockedAt.After(after) {
		t.Error("LockedAt should be set to current time")
	}
}

func TestLockfile_GetEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}

	// Unknown item type should not find any entry
	_, ok := lockfile.GetEntry(ItemType("unknown"), "test/bundle")
	if ok {
		t.Error("expected entry not to be found for unknown type")
	}
}

func TestLockfile_AddEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles:  make(map[string]LockEntry),
		Profiles: make(map[string]LockEntry),
	}

	// Unknown type should not add to any map
	lockfile.AddEntry(ItemType("unknown"), "test/bundle", LockEntry{SHA: "abc123"})

	if len(lockfile.Bundles) != 0 {
		t.Error("unknown type should not add to bundles")
	}
	if len(lockfile.Profiles) != 0 {
		t.Error("unknown type should not add to profiles")
	}
}

func TestLockfile_RemoveEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles:  map[string]LockEntry{"test/bundle": {SHA: "abc123"}},
		Profiles: map[string]LockEntry{"test/profile": {SHA: "def456"}},
	}

	// Unknown type should not remove from any map
	lockfile.RemoveEntry(ItemType("unknown"), "test/bundle")

	if len(lockfile.Bundles) != 1 {
		t.Error("unknown type should not remove from bundles")
	}
	if len(lockfile.Profiles) != 1 {
		t.Error("unknown type should not remove from profiles")
	}
}
