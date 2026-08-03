package content

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
)

// TreeFS is the storage seam beneath the tree walker: LIST a directory, READ a
// file, and nothing else.
//
// # Why it exists
//
// The walker — grouping by stem, merging a same-named subdirectory into its
// group, offering an unclaimed directory to the type whole and descending when
// it declines, computing the unclaimed set by subtraction — is the part of this
// package that is subtle enough to be worth having exactly once. It was written
// against afero.Fs, which excluded the backend the design exists to span: a
// remote git tree at a pinned SHA is BYTES, reachable by path, and is not a
// filesystem. The choice was a second walker or a smaller seam; this is the
// smaller seam. Every recursive traversal in this package is now derived from
// these two methods, so a backend implements two functions and inherits the
// whole traversal, including the fail-loud unclaimed-file rule.
//
// # Path convention
//
// Paths are STORE-relative, slash-separated, and never absolute: "." is the
// store root, "code-quality/fragments/solid.md" is a file two levels down. This
// is the same vocabulary Source.Open and Component.Path already use, so nothing
// in the walker has to translate between two path dialects — the one place a
// backslash or a "\.\." could sneak past a check.
//
// # Contract
//
// Neither method takes a context, matching Source, whose implementations are
// already the read path for every backend. A backend that needs cancellation
// carries it in its own construction, where the bytes are fetched.
type TreeFS interface {
	// ReadDir lists the immediate entries of dir. Order is not significant —
	// the walker sorts everything it depends on — but a backend that returns a
	// stable order makes its own failures easier to read.
	//
	// A directory that does not exist MUST report an error satisfying
	// errors.Is(err, fs.ErrNotExist). The walker reads that as "no items of
	// this kind", which is the normal case for every bundle without a hooks/
	// directory; any other error is a real read failure and fails the
	// enumeration.
	ReadDir(dir string) ([]TreeEntry, error)

	// ReadFile returns one file's exact stored bytes. A missing file MUST
	// likewise report fs.ErrNotExist, which the bundle turns into ErrNotFound.
	ReadFile(name string) ([]byte, error)
}

// TreeEntry is one directory entry: a name and whether it is a directory.
//
// It is deliberately NOT fs.DirEntry. The walker uses exactly two facts about
// an entry, and fs.DirEntry would oblige every backend to synthesise an
// fs.FileInfo — a size, a mode and a modification time — that nothing here
// reads and that a remote tree listing does not have. Modes in this package are
// DECLARED metadata read from signed bytes, never filesystem bits (see
// ComponentMode), so a seam that carried a mode would be offering the walker a
// fact it must not use.
type TreeEntry struct {
	// Name is the entry's own name, with no directory part.
	Name string
	// IsDir reports whether the entry can be listed.
	IsDir bool
}

// ErrReadOnly reports a write attempted against a store with no writable
// backing — a builtin, an archive, a pinned remote. It is an ERROR rather than
// a no-op on purpose: a Put that returned nil while writing nothing is this
// codebase's characteristic failure.
var ErrReadOnly = errors.New("content: store is read-only")

// AferoTreeFS serves a TreeFS from a subtree of an afero.Fs. It is the authored
// (and, through afero.FromIOFS, the embedded and archive) backend's adapter,
// and the only place in this package that translates between store-relative
// slash paths and native filesystem paths.
type AferoTreeFS struct {
	fsys afero.Fs
	root string
}

var _ TreeFS = (*AferoTreeFS)(nil)

// NewAferoTreeFS adapts the subtree of fsys rooted at root.
func NewAferoTreeFS(fsys afero.Fs, root string) (*AferoTreeFS, error) {
	if fsys == nil {
		return nil, errors.New("content: nil filesystem")
	}
	if root == "" {
		return nil, errors.New("content: empty store root")
	}
	return &AferoTreeFS{fsys: fsys, root: root}, nil
}

// osPath resolves a store-relative slash path to a native path under the root.
func (a *AferoTreeFS) osPath(rel string) string {
	return filepath.Join(a.root, filepath.FromSlash(rel))
}

func (a *AferoTreeFS) ReadDir(dir string) ([]TreeEntry, error) {
	if err := validateTreePath(dir); err != nil {
		return nil, err
	}
	infos, err := afero.ReadDir(a.fsys, a.osPath(dir))
	if err != nil {
		// Passed through unwrapped: afero's own errors already satisfy
		// errors.Is(err, fs.ErrNotExist) for both the OS and the in-memory
		// backend, and re-wrapping with a message would only hide which path
		// the underlying filesystem actually rejected.
		return nil, err
	}
	out := make([]TreeEntry, 0, len(infos))
	for _, info := range infos {
		out = append(out, TreeEntry{Name: info.Name(), IsDir: info.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (a *AferoTreeFS) ReadFile(name string) ([]byte, error) {
	if err := validateTreePath(name); err != nil {
		return nil, err
	}
	return afero.ReadFile(a.fsys, a.osPath(name))
}

// MapTreeFS serves a TreeFS from an in-memory path-to-bytes map, with the
// directory structure SYNTHESISED from the paths.
//
// This is the bytes-only shape: a remote tree listing at a pinned SHA, an
// archive index, a test fixture. Nothing about it touches a filesystem, and the
// walker cannot tell the difference — which is the property the seam exists to
// have.
type MapTreeFS struct {
	files map[string][]byte
	dirs  map[string][]TreeEntry
}

var _ TreeFS = (*MapTreeFS)(nil)

// NewMapTreeFS builds a TreeFS over files, keyed by store-relative slash path.
//
// Every path is validated HERE rather than at each read. A bytes-only backend
// receives its paths from wherever the bytes came from — a remote listing is
// exactly as trustworthy as its publisher — so refusing a traversal, an
// absolute path or a name that is both a file and a directory at construction
// makes "the walker stays inside its root" a property of the seam instead of a
// check each backend has to remember. The both-file-and-directory case matters
// specifically because no real filesystem can represent it: accepting it would
// let this backend answer questions the afero one cannot, which is the exact
// divergence a shared traversal exists to prevent.
func NewMapTreeFS(files map[string][]byte) (*MapTreeFS, error) {
	m := &MapTreeFS{
		files: make(map[string][]byte, len(files)),
		dirs:  map[string][]TreeEntry{".": nil},
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		if err := validateTreePath(p); err != nil {
			return nil, err
		}
		if p == "." {
			return nil, fmt.Errorf("%w: %q is the store root, not a file", ErrBadPath, p)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	kinds := map[string]bool{}
	for _, p := range paths {
		m.files[p] = files[p]
		if err := m.register(p, kinds); err != nil {
			return nil, err
		}
	}
	for dir := range m.dirs {
		entries := m.dirs[dir]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
	return m, nil
}

// register records a file and every ancestor directory it implies, so a listing
// exists at each level even though only leaves were supplied. kinds remembers
// what each path has already been seen as, which is what makes the
// both-file-and-directory conflict detectable at all.
func (m *MapTreeFS) register(p string, kinds map[string]bool) error {
	segments := strings.Split(p, "/")
	dir := "."
	for i, seg := range segments {
		isDir := i < len(segments)-1
		key := seg
		if dir != "." {
			key = dir + "/" + seg
		}
		was, seen := kinds[key]
		if seen && was != isDir {
			return fmt.Errorf("%w: %q is both a file and a directory", ErrBadPath, key)
		}
		if !seen {
			kinds[key] = isDir
			m.dirs[dir] = append(m.dirs[dir], TreeEntry{Name: seg, IsDir: isDir})
		}
		if !isDir {
			return nil
		}
		dir = key
		if _, ok := m.dirs[dir]; !ok {
			m.dirs[dir] = nil
		}
	}
	return nil
}

func (m *MapTreeFS) ReadDir(dir string) ([]TreeEntry, error) {
	if err := validateTreePath(dir); err != nil {
		return nil, err
	}
	entries, ok := m.dirs[dir]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrNotExist}
	}
	return append([]TreeEntry(nil), entries...), nil
}

func (m *MapTreeFS) ReadFile(name string) ([]byte, error) {
	if err := validateTreePath(name); err != nil {
		return nil, err
	}
	data, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

// validateTreePath refuses any path that is not a clean, relative, slash-
// separated store path. It is applied on BOTH sides of the seam — by each
// adapter on the way in — so a backend cannot be walked out of its root by a
// path the walker itself constructed from publisher-supplied names.
func validateTreePath(p string) error {
	if p == "." {
		return nil
	}
	switch {
	case p == "":
		return fmt.Errorf("%w: empty path", ErrBadPath)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%w: %q must be store-relative", ErrBadPath, p)
	case strings.Contains(p, `\`):
		return fmt.Errorf("%w: %q must use forward slashes", ErrBadPath, p)
	case path.Clean(p) != p:
		// Catches "", "a/", "a/./b", "a/../b" and "../a" in one rule, and does
		// it by comparing against the canonical form rather than by listing the
		// shapes someone thought of.
		return fmt.Errorf("%w: %q is not a clean relative path", ErrBadPath, p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%w: %q escapes the store root", ErrBadPath, p)
	}
	return nil
}

// FSStore is a read-only Store over any TreeFS — the constructor a bytes-only
// backend uses.
//
// It WRAPS a TreeStore rather than embedding it, for the same reason
// BuiltinStore does: TreeStore is both a Store and a Writer, and an embedded
// one would make a read-only store satisfy Writer by accident. Wrapping makes
// "a bytes-only store cannot be written" a fact the type system enforces.
type FSStore struct {
	inner *TreeStore
}

var _ Store = (*FSStore)(nil)

// NewFSStore opens a read-only store over tfs, whose root is the store root:
// its immediate directory entries are bundles.
//
// prov is the caller's to decide, exactly as it is for the archive backend —
// the same bytes are a local export when you produced them and a pinned remote
// when someone published them, and only the caller knows which.
func NewFSStore(tfs TreeFS, prov Provenance) (*FSStore, error) {
	inner, err := newReadOnlyTreeStore(tfs, prov)
	if err != nil {
		return nil, err
	}
	return &FSStore{inner: inner}, nil
}

func (s *FSStore) Bundles(ctx context.Context) ([]BundleID, error) {
	return s.inner.Bundles(ctx)
}

func (s *FSStore) Open(ctx context.Context, id BundleID) (Bundle, error) {
	return s.inner.Open(ctx, id)
}
