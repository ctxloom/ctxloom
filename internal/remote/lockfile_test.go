package remote

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
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
	if !lockfile.IsEmpty() {
		t.Error("new lockfile should be empty")
	}
}

func TestLockfileManager_SaveAndLoad(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	// Create lockfile
	lockfile := &Lockfile{
		Version: 1,
		Bundles: make(map[string]LockEntry),
	}

	now := time.Now().UTC().Truncate(time.Second)
	lockfile.AddEntry(ItemTypeBundle, "alice/go-tools", LockEntry{
		SHA:       "abc1234def5678",
		URL:       "https://github.com/alice/ctxloom",
		FetchedAt: now,
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
	if entry.URL != "https://github.com/alice/ctxloom" {
		t.Errorf("URL = %q, want %q", entry.URL, "https://github.com/alice/ctxloom")
	}
}

func TestLockfileManager_LoadSelfHealsLegacyCtxloomVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	path := manager.Path()

	legacy := "version: 1\n" +
		"bundles:\n" +
		"  alice/go-tools:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/ctxloom\n" +
		"    ctxloom_version: v1\n"
	if err := afero.WriteFile(fs, path, []byte(legacy), 0644); err != nil {
		t.Fatalf("seed legacy lockfile: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry, ok := loaded.GetEntry(ItemTypeBundle, "alice/go-tools")
	if !ok {
		t.Fatal("entry not found after self-heal")
	}
	if entry.SHA != "abc1234" {
		t.Errorf("SHA = %q, want %q (real fields must survive)", entry.SHA, "abc1234")
	}

	// The cleaned form is written back to disk up front.
	onDisk, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(onDisk), "ctxloom_version") {
		t.Errorf("legacy ctxloom_version not stripped from disk:\n%s", onDisk)
	}
}

func TestLockfile_AddEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[string]LockEntry),
	}

	entry := LockEntry{SHA: "abc123"}

	lockfile.AddEntry(ItemTypeBundle, "alice/go-tools", entry)

	if len(lockfile.Bundles) != 1 {
		t.Errorf("Bundles count = %d, want 1", len(lockfile.Bundles))
	}
}

func TestLockfile_GetEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"alice/go-tools": {SHA: "abc123"},
		},
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
	}

	entries := lockfile.AllEntries()
	if len(entries) != 1 {
		t.Errorf("entries count = %d, want 1", len(entries))
	}

	// Verify each type is present
	typeCount := make(map[ItemType]int)
	for _, e := range entries {
		typeCount[e.Type]++
	}

	if typeCount[ItemTypeBundle] != 1 {
		t.Errorf("bundle count = %d, want 1", typeCount[ItemTypeBundle])
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
				Bundles: make(map[string]LockEntry),
			},
			want: true,
		},
		{
			name: "with bundle",
			lockfile: Lockfile{
				Bundles: map[string]LockEntry{"a": {}},
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
		Bundles: map[string]LockEntry{"a": {}, "b": {}},
	}

	if got := lockfile.Count(); got != 2 {
		t.Errorf("Count() = %d, want 2", got)
	}
}

func TestLockfileManager_Path(t *testing.T) {
	manager := NewLockfileManager("/home/user/.ctxloom")
	path := manager.Path()
	expected := filepath.Join("/home/user/.ctxloom", "lock.yaml")

	if path != expected {
		t.Errorf("Path() = %q, want %q", path, expected)
	}
}

func TestLockfileManager_DefaultDir(t *testing.T) {
	manager := NewLockfileManager("")
	path := manager.Path()
	expected := filepath.Join(".ctxloom", "lock.yaml")

	if path != expected {
		t.Errorf("Path() = %q, want %q", path, expected)
	}
}

func TestWithLockfileFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	// Verify the custom FS is used by saving and loading
	lockfile := &Lockfile{
		Version: 1,
		Bundles: make(map[string]LockEntry),
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
			localName: "https://github.com/alice/ctxloom@bundles/core-practices",
			entry: LockEntry{
				SHA:              "abc123",
				URL:              "https://github.com/alice/ctxloom",
				RequestedVersion: "v2.0.0",
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/alice/ctxloom@bundles/core-practices@v2.0.0",
			wantOk:   true,
		},
		{
			name:      "bundle without requested version uses SHA",
			localName: "https://github.com/bob/ctxloom@bundles/tools",
			entry: LockEntry{
				SHA: "def456",
				URL: "https://github.com/bob/ctxloom",
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/bob/ctxloom@bundles/tools@def456",
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
			name:      "legacy remote/path key is not supported",
			localName: "ctxloom-github/core-practices",
			entry: LockEntry{
				SHA: "abc123",
				URL: "https://github.com/alice/ctxloom",
			},
			itemType: ItemTypeBundle,
			wantURL:  "",
			wantOk:   false,
		},
		{
			name:      "invalid local name (no slash)",
			localName: "invalid",
			entry: LockEntry{
				SHA: "abc123",
				URL: "https://github.com/alice/ctxloom",
			},
			itemType: ItemTypeBundle,
			wantURL:  "",
			wantOk:   false,
		},
		{
			name:      "canonical key",
			localName: "https://github.com/alice/ctxloom@bundles/core-practices",
			entry: LockEntry{
				SHA: "abc123",
				URL: "https://github.com/alice/ctxloom",
			},
			itemType: ItemTypeBundle,
			wantURL:  "https://github.com/alice/ctxloom@bundles/core-practices@abc123",
			wantOk:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockfile := &Lockfile{
				Bundles: make(map[string]LockEntry),
			}

			if tt.entry.SHA != "" || tt.entry.URL != "" {
				lockfile.AddEntry(tt.itemType, tt.localName, tt.entry)
			}

			gotURL, gotOk, err := lockfile.GetCanonicalURL(tt.itemType, tt.localName)
			if err != nil {
				t.Fatalf("GetCanonicalURL() unexpected error: %v", err)
			}
			if gotOk != tt.wantOk {
				t.Errorf("GetCanonicalURL() ok = %v, want %v", gotOk, tt.wantOk)
			}
			if gotURL != tt.wantURL {
				t.Errorf("GetCanonicalURL() = %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

// Lockfile keys are canonical refs ("<url>@bundles/<path>"); publish resolves
// a profile's short bundle name against them by full path or last segment,
// erroring rather than guessing when the name matches more than one entry.
func TestLockfile_GetCanonicalURL_ShortNameResolution(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"https://github.com/alice/ctxloom@bundles/core-practices": {SHA: "abc123", URL: "https://github.com/alice/ctxloom"},
			"https://github.com/alice/ctxloom@bundles/lang/go":        {SHA: "def456", URL: "https://github.com/alice/ctxloom"},
			"https://github.com/bob/ctxloom@bundles/tools/go":         {SHA: "fed654", URL: "https://github.com/bob/ctxloom"},
		},
	}

	t.Run("short name resolves against canonical key", func(t *testing.T) {
		got, ok, err := lockfile.GetCanonicalURL(ItemTypeBundle, "core-practices")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected the short name to resolve")
		}
		want := "https://github.com/alice/ctxloom@bundles/core-practices@abc123"
		if got != want {
			t.Errorf("GetCanonicalURL() = %q, want %q", got, want)
		}
	})

	t.Run("nested full path resolves uniquely", func(t *testing.T) {
		got, ok, err := lockfile.GetCanonicalURL(ItemTypeBundle, "lang/go")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected the full path to resolve")
		}
		want := "https://github.com/alice/ctxloom@bundles/lang/go@def456"
		if got != want {
			t.Errorf("GetCanonicalURL() = %q, want %q", got, want)
		}
	})

	t.Run("ambiguous last segment errors with candidates", func(t *testing.T) {
		_, ok, err := lockfile.GetCanonicalURL(ItemTypeBundle, "go")
		if ok {
			t.Fatal("expected ok = false for an ambiguous name")
		}
		if err == nil {
			t.Fatal("expected an ambiguity error")
		}
		for _, candidate := range []string{
			"https://github.com/alice/ctxloom@bundles/lang/go",
			"https://github.com/bob/ctxloom@bundles/tools/go",
		} {
			if !strings.Contains(err.Error(), candidate) {
				t.Errorf("error %q should list candidate %q", err, candidate)
			}
		}
	})

	t.Run("unknown short name is not found", func(t *testing.T) {
		_, ok, err := lockfile.GetCanonicalURL(ItemTypeBundle, "missing")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("expected ok = false")
		}
	})
}

func TestLockfile_FindByURL(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"ctxloom-github/core": {SHA: "abc123", URL: "https://github.com/alice/ctxloom"},
		},
	}

	t.Run("find bundle by URL", func(t *testing.T) {
		name, entry, found := lockfile.FindByURL("https://github.com/alice/ctxloom", ItemTypeBundle)
		if !found {
			t.Fatal("expected to find entry")
		}
		if name != "ctxloom-github/core" {
			t.Errorf("name = %q, want %q", name, "ctxloom-github/core")
		}
		if entry.SHA != "abc123" {
			t.Errorf("SHA = %q, want %q", entry.SHA, "abc123")
		}
	})

	t.Run("URL not found", func(t *testing.T) {
		_, _, found := lockfile.FindByURL("https://github.com/nonexistent/repo", ItemTypeBundle)
		if found {
			t.Error("expected entry not to be found")
		}
	})

	t.Run("unknown item type", func(t *testing.T) {
		_, _, found := lockfile.FindByURL("https://github.com/alice/ctxloom", ItemType("unknown"))
		if found {
			t.Error("expected entry not to be found for unknown type")
		}
	})
}

func TestLockfile_FindAllByURL(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{
			"ctxloom-github/core":  {SHA: "abc123", URL: "https://github.com/alice/ctxloom"},
			"ctxloom-github/tools": {SHA: "def456", URL: "https://github.com/alice/ctxloom"},
		},
	}

	t.Run("finds all matching entries", func(t *testing.T) {
		results := lockfile.FindAllByURL("https://github.com/alice/ctxloom")
		if len(results) != 2 {
			t.Errorf("len(results) = %d, want 2", len(results))
		}
	})

	t.Run("returns empty for no matches", func(t *testing.T) {
		results := lockfile.FindAllByURL("https://github.com/nonexistent/repo")
		if len(results) != 0 {
			t.Errorf("len(results) = %d, want 0", len(results))
		}
	})
}

func TestLockfileManager_Load_InvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/test", 0755)
	_ = afero.WriteFile(fs, "/test/"+paths.LockFileName+".yaml", []byte("invalid: ["), 0644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLockfileManager_Load_NilMaps(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Write a lockfile without a bundles map
	content := "version: 1\n"
	_ = fs.MkdirAll("/test", 0755)
	_ = afero.WriteFile(fs, "/test/"+paths.LockFileName+".yaml", []byte(content), 0644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	lockfile, err := manager.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Maps should be initialized
	if lockfile.Bundles == nil {
		t.Error("Bundles map should be initialized")
	}
}

func TestLockfileManager_Load_ReadError(t *testing.T) {
	// Create a scenario where the file exists but cannot be read
	// Use a read-only filesystem with a file that exists
	baseFs := afero.NewMemMapFs()
	_ = baseFs.MkdirAll("/test", 0755)
	_ = afero.WriteFile(baseFs, "/test/"+paths.LockFileName+".yaml", []byte("version: 1\n"), 0000)
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
		Version: 1,
		Bundles: make(map[string]LockEntry),
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
		Bundles: make(map[string]LockEntry),
	}

	// Unknown item type should not find any entry
	_, ok := lockfile.GetEntry(ItemType("unknown"), "test/bundle")
	if ok {
		t.Error("expected entry not to be found for unknown type")
	}
}

func TestLockfile_AddEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[string]LockEntry),
	}

	// Unknown type should not add to any map
	lockfile.AddEntry(ItemType("unknown"), "test/bundle", LockEntry{SHA: "abc123"})

	if len(lockfile.Bundles) != 0 {
		t.Error("unknown type should not add to bundles")
	}
}

func TestLockfile_RemoveEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[string]LockEntry{"test/bundle": {SHA: "abc123"}},
	}

	// Unknown type should not remove from any map
	lockfile.RemoveEntry(ItemType("unknown"), "test/bundle")

	if len(lockfile.Bundles) != 1 {
		t.Error("unknown type should not remove from bundles")
	}
}
