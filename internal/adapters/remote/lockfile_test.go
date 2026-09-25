package remote

import (
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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
		Bundles: make(map[trust.BundleKey]LockEntry),
	}

	now := time.Now().UTC().Truncate(time.Second)
	lockfile.AddEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools", LockEntry{
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

	if loaded.Version != LockfileVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, LockfileVersion)
	}

	entry, ok := loaded.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools")
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

	legacy := "version: 2\n" +
		"bundles:\n" +
		"  ctxloom+git://github.com/alice/ctxloom//bundles/go-tools:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/ctxloom\n" +
		"    ctxloom_version: v1\n"
	testsupport.WriteFileString(t, fs, path, legacy, 0o644)

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry, ok := loaded.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools")
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

// TestLockfileManager_LoadDoesNotRewriteOnAMereMention pins that the load-time
// self-heal must fire on the retired ctxloom_version KEY,
// not on the characters appearing anywhere in the document.
//
// A read that writes is already a strong thing to do; triggering it off a raw
// substring meant a repository URL, a bundle path or a retraction reason that
// merely mentions the words rewrote the whole file through the struct —
// discarding comments and any key the struct does not model.
func TestLockfileManager_LoadDoesNotRewriteOnAMereMention(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	path := manager.Path()

	// "ctxloom_version" appears twice — in a bundle key and in free text — but
	// never as an entry field.
	original := "# hand-maintained; do not reformat\n" +
		"version: 2\n" +
		"bundles:\n" +
		"  ctxloom+git://github.com/alice/repo//bundles/docs/ctxloom_version:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/repo\n" +
		"    retracted_reason: the ctxloom_version field was dropped\n"
	testsupport.WriteFileString(t, fs, path, original, 0644)

	loaded, err := manager.Load()
	require.NoError(t, err)
	_, ok := loaded.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/repo//bundles/docs/ctxloom_version")
	require.True(t, ok, "the entry must still load")

	onDisk, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, original, string(onDisk), "a read that finds no legacy field must not write")
}

// TestLockfileManager_SaveStampsTheCurrentVersion: Save writes
// LockfileVersion whatever the caller constructed, because Load refuses any
// older version (ErrLockKeyFormRetired) — a Save that left "version: 0" on
// disk would write a lockfile the next Load cannot read.
func TestLockfileManager_SaveStampsTheCurrentVersion(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	require.NoError(t, manager.Save(&Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{"ctxloom+git://github.com/a/r//bundles/x": {SHA: "abc1234"}},
	}))

	onDisk, err := afero.ReadFile(fs, manager.Path())
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "version: 2")

	reloaded, err := manager.Load()
	require.NoError(t, err)
	assert.Equal(t, LockfileVersion, reloaded.Version, "and it round-trips at the current version")
}

func TestLockfile_AddEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[trust.BundleKey]LockEntry),
	}

	entry := LockEntry{SHA: "abc123"}

	lockfile.AddEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools", entry)

	if len(lockfile.Bundles) != 1 {
		t.Errorf("Bundles count = %d, want 1", len(lockfile.Bundles))
	}
}

func TestLockfile_GetEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{
			"ctxloom+git://github.com/alice/ctxloom//bundles/go-tools": {SHA: "abc123"},
		},
	}

	// Existing entry
	entry, ok := lockfile.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if entry.SHA != "abc123" {
		t.Errorf("SHA = %q, want %q", entry.SHA, "abc123")
	}

	// Non-existing entry
	_, ok = lockfile.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/bob/ctxloom//bundles/missing")
	if ok {
		t.Error("expected entry to not exist")
	}
}

func TestLockfile_RemoveEntry(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{
			"ctxloom+git://github.com/alice/ctxloom//bundles/go-tools": {SHA: "abc123"},
			"ctxloom+git://github.com/bob/ctxloom//bundles/testing":    {SHA: "def456"},
		},
	}

	lockfile.RemoveEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools")

	if len(lockfile.Bundles) != 1 {
		t.Errorf("Bundles count = %d, want 1", len(lockfile.Bundles))
	}
	if _, ok := lockfile.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools"); ok {
		t.Error("entry should have been removed")
	}
	if _, ok := lockfile.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/bob/ctxloom//bundles/testing"); !ok {
		t.Error("other entry should still exist")
	}
}

func TestLockfile_AllEntries(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{
			"ctxloom+git://github.com/alice/ctxloom//bundles/go-tools": {SHA: "abc123"},
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
				Bundles: make(map[trust.BundleKey]LockEntry),
			},
			want: true,
		},
		{
			name: "with bundle",
			lockfile: Lockfile{
				Bundles: map[trust.BundleKey]LockEntry{"a": {}},
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
		Bundles: map[trust.BundleKey]LockEntry{"a": {}, "b": {}},
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
		Bundles: make(map[trust.BundleKey]LockEntry),
	}
	lockfile.AddEntry(ItemTypeBundle, "ctxloom+git://example.test/r//bundles/bundle", LockEntry{SHA: "abc123"})

	if err := manager.Save(lockfile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file was written to memfs
	exists, err := afero.Exists(fs, manager.Path())
	if err != nil || !exists {
		t.Error("lockfile should exist in memory fs")
	}
}

// Lockfile keys are canonical refs ("<url>@bundles/<path>"); publish resolves
// a profile's short bundle name against them by full path or last segment,
// erroring rather than guessing when the name matches more than one entry.
func TestLockfileManager_Load_InvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/test/"+paths.LockFileName+".yaml", "invalid: [", 0o644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLockfileManager_Load_NilMaps(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Write a lockfile without a bundles map
	content := "version: 2\n"
	testsupport.WriteFileString(t, fs, "/test/"+paths.LockFileName+".yaml", content, 0o644)

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

// A PRESENT-but-EMPTY (or whitespace/comment-only) lock.yaml must
// not load as a valid, legitimately-empty lockfile — every path that writes a
// lockfile (Save/write) always marshals at least "version: 2\nbundles: {}\n"
// plus a timestamp, so a genuinely 0-byte file on disk can only mean
// truncation, a crash mid-write, or a hand-created stub — never a real
// "nothing pinned yet" project (that case is instead ordinary IsNotExist,
// handled separately and unaffected by this). Silently treating it as valid
// means every remote bundle just vanishes from the session with no
// diagnostic at all.
func TestLockfileManager_Load_PresentButEmptyFileIsRefused(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/test/"+paths.LockFileName+".yaml", "", 0o644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	if err == nil {
		t.Error("a present-but-0-byte lockfile must be refused, not silently loaded as an empty lockfile")
	}
}

func TestLockfileManager_Load_WhitespaceOnlyFileIsRefused(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/test/"+paths.LockFileName+".yaml", "   \n\n", 0o644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	_, err := manager.Load()
	if err == nil {
		t.Error("a present-but-whitespace-only lockfile must be refused, not silently loaded as an empty lockfile")
	}
}

func TestLockfileManager_Load_ReadError(t *testing.T) {
	// Create a scenario where the file exists but cannot be read
	// Use a read-only filesystem with a file that exists
	baseFs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, baseFs, "/test/"+paths.LockFileName+".yaml", "version: 2\n", 0o000)
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
		Bundles: make(map[trust.BundleKey]LockEntry),
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
		Bundles: make(map[trust.BundleKey]LockEntry),
	}

	// Unknown item type should not find any entry
	_, ok := lockfile.GetEntry(ItemType("unknown"), "ctxloom+git://example.test/r//bundles/bundle")
	if ok {
		t.Error("expected entry not to be found for unknown type")
	}
}

// TestLockfile_OnlyBundlesAreDistributed pins the property that makes
// AddEntry's non-bundle discard unreachable — it used to read as a live
// silent no-op.
//
// AddEntry does drop an entry whose itemType is not ItemTypeBundle, and it has
// no return value with which to say so. But ItemTypeBundle is the ONLY ItemType
// constant, no production code converts a string to an ItemType, and the single
// parser that derives one from a ref — parseTypePathVersion, behind
// ParseReference — accepts the literal segment "bundles" and rejects every
// other spelling. So no caller can reach the discard except by fabricating a
// type, which only this package's own tests do.
//
// The day a second distributed item type is introduced, this goes red — and
// that is exactly the day AddEntry's silent drop becomes reachable and must
// grow a way to report it.
func TestLockfile_OnlyBundlesAreDistributed(t *testing.T) {
	ref, err := ParseReference("https://github.com/alice/repo@bundles/core")
	require.NoError(t, err)
	assert.Equal(t, ItemTypeBundle, ref.ItemType)

	for _, seg := range []string{"profiles", "fragments", "commands", "skills", "mcp", "hooks", "widgets"} {
		_, err := ParseReference("https://github.com/alice/repo@" + seg + "/core")
		require.Error(t, err, "segment %q must not name a distributed item type", seg)
		assert.ErrorContains(t, err, "unknown item type")
	}
}

func TestLockfile_AddEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: make(map[trust.BundleKey]LockEntry),
	}

	// Unknown type should not add to any map
	lockfile.AddEntry(ItemType("unknown"), "ctxloom+git://example.test/r//bundles/bundle", LockEntry{SHA: "abc123"})

	if len(lockfile.Bundles) != 0 {
		t.Error("unknown type should not add to bundles")
	}
}

func TestLockfile_RemoveEntry_UnknownType(t *testing.T) {
	lockfile := &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{"ctxloom+git://example.test/r//bundles/bundle": {SHA: "abc123"}},
	}

	// Unknown type should not remove from any map
	lockfile.RemoveEntry(ItemType("unknown"), "ctxloom+git://example.test/r//bundles/bundle")

	if len(lockfile.Bundles) != 1 {
		t.Error("unknown type should not remove from bundles")
	}
}

// TestLockfileManager_Load_IgnoresTheRetiredTreeField pins the on-disk
// contract for a lockfile written before the per-pin `tree` shape flag was
// retired: the key is IGNORED, not refused, and the entry it sat on loads
// intact. Unlike the retired hold key (which carries a user decision and is
// refused by name), `tree` recorded a fact every v2 pin now has by
// construction, so dropping it silently loses nothing.
func TestLockfileManager_Load_IgnoresTheRetiredTreeField(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/test/"+paths.LockFileName+".yaml", `version: 2
locked_at: 2026-01-01T00:00:00Z
bundles:
  ctxloom+git://github.com/acme/tools//bundles/tools:
    sha: abc123
    url: https://github.com/acme/tools
    tree: true
    held: true
`, 0o644)

	manager := NewLockfileManager("/test", WithLockfileFS(fs))
	lock, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() with a retired tree key: %v — an old lockfile must still parse", err)
	}
	entry, ok := lock.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/acme/tools//bundles/tools")
	if !ok {
		t.Fatal("the entry carrying the retired key was dropped rather than loaded")
	}
	if entry.SHA != "abc123" || !entry.Held {
		t.Errorf("entry = %+v; the fields beside the retired key must survive the load", entry)
	}
}
