package remote

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// A hold is spelled `held` on disk, because that is what the CLI calls it.
//
// The field behind `ctxloom deps hold` was named Pinned, serialized `pinned`,
// and read back by a DTO layer that already called it Held — three names for
// one idea, with only the middle one visible to anyone opening the file.

func TestLockfile_SerializesAHoldAsHeld(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	lf := &Lockfile{Version: 1, Bundles: map[string]LockEntry{
		"alice/go-tools": {SHA: "abc1234", URL: "https://github.com/alice/ctxloom", Held: true},
	}}
	if err := manager.Save(lf); err != nil {
		t.Fatalf("save: %v", err)
	}

	onDisk, err := afero.ReadFile(fs, manager.Path())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(onDisk), "held: true") {
		t.Errorf("a hold must serialize as `held: true`:\n%s", onDisk)
	}
	if strings.Contains(string(onDisk), "pinned:") {
		t.Errorf("the retired `pinned:` key must not be written:\n%s", onDisk)
	}
}

func TestLockfile_RoundTripsAHold(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	lf := &Lockfile{Version: 1, Bundles: map[string]LockEntry{
		"alice/go-tools": {SHA: "abc1234", URL: "https://github.com/alice/ctxloom", Held: true},
	}}
	if err := manager.Save(lf); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry, ok := loaded.GetEntry(ItemTypeBundle, "alice/go-tools")
	if !ok {
		t.Fatal("entry not found")
	}
	if !entry.Held {
		t.Error("a hold written to disk must come back as a hold")
	}
}

// The retired key REFUSES rather than being ignored.
//
// yaml silently drops a key the struct does not model, so a lockfile written
// by an older ctxloom would load cleanly with every hold gone — `deps upgrade`
// would then advance a dependency the user deliberately froze, reporting
// success. Refusing names the fix instead.
func TestLockfile_RefusesTheRetiredPinnedKey(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	legacy := "version: 1\n" +
		"bundles:\n" +
		"  alice/go-tools:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/ctxloom\n" +
		"    pinned: true\n"
	if err := afero.WriteFile(fs, manager.Path(), []byte(legacy), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := manager.Load()
	if err == nil {
		t.Fatal("a lockfile carrying the retired `pinned:` key must be refused, not silently read with the hold dropped")
	}
	if !strings.Contains(err.Error(), "held") {
		t.Errorf("the refusal must name the current spelling; got: %v", err)
	}
}

// The refusal fires on the KEY, not on the characters — the same distinction
// the retired-schema-field check draws, and for the same reason: a repository
// URL, a bundle path or a retraction reason may contain the word without any
// such key existing.
func TestLockfile_LoadsWhenPinnedIsMerelyMentioned(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	mention := "version: 1\n" +
		"bundles:\n" +
		"  alice/pinned-tools:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/pinned\n" +
		"    retracted_reason: the author pinned the wrong commit\n"
	if err := afero.WriteFile(fs, manager.Path(), []byte(mention), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("a mere mention must not be read as the retired key: %v", err)
	}
	if _, ok := loaded.GetEntry(ItemTypeBundle, "alice/pinned-tools"); !ok {
		t.Error("entry not found")
	}
}
