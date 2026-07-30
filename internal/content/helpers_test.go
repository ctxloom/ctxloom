package content

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spf13/afero"
)

const fixtureRoot = "/store"

// fixtureStore copies testdata/tree into an in-memory filesystem and opens a
// store over it. The copy is deliberate: tests that mutate the tree (to prove a
// metadata edit changes a digest, say) must not touch the committed fixture.
func fixtureStore(t *testing.T) *TreeStore {
	t.Helper()
	fsys := afero.NewMemMapFs()
	copyTreeInto(t, fsys, filepath.Join("testdata", "tree"), fixtureRoot)
	store, err := NewTreeStore(fsys, fixtureRoot, Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("NewTreeStore: %v", err)
	}
	return store
}

// emptyStore opens a store over an empty in-memory tree.
func emptyStore(t *testing.T) *TreeStore {
	t.Helper()
	fsys := afero.NewMemMapFs()
	if err := fsys.MkdirAll(fixtureRoot+"/code-quality", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, err := NewTreeStore(fsys, fixtureRoot, Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("NewTreeStore: %v", err)
	}
	return store
}

func copyTreeInto(t *testing.T, dst afero.Fs, src, root string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		if d.IsDir() {
			return dst.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return afero.WriteFile(dst, target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
}

// writeFile is a terse in-test writer.
func writeFile(t *testing.T, fsys afero.Fs, path, body string) {
	t.Helper()
	if err := fsys.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
	if err := afero.WriteFile(fsys, path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// componentPaths reduces components to their paths, for readable assertions.
func componentPaths(cs []Component) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

// newMemFsWithRoot returns an empty in-memory filesystem with the store root
// created.
func newMemFsWithRoot(t *testing.T) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	if err := fsys.MkdirAll(fixtureRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return fsys
}

// reverseOrderFixtureStore populates a store with the same fixture contents but
// writes the files in reverse path order, so a digest that depended on creation
// or directory-read order would disagree with fixtureStore's.
func reverseOrderFixtureStore(t *testing.T) *TreeStore {
	t.Helper()
	src := filepath.Join("testdata", "tree")
	var paths []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking fixture: %v", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	fsys := afero.NewMemMapFs()
	for _, p := range paths {
		rel, err := filepath.Rel(src, p)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		writeFile(t, fsys, filepath.Join(fixtureRoot, rel), string(data))
	}
	store, err := NewTreeStore(fsys, fixtureRoot, Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("NewTreeStore: %v", err)
	}
	return store
}
