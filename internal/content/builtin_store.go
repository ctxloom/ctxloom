package content

import (
	"context"
	"errors"
	"io/fs"

	"github.com/spf13/afero"
)

// BuiltinStore is a read-only Store over bundles compiled INTO the binary.
//
// The transport is an io/fs.FS — what `//go:embed` produces — and nothing else
// about the model changes: every registered SurfaceType, the candidate walker,
// the digest and the form partition are reused exactly as the authored-tree
// backend uses them. That is the property the L0 interface exists to have, and
// this backend is the cheapest demonstration of it: it is a wrapper, not a
// second implementation.
//
// # Why it wraps rather than embeds TreeStore
//
// TreeStore is BOTH a Store and a Writer. Embedding it here would make a
// builtin store satisfy Writer by accident, and its Put would appear to
// succeed while writing into a copy that is discarded — a silent no-op with a
// success return, which is this codebase's characteristic bug. Wrapping and
// re-exporting only the two Store methods makes "a builtin store is read-only"
// a fact the type system enforces rather than a convention, and
// TestBuiltinStore_IsNotAWriter pins it.
//
// # Provenance
//
// Refs are stamped IsBuiltin, never IsLocal. The distinction is load-bearing:
// local-authored content takes the auto-allow trust path, and embedded content
// must not inherit it just because it also has no repo URL.
type BuiltinStore struct {
	inner *TreeStore
}

var _ Store = (*BuiltinStore)(nil)

// ErrNoFS reports a store constructed with no source of bytes.
var ErrNoFS = errors.New("content: no filesystem supplied")

// NewBuiltinStore builds a read-only store over an embedded filesystem whose
// top-level entries are bundle directories.
func NewBuiltinStore(fsys fs.FS) (*BuiltinStore, error) {
	if fsys == nil {
		return nil, ErrNoFS
	}
	// afero.FromIOFS adapts a read-only io/fs.FS; the TreeStore beneath never
	// writes through it, and the wrapper above never offers a way to try.
	inner, err := NewTreeStore(afero.FromIOFS{FS: fsys}, ".", Provenance{IsBuiltin: true})
	if err != nil {
		return nil, err
	}
	return &BuiltinStore{inner: inner}, nil
}

func (s *BuiltinStore) Bundles(ctx context.Context) ([]BundleID, error) {
	return s.inner.Bundles(ctx)
}

func (s *BuiltinStore) Open(ctx context.Context, id BundleID) (Bundle, error) {
	return s.inner.Open(ctx, id)
}
