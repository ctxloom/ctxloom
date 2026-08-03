package remotetree_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/remote"
)

const pinnedSHA = "0123456789abcdef0123456789abcdef01234567"

// TestNew_AnswersIdenticallyToALocalTree is the payoff of the TreeFS seam.
//
// The SAME fixture tree is served two ways — from a remote fetcher at a pinned
// SHA, and from a local bytes map — and the full walker answer is compared.
// They agree because there is ONE walker; if the remote backend had brought its
// own traversal, this is the test that would catch the two drifting.
func TestNew_AnswersIdenticallyToALocalTree(t *testing.T) {
	files := fixtureFiles(t)

	remoteStore, err := remotetree.New(t.Context(), mockOf(files, "content"), spec("content"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	local, err := content.NewMapTreeFS(files)
	if err != nil {
		t.Fatalf("NewMapTreeFS: %v", err)
	}
	localStore, err := content.NewFSStore(local, content.Provenance{RepoURL: "https://example.test/o/r"})
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}
	if got, want := snapshot(t, remoteStore), snapshot(t, localStore); got != want {
		t.Fatalf("pinned-remote store disagrees with a local tree over the same bytes:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestNew_StampsRemoteProvenance: a pinned remote is neither local nor builtin,
// and the distinction decides whether content takes the auto-allow trust path.
// A backend that stamped IsLocal would launder remote content into local trust.
func TestNew_StampsRemoteProvenance(t *testing.T) {
	store, err := remotetree.New(t.Context(), mockOf(fixtureFiles(t), "content"), spec("content"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bundle, err := store.Open(t.Context(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(t.Context())
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("Refs enumerated nothing")
	}
	for _, ref := range refs {
		if ref.IsLocal || ref.IsBuiltin || ref.RepoURL != "https://example.test/o/r" {
			t.Fatalf("%s stamped local=%t builtin=%t repo=%q, want remote provenance only",
				ref.Key(), ref.IsLocal, ref.IsBuiltin, ref.RepoURL)
		}
	}
}

// TestNew_RefusesAnUnpinnedRef is the reason this backend takes a SHA and not a
// ref: a branch or a tag can be repointed between the enumeration that produced
// a digest and the fetch that produced the bytes, so the tree a signature covers
// need not be the tree that was served. Resolving a ref to a SHA is the caller's
// job (Fetcher.ResolveRef); accepting one here would hide the moment the pin
// stopped being a pin.
func TestNew_RefusesAnUnpinnedRef(t *testing.T) {
	for name, ref := range map[string]string{
		"branch":       "main",
		"tag":          "v1.2.3",
		"empty":        "",
		"short sha":    "0123456",
		"not hex":      "0123456789abcdef0123456789abcdef0123456z",
		"sha with ref": "refs/heads/main",
	} {
		s := spec("content")
		s.SHA = ref
		if _, err := remotetree.New(t.Context(), mockOf(fixtureFiles(t), "content"), s); !errors.Is(err, remotetree.ErrNotPinned) {
			t.Errorf("%s: New(%q) = %v, want ErrNotPinned", name, ref, err)
		}
	}
}

// TestNew_RefusesAnEmptyTree guards this codebase's characteristic failure. A
// content root that lists nothing is not an empty store: it is a wrong root, a
// wrong SHA, or a publisher who published a file where a directory was expected
// — which is the exact shape of the J20 failure this backend exists to fix.
// Returning a store that enumerates zero items would report success and deliver
// nothing.
func TestNew_RefusesAnEmptyTree(t *testing.T) {
	m := remote.NewMockFetcher()
	m.WithDir("content", nil)
	if _, err := remotetree.New(t.Context(), m, spec("content")); !errors.Is(err, remotetree.ErrEmptyTree) {
		t.Fatalf("New over an empty root = %v, want ErrEmptyTree", err)
	}
}

// TestNew_MissingRootIsNotFound: a root that is not there at the pinned SHA is
// a distinct answer from a root that is there and empty, and both are distinct
// from a broken transport.
func TestNew_MissingRootIsNotFound(t *testing.T) {
	m := remote.NewMockFetcher()
	m.WithDir("elsewhere", []remote.DirEntry{{Name: "x", IsDir: false}})
	_, err := remotetree.New(t.Context(), m, spec("content"))
	if !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("New over a missing root = %v, want content.ErrNotFound", err)
	}
}

// TestNew_RefusesTraversalInAnEntryName: entry names come from the forge, so
// they are exactly as trustworthy as the publisher. A ".." that reached the
// path join would fetch — and then serve, under this bundle's identity — bytes
// from outside the pinned content root.
func TestNew_RefusesTraversalInAnEntryName(t *testing.T) {
	for name, entry := range map[string]string{
		"parent":    "..",
		"self":      ".",
		"separator": "../etc",
		"backslash": `..\etc`,
		"empty":     "",
	} {
		m := remote.NewMockFetcher()
		m.WithDir("content", []remote.DirEntry{{Name: entry, IsDir: false}})
		_, err := remotetree.New(t.Context(), m, spec("content"))
		if err == nil {
			t.Errorf("%s: New accepted an entry named %q", name, entry)
			continue
		}
		if !errors.Is(err, content.ErrBadPath) {
			t.Errorf("%s: New(%q) = %v, want content.ErrBadPath", name, entry, err)
		}
	}
}

// TestNew_EnforcesAFileCountBudget: the tree is described by the publisher, so
// its size is too. Without a budget a hostile or broken listing turns a pull
// into an unbounded fetch loop.
func TestNew_EnforcesAFileCountBudget(t *testing.T) {
	entries := make([]remote.DirEntry, 0, 10)
	files := map[string][]byte{}
	for i := range 10 {
		name := fmt.Sprintf("f%d.md", i)
		entries = append(entries, remote.DirEntry{Name: name, IsDir: false})
		files["content/b/fragments/"+name] = []byte("x")
	}
	m := remote.NewMockFetcher()
	m.WithDir("content", []remote.DirEntry{{Name: "b", IsDir: true}})
	m.WithDir("content/b", []remote.DirEntry{{Name: "fragments", IsDir: true}})
	m.WithDir("content/b/fragments", entries)
	for p, data := range files {
		m.WithFile(p, data)
	}
	s := spec("content")
	s.Limits = remotetree.Limits{MaxFiles: 4}
	if _, err := remotetree.New(t.Context(), m, s); !errors.Is(err, remotetree.ErrTooLarge) {
		t.Fatalf("New with MaxFiles=4 over 10 files = %v, want ErrTooLarge", err)
	}
}

// TestNew_EnforcesAByteBudget is the same argument for total bytes: a listing of
// few but enormous files is the other half of the same bomb.
func TestNew_EnforcesAByteBudget(t *testing.T) {
	m := remote.NewMockFetcher()
	m.WithDir("content", []remote.DirEntry{{Name: "b", IsDir: true}})
	m.WithDir("content/b", []remote.DirEntry{{Name: "fragments", IsDir: true}})
	m.WithDir("content/b/fragments", []remote.DirEntry{{Name: "big.md", IsDir: false}})
	m.WithFile("content/b/fragments/big.md", make([]byte, 4096))
	s := spec("content")
	s.Limits = remotetree.Limits{MaxBytes: 1024}
	if _, err := remotetree.New(t.Context(), m, s); !errors.Is(err, remotetree.ErrTooLarge) {
		t.Fatalf("New with MaxBytes=1024 over a 4096-byte file = %v, want ErrTooLarge", err)
	}
}

// TestNew_EnforcesADepthBudget: the listing describes its own nesting, so a
// cycle in it is expressible even though a real git tree has none.
func TestNew_EnforcesADepthBudget(t *testing.T) {
	m := remote.NewMockFetcher()
	m.Dirs = selfNestingDirs("content", 60)
	s := spec("content")
	s.Limits = remotetree.Limits{MaxDepth: 8}
	if _, err := remotetree.New(t.Context(), m, s); !errors.Is(err, remotetree.ErrTooLarge) {
		t.Fatalf("New over a 60-deep tree with MaxDepth=8 = %v, want ErrTooLarge", err)
	}
}

// TestNew_FetchesAtThePinnedSHAAndNowhereElse: every read must carry the pin. A
// single call that omitted it would read the default branch, so the store would
// mix two trees and the digest would cover neither.
func TestNew_FetchesAtThePinnedSHAAndNowhereElse(t *testing.T) {
	m := mockOf(fixtureFiles(t), "content")
	if _, err := remotetree.New(t.Context(), m, spec("content")); err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(m.ListDirCalls) == 0 || len(m.FetchFileCalls) == 0 {
		t.Fatal("New made no reads")
	}
	for _, c := range m.ListDirCalls {
		if c.Ref != pinnedSHA {
			t.Fatalf("ListDir %q used ref %q, want the pinned SHA", c.Path, c.Ref)
		}
	}
	for _, c := range m.FetchFileCalls {
		if c.Ref != pinnedSHA {
			t.Fatalf("FetchFile %q used ref %q, want the pinned SHA", c.Path, c.Ref)
		}
	}
}

// TestNew_RefusesLocalOrBuiltinProvenance: this backend is remote by
// construction, and the constructor derives its provenance from RepoURL. A
// missing RepoURL must not silently produce a store whose refs claim no origin.
func TestNew_RefusesAnEmptyRepoURL(t *testing.T) {
	s := spec("content")
	s.RepoURL = ""
	if _, err := remotetree.New(t.Context(), mockOf(fixtureFiles(t), "content"), s); err == nil {
		t.Fatal("New accepted a spec with no repo URL")
	}
}

// TestNew_ServesTheRepoRootWhenNoRootIsGiven pins the empty-Root case, since a
// repository whose bundles sit at the top level is a legitimate layout and a
// naive path.Join would prefix every path with "/".
func TestNew_ServesTheRepoRootWhenNoRootIsGiven(t *testing.T) {
	files := map[string][]byte{"b/mcp/redis.yaml": []byte("command: redis\n")}
	store, err := remotetree.New(t.Context(), mockOf(files, ""), spec(""))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ids, err := store.Bundles(t.Context())
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	if len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("Bundles = %v, want [b]", ids)
	}
}

func spec(root string) remotetree.Spec {
	return remotetree.Spec{
		Owner:   "o",
		Repo:    "r",
		SHA:     pinnedSHA,
		Root:    root,
		RepoURL: "https://example.test/o/r",
	}
}

// mockOf builds a fetcher serving files (keyed by STORE-relative path) under
// root, synthesising the directory listings a forge would return.
func mockOf(files map[string][]byte, root string) *remote.MockFetcher {
	m := remote.NewMockFetcher()
	dirs := map[string]map[string]bool{}
	ensure := func(d string) {
		if _, ok := dirs[d]; !ok {
			dirs[d] = map[string]bool{}
		}
	}
	repoRoot := root
	if repoRoot == "" {
		repoRoot = "."
	}
	ensure(repoRoot)
	for p, data := range files {
		full := p
		if root != "" {
			full = root + "/" + p
		}
		m.WithFile(full, data)
		segments := strings.Split(full, "/")
		dir := repoRoot
		if root == "" {
			dir = "."
		}
		for i, seg := range segments {
			ensure(dir)
			dirs[dir][seg] = i < len(segments)-1
			if i == len(segments)-1 {
				break
			}
			if dir == "." {
				dir = seg
			} else {
				dir += "/" + seg
			}
		}
	}
	for d, names := range dirs {
		entries := make([]remote.DirEntry, 0, len(names))
		for n, isDir := range names {
			entries = append(entries, remote.DirEntry{Name: n, IsDir: isDir})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		key := d
		if root == "" && d == "." {
			key = ""
		}
		m.WithDir(key, entries)
	}
	return m
}

func selfNestingDirs(root string, depth int) map[string][]remote.DirEntry {
	out := map[string][]remote.DirEntry{}
	p := root
	for range depth {
		out[p] = []remote.DirEntry{{Name: "deeper", IsDir: true}}
		p = path.Join(p, "deeper")
	}
	out[p] = []remote.DirEntry{{Name: "x.md", IsDir: false}}
	return out
}

// fixtureFiles reads internal/content's committed tree fixture, so this backend
// is exercised against the same bundle the authored-tree tests use — a skill
// package, dot-prefixed sidecars, two-level hooks and a distilled sibling.
func fixtureFiles(t *testing.T) map[string][]byte {
	t.Helper()
	src := filepath.Join("..", "testdata", "tree")
	files := map[string][]byte{}
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
	return files
}

// snapshot renders every answer a Store gives, as deterministic text.
func snapshot(t *testing.T, s content.Store) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	ids, err := s.Bundles(ctx)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	for _, id := range ids {
		fmt.Fprintf(&b, "bundle %s\n", id)
		bundle, err := s.Open(ctx, id)
		if err != nil {
			t.Fatalf("Open %s: %v", id, err)
		}
		files, err := bundle.Files(ctx)
		if err != nil {
			t.Fatalf("Files %s: %v", id, err)
		}
		for _, f := range files {
			raw, err := bundle.ReadFile(ctx, f)
			if err != nil {
				t.Fatalf("ReadFile %s/%s: %v", id, f, err)
			}
			fmt.Fprintf(&b, "  file %s sha=%s\n", f, sum(raw))
		}
		refs, err := bundle.Refs(ctx)
		if err != nil {
			t.Fatalf("Refs %s: %v", id, err)
		}
		for _, ref := range refs {
			fmt.Fprintf(&b, "  ref %s\n", ref.Key())
			item, err := bundle.Item(ctx, ref)
			if err != nil {
				t.Fatalf("Item %s: %v", ref.Key(), err)
			}
			forms, err := item.Forms(ctx)
			if err != nil {
				t.Fatalf("Forms %s: %v", ref.Key(), err)
			}
			for _, f := range forms {
				form, err := item.Form(ctx, f)
				if err != nil {
					t.Fatalf("Form %s: %v", ref.Key(), err)
				}
				digest, err := form.Content(ctx)
				if err != nil {
					t.Fatalf("Content %s: %v", ref.Key(), err)
				}
				fmt.Fprintf(&b, "    form %s digest=%s\n", f, sum(digest))
				components, err := form.Components(ctx)
				if err != nil {
					t.Fatalf("Components %s: %v", ref.Key(), err)
				}
				for _, c := range components {
					fmt.Fprintf(&b, "      component %s mode=%s sha=%s\n", c.Path, c.Mode, sum(c.Bytes))
				}
			}
		}
	}
	return b.String()
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])[:16]
}
