package remote

import (
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
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

	lf := &Lockfile{Version: 1, Bundles: map[ident.BundleKey]LockEntry{
		"ctxloom+git://github.com/alice/ctxloom//bundles/go-tools": {SHA: "abc1234", URL: "https://github.com/alice/ctxloom", Held: true},
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

	lf := &Lockfile{Version: 1, Bundles: map[ident.BundleKey]LockEntry{
		"ctxloom+git://github.com/alice/ctxloom//bundles/go-tools": {SHA: "abc1234", URL: "https://github.com/alice/ctxloom", Held: true},
	}}
	if err := manager.Save(lf); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry, ok := loaded.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/go-tools")
	if !ok {
		t.Fatal("entry not found")
	}
	if !entry.Held {
		t.Error("a hold written to disk must come back as a hold")
	}
}

// The strict decode judges KEYS, not characters: a repository URL, a bundle
// path or a requested version may contain a word that is not a field.
func TestLockfile_LoadsWhenPinnedIsMerelyMentioned(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	mention := "schema_version: 2\n" +
		"bundles:\n" +
		"  ctxloom+git://github.com/alice/ctxloom//bundles/pinned-tools:\n" +
		"    sha: abc1234\n" +
		"    url: https://github.com/alice/pinned\n" +
		"    requested_version: pinned-release\n"
	testsupport.WriteFileString(t, fs, manager.Path(), mention, 0o644)

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("a mere mention must not be read as a field: %v", err)
	}
	if _, ok := loaded.GetEntry(ItemTypeBundle, "ctxloom+git://github.com/alice/ctxloom//bundles/pinned-tools"); !ok {
		t.Error("entry not found")
	}
}
