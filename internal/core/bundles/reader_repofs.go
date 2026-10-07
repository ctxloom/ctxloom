package bundles

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// TreeFS is the pinned-tree seam a repofs reader reads through: LIST a
// directory, READ a file, and nothing else. It is content.TreeFS itself rather
// than a parallel declaration, so a remote tree at a pinned SHA
// (content.NewMapTreeFS over fetched bytes) and an installed tree on disk
// (content.NewAferoTreeFS) both satisfy it without an adapter.
type TreeFS = content.TreeFS

// repoFSReader reads ONE bundle out of a tree pinned at a revision:
// ProvenanceRemote, LocalityRemote.
type repoFSReader struct {
	tree TreeFS
	ref  string
	cfg  readerConfig
}

// NewRepoFSReader reads the bundle identified by ref out of a pinned tree.
//
// ref is the bundle's canonical identity (the lockfile key), not a path: one
// lockfile entry is one bundle, and the tree handed in holds that bundle's
// bytes at the revision the lockfile pinned. Provenance and trust context are
// hard-coded remote and are not parameters — a repofs reader that could be
// asked for local-context content would be a trust bypass with a struct literal
// for a weapon.
//
// A bundle is a tree whose root holds bundle.yaml alongside item directories.
func NewRepoFSReader(tree TreeFS, ref string, opts ...ReaderOption) Reader {
	return &repoFSReader{tree: tree, ref: ref, cfg: newReaderConfig(opts)}
}

// Read reports the bundle this reader was pointed at.
func (r *repoFSReader) Read(ctx context.Context) ([]BundleRead, error) {
	if r.tree == nil {
		return nil, fmt.Errorf("bundles: repofs reader for %q has no tree", r.ref)
	}
	treeForm, err := r.isTreeForm()
	if err != nil {
		return nil, err
	}
	if !treeForm {
		return nil, fmt.Errorf("bundles: %q is not a bundle: the pinned tree holds no %q directory",
			r.ref, r.leaf())
	}
	read, err := r.readTreeForm(ctx)
	if err != nil {
		return nil, err
	}
	return []BundleRead{read}, nil
}

// sourceRefTyped mints this reader's structured source ref from r.ref, its
// lockfile identity, through sourceBundleRef.
func (r *repoFSReader) sourceRefTyped() ident.BundleRef {
	br, err := sourceBundleRef(r.ref)
	if err != nil {
		warnUnmintableSource(r.cfg.rep, r.ref, err)
		return ident.BundleRef{}
	}
	return br
}

// sourceBundleRef is a reader's source ref for ref: remote.Reference.BundleRef,
// the one place a parsed reference becomes an identity, with any content
// version dropped — a source ref names the bundle, and a version-pinned read
// carries the same identity as its unpinned twin (loader_version.go).
//
// It returns the ERROR rather than the zero BundleRef alone, and that return is
// load-bearing. A caller that cannot mint here degrades to an unaddressable ref,
// and an unaddressable ref is WITHHELD from delivery — so a swallowed failure
// here is not a missing field, it is content silently vanishing. Callers must
// report what could not be minted; warnUnmintableSource is the shared way to
// do it.
func sourceBundleRef(ref string) (ident.BundleRef, error) {
	parsed, err := remote.ParseReference(ref)
	if err != nil {
		return ident.BundleRef{}, fmt.Errorf("parse %q: %w", ref, err)
	}
	parsed.ContentVersion = ""
	br, err := parsed.BundleRef()
	if err != nil {
		return ident.BundleRef{}, fmt.Errorf("convert %q: %w", ref, err)
	}
	return br, nil
}

// leaf is the last segment of the ref — the name the bundle answers to inside
// the tree it was handed, which is its own directory ("<leaf>/").
func (r *repoFSReader) leaf() string { return path.Base(strings.TrimSuffix(r.ref, "/")) }

// isTreeForm reports whether the tree holds this ref as a DIRECTORY, which is
// what makes a directory-form bundle a directory-form bundle. The tree is
// rooted at the parent in both cases — a bundle id must be a single path
// segment, so a nested ref path is absorbed by the root — which is why the
// question is about an entry in the root rather than about the root itself.
func (r *repoFSReader) isTreeForm() (bool, error) {
	entries, err := r.tree.ReadDir(".")
	if err != nil {
		return false, fmt.Errorf("bundles: listing the pinned tree for %q: %w", r.ref, err)
	}
	for _, e := range entries {
		if e.IsDir && e.Name == r.leaf() {
			return true, nil
		}
	}
	return false, nil
}

// syntheticPath names the bundle's origin in the one field callers look at for
// it, WITHOUT handing them a path they could walk: the ref, and the revision
// its bytes were pinned at when the caller supplied one. FSDir refuses it (see
// nonFilesystemPathPrefixes), which is the point — a tree read without an
// install directory has nothing on a filesystem to point at, and guessing one
// resolves against the process working directory.
func (r *repoFSReader) syntheticPath() string {
	if r.cfg.revision == "" {
		return remotePathSentinel + r.ref
	}
	return remotePathSentinel + r.ref + "@" + r.cfg.revision
}

// readTreeForm reads a directory-form bundle as installed; the pin decided
// WHICH bytes were installed.
func (r *repoFSReader) readTreeForm(ctx context.Context) (BundleRead, error) {
	tree, err := r.openTreeBundle()
	if err != nil {
		return BundleRead{}, err
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return BundleRead{}, fmt.Errorf("bundles: reading the pinned tree for %q: %w", r.ref, err)
	}
	// A declared name wins; the canonical ref is only the fallback identity for
	// a tree that named nothing.
	if b.Name == "" {
		b.Name = r.ref
	}
	b.sourceRef = r.sourceRefTyped()
	b.sourceRefSet = true
	// Path points at the tree's own envelope so FSDir resolves to the installed
	// directory — which is what makes a tree bundle's SKILL packages loadable.
	// Without an installed directory there is nothing on a filesystem to point
	// at, and the sentinel is the honest answer.
	if r.cfg.installDir != "" {
		b.Path = filepath.Join(r.cfg.installDir, DirectoryFormManifest)
	} else {
		b.Path = r.syntheticPath()
	}
	return newRead(r.ref, b, ProvenanceRemote, LocalityRemote), nil
}

// openTreeBundle opens the tree as a content.Bundle. The store is rooted at the
// tree itself, so the bundle id is the ref's last segment — a bundle id must be
// a single path segment, and a nested ref path is absorbed by the root rather
// than smuggled into the id.
func (r *repoFSReader) openTreeBundle() (content.Bundle, error) {
	store, err := content.NewFSStore(r.tree, content.Provenance{RepoURL: r.cfg.repoURL})
	if err != nil {
		return nil, fmt.Errorf("bundles: opening the pinned tree for %q: %w", r.ref, err)
	}
	id := content.BundleID(path.Base(strings.TrimSuffix(r.ref, "/")))
	tree, err := store.Open(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("bundles: the pinned tree for %q is not readable as bundle %q: %w", r.ref, id, err)
	}
	return tree, nil
}
