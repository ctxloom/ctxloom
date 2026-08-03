package content

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/trust"
)

func TestAferoTreeFS_ListsAndReadsStoreRelativeSlashPaths(t *testing.T) {
	fsys := afero.NewMemMapFs()
	writeFile(t, fsys, filepath.Join(fixtureRoot, "b", "mcp", "redis.yaml"), "command: redis\n")
	writeFile(t, fsys, filepath.Join(fixtureRoot, "b", "mcp", ".redis.meta.yaml"), "tags: []\n")

	tfs, err := NewAferoTreeFS(fsys, fixtureRoot)
	if err != nil {
		t.Fatalf("NewAferoTreeFS: %v", err)
	}
	entries, err := tfs.ReadDir("b/mcp")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if got := entryNames(entries); got != ".redis.meta.yaml,redis.yaml" {
		t.Fatalf("ReadDir = %q, want the dot-prefixed sidecar listed alongside the content file", got)
	}
	data, err := tfs.ReadFile("b/mcp/redis.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "command: redis\n" {
		t.Fatalf("ReadFile = %q", data)
	}
}

func TestAferoTreeFS_ReportsDirectoriesAsDirectories(t *testing.T) {
	fsys := afero.NewMemMapFs()
	writeFile(t, fsys, filepath.Join(fixtureRoot, "b", "skills", "x", "SKILL.md"), "---\nname: x\n---\n")
	tfs, err := NewAferoTreeFS(fsys, fixtureRoot)
	if err != nil {
		t.Fatalf("NewAferoTreeFS: %v", err)
	}
	entries, err := tfs.ReadDir("b/skills")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "x" || !entries[0].IsDir {
		t.Fatalf("ReadDir = %+v, want one directory entry named x", entries)
	}
}

// TestTreeFS_MissingDirectoryIsErrNotExist pins the contract the walker depends
// on: a kind directory that simply is not there means "no items of that kind",
// and the walker distinguishes that from a real read failure by errors.Is. A
// backend that returned a bare error instead would turn every bundle without a
// hooks/ directory into a hard failure.
func TestTreeFS_MissingDirectoryIsErrNotExist(t *testing.T) {
	fsys := afero.NewMemMapFs()
	writeFile(t, fsys, filepath.Join(fixtureRoot, "b", "mcp", "redis.yaml"), "command: redis\n")
	afs, err := NewAferoTreeFS(fsys, fixtureRoot)
	if err != nil {
		t.Fatalf("NewAferoTreeFS: %v", err)
	}
	mfs, err := NewMapTreeFS(map[string][]byte{"b/mcp/redis.yaml": []byte("command: redis\n")})
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	for name, tfs := range map[string]TreeFS{"afero": afs, "map": mfs} {
		t.Run(name, func(t *testing.T) {
			if _, err := tfs.ReadDir("b/hooks"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("ReadDir of a missing directory = %v, want fs.ErrNotExist", err)
			}
			if _, err := tfs.ReadFile("b/hooks/none.yaml"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("ReadFile of a missing file = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

func TestMapTreeFS_SynthesizesDirectoriesFromPaths(t *testing.T) {
	tfs, err := NewMapTreeFS(map[string][]byte{
		"b/skills/x/SKILL.md":             []byte("s"),
		"b/skills/x/scripts/run.sh":       []byte("r"),
		"b/skills/.x.meta.yaml":           []byte("m"),
		"other/fragments/a.md":            []byte("a"),
		"b/hooks/pre_tool/guard.yaml":     []byte("g"),
		"b/hooks/session_start/hi.yaml":   []byte("h"),
		"b/hooks/pre_tool/.guard.meta.yl": []byte("gm"),
	})
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	for dir, want := range map[string]string{
		".":          "b,other",
		"b":          "hooks,skills",
		"b/skills":   ".x.meta.yaml,x",
		"b/skills/x": "SKILL.md,scripts",
		"b/hooks":    "pre_tool,session_start",
	} {
		entries, err := tfs.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir %q: %v", dir, err)
		}
		if got := entryNames(entries); got != want {
			t.Errorf("ReadDir %q = %q, want %q", dir, got, want)
		}
	}
	entries, err := tfs.ReadDir("b/skills")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name == "x" && !e.IsDir {
			t.Errorf("synthesized entry x is not reported as a directory")
		}
		if e.Name == ".x.meta.yaml" && e.IsDir {
			t.Errorf("file .x.meta.yaml is reported as a directory")
		}
	}
}

// TestMapTreeFS_RefusesMalformedPaths: a bytes-only backend receives its paths
// from somewhere else — a remote tree listing, an archive index — so they are
// exactly as trustworthy as the publisher. Refusing at construction is what
// keeps "the walker cannot be walked out of its root" a property of the seam
// rather than a check every backend has to remember.
func TestMapTreeFS_RefusesMalformedPaths(t *testing.T) {
	for name, path := range map[string]string{
		"empty":          "",
		"absolute":       "/b/x.md",
		"parent escape":  "b/../../x.md",
		"dot segment":    "b/./x.md",
		"trailing slash": "b/x/",
		"backslash":      `b\x.md`,
	} {
		if _, err := NewMapTreeFS(map[string][]byte{path: []byte("x")}); err == nil {
			t.Errorf("%s: NewMapTreeFS accepted path %q", name, path)
		}
	}
}

// TestMapTreeFS_RefusesAPathThatIsBothFileAndDirectory: a real filesystem cannot
// represent this, so accepting it would make the map backend answer questions
// the afero backend cannot — the exact divergence a shared traversal exists to
// prevent.
func TestMapTreeFS_RefusesAPathThatIsBothFileAndDirectory(t *testing.T) {
	_, err := NewMapTreeFS(map[string][]byte{
		"b/mcp":       []byte("i am a file"),
		"b/mcp/x.yml": []byte("i am under it"),
	})
	if err == nil {
		t.Fatal("NewMapTreeFS accepted a path that is both a file and a directory")
	}
	if !strings.Contains(err.Error(), "b/mcp") {
		t.Fatalf("error %q does not name the conflicting path", err)
	}
}

// TestFSStore_BytesOnlyBackendAnswersIdenticallyToAfero is the whole point of
// the seam.
//
// The SAME fixture is served two ways — through an afero.Fs and through a
// bytes-only map that never touched a filesystem — and the full walker answer
// (bundles, files, refs, forms, components, digests) is compared against the
// SAME golden. If the seam had not been extracted, the bytes-only side would
// need a second walker, and this test is what would catch the two drifting.
func TestFSStore_BytesOnlyBackendAnswersIdenticallyToAfero(t *testing.T) {
	files := map[string][]byte{}
	src := filepath.Join("testdata", "tree")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	tfs, err := NewMapTreeFS(files)
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	store, err := NewFSStore(tfs, Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	compareGolden(t, goldenTreeWalk, snapshotStore(t, store))
}

// TestFSStore_IsNotAWriter: an FSStore is read-only by construction. If it
// satisfied Writer, a Put would appear to succeed while writing into bytes
// nothing reads back — a silent no-op with a success return.
func TestFSStore_IsNotAWriter(t *testing.T) {
	tfs, err := NewMapTreeFS(map[string][]byte{"b/mcp/redis.yaml": []byte("command: redis\n")})
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	store, err := NewFSStore(tfs, Provenance{RepoURL: "https://example.test/x"})
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	if _, ok := any(store).(Writer); ok {
		t.Fatal("FSStore satisfies Writer; a read-only store must not")
	}
}

// TestTreeStore_ReadOnlyBackingRefusesWritesLoudly guards the one path that
// could still nil-panic or silently no-op: the TreeStore underneath an FSStore
// has no writable filesystem, so every Writer method must refuse in words.
func TestTreeStore_ReadOnlyBackingRefusesWritesLoudly(t *testing.T) {
	tfs, err := NewMapTreeFS(map[string][]byte{"b/mcp/redis.yaml": []byte("command: redis\n")})
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	store, err := newReadOnlyTreeStore(tfs, Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("newReadOnlyTreeStore: %v", err)
	}
	ctx := t.Context()
	ref := trust.Ref{Bundle: "b", Kind: trust.KindMCP, Name: "redis"}
	for name, call := range map[string]func() error{
		"Put":                func() error { return store.Put(ctx, ref, "raw", nil) },
		"Delete":             func() error { return store.Delete(ctx, ref) },
		"PutSignature":       func() error { return store.PutSignature(ctx, ref, "raw", "publish", []byte("s")) },
		"PutManifest":        func() error { return store.PutManifest(ctx, "b", Manifest{}) },
		"PutBundleSignature": func() error { return store.PutBundleSignature(ctx, "b", "publish", []byte("s")) },
	} {
		if err := call(); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s on a read-only store = %v, want ErrReadOnly", name, err)
		}
	}
}

func entryNames(entries []TreeEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return strings.Join(names, ",")
}
