// Package content is the storage-agnostic access surface for ctxloom bundle
// contents: the L0 foundation the distillation, search and materialize layers
// are built ON TOP OF rather than beside.
//
// The shape is four nested interfaces — Store -> Bundle -> Item -> Form — plus
// a registry of SurfaceType values that own their own on-disk recognition and
// codec. Nothing in Store/Bundle/Item/Form branches on an item kind: every
// kind-specific fact (which directory, which extension, how metadata is
// stored, how a path becomes a ref) lives in that kind's registered
// SurfaceType. A new kind therefore plugs in by calling Register, with no edit
// to this file.
//
// # An Item is addressable; a Form is materializable
//
// A fragment with a published distilled rewrite has ONE Item and TWO Forms,
// stored as two sibling files, so the bytes and the components hang off Form,
// never off Item. Raw bytes are reached through Form.Components.
//
// It does not read or write bundle.yaml.
package content

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// BundleID identifies one bundle within a Store. For the tree implementation
// it is the bundle directory's name, which is also trust.Ref.Bundle.
type BundleID string

// Store is the access surface, independent of where the bytes live. The tree
// implementation in this package reads an authored directory tree off an
// afero.Fs; the backends this interface exists to also span, and which are NOT
// implemented here, are remote-git-at-a-pinned-SHA (bytes-only, and pointedly
// NOT an afero.Fs), a packed archive, and resources embedded in the binary.
//
// Adding one of those must not require changing this interface — which is why
// SurfaceType.Detect and SurfaceType.Decode take a Source rather than a
// filesystem.
type Store interface {
	// Bundles lists every bundle in the store, in a deterministic order.
	Bundles(ctx context.Context) ([]BundleID, error)
	// Open returns the named bundle, or ErrNotFound.
	Open(ctx context.Context, id BundleID) (Bundle, error)
}

// Bundle is one bundle's contents.
type Bundle interface {
	ID() BundleID
	// Refs enumerates the bundle's items, in a deterministic order. An empty
	// kinds list means every registered kind; otherwise only the named kinds
	// are enumerated.
	Refs(ctx context.Context, kinds ...trust.ItemKind) ([]trust.Ref, error)
	// Item resolves one ref, or returns ErrNotFound. Resolution is by PATH,
	// not by a lookup in a parsed document: ref.Kind.Dir() names the
	// directory and ref.Name names the entry within it.
	Item(ctx context.Context, ref trust.Ref) (Item, error)

	// Files enumerates EVERY file in the bundle, bundle-relative and sorted,
	// including dot-prefixed files.
	//
	// It is deliberately total and deliberately NOT item-scoped: Refs answers
	// "what items are here", and a file that no SurfaceType claims is
	// invisible to it by construction.
	Files(ctx context.Context) ([]string, error)

	// ReadFile returns one file's exact stored bytes, by bundle-relative path.
	//
	// It reaches NON-ITEM files — the bundle envelope, the README, the LICENSE —
	// without reaching around the store to a filesystem.
	ReadFile(ctx context.Context, relPath string) ([]byte, error)
}

// Item is one addressable item. It carries no bytes of its own: the
// materializable unit is (item, form), so bytes live on Form.
type Item interface {
	Ref() trust.Ref
	// Surface returns the decoded, typed representation of the WHOLE item —
	// every form it carries. Use As to recover the concrete type.
	Surface(ctx context.Context) (Surface, error)
	// Forms reports exactly the LAYOUT forms this item actually has on disk. A
	// fragment with no distilled sibling reports only FormRaw, and so does a
	// single-form surface such as an mcp server or a hook.
	Forms(ctx context.Context) ([]trust.ContentForm, error)
	// Form returns one form, or ErrNoSuchForm when the item does not carry it.
	Form(ctx context.Context, f trust.ContentForm) (Form, error)
}

// Form is one materialization of an item.
type Form interface {
	ContentForm() trust.ContentForm
	// Components returns every component of this form, sorted by path, with
	// its bytes.
	Components(ctx context.Context) ([]Component, error)
	// Surface returns the decoded surface of the item this form belongs to.
	//
	// DIVERGENCE from the brief, which specifies both `As[T Surface](ctx, f
	// Form)` and Surface only on Item: As cannot reach a Surface from a Form
	// unless Form exposes one. Given that a Form is the unit consumers hold,
	// reaching its surface from it is what callers want anyway.
	Surface(ctx context.Context) (Surface, error)
}

// Writer is the mutating half, deliberately a separate interface: a
// pinned-remote store is read-only by construction and simply does not
// implement it.
type Writer interface {
	// Put writes the components of s that belong to form f. A surface decoded
	// with two forms therefore writes one form per call, and writing the
	// distilled form never rewrites the raw file.
	Put(ctx context.Context, ref trust.Ref, f trust.ContentForm, s Surface) error
	// Delete removes every component of the item, in every form — the content
	// file AND its metadata sidecar.
	Delete(ctx context.Context, ref trust.Ref) error

	// PutRootFile writes one bundle-ROOT file that is not an item: the bundle
	// envelope, a README, a LICENSE. It is the write-side counterpart to
	// Bundle.ReadFile, added for the same stated reason ReadFile was — a caller
	// reaching around the store to a filesystem is the signal this API is
	// incomplete.
	//
	// It refuses a path with a directory component. Everything below the root is
	// a kind directory, where an item must go through Put so its surface type
	// does the encoding. Without that guard this would be a way to place an unrecognised file
	// inside a kind directory — which Refs then fails on, turning a write here
	// into a bundle nobody can enumerate.
	PutRootFile(ctx context.Context, id BundleID, name string, data []byte) error
}

// ComponentMode is a component's declared mode. It is ONE enum rather than a
// boolean so a further value (readonly, say) can be added without a schema
// break.
//
// It governs what materialize sets on disk. It is DECLARED metadata, read from
// the item's bytes rather than from the filesystem's mode bits, because a
// filesystem mode is not portable: on Windows Go toggles only the read-only
// bit.
type ComponentMode string

const (
	// ModeRegular is a plain file — the default for anything not declared
	// executable.
	ModeRegular ComponentMode = "regular"
	// ModeExecutable is a file whose exec bit is load-bearing (a skill's
	// scripts/ entries).
	ModeExecutable ComponentMode = "executable"
)

// Component is one file of one form of one item.
type Component struct {
	// Path is bundle-relative and always uses forward slashes, e.g.
	// "fragments/solid.md".
	Path string
	// Mode is the declared mode (see ComponentMode).
	Mode ComponentMode
	// Bytes is the component's exact stored bytes.
	Bytes []byte
}

// The package's error vocabulary. Callers match with errors.Is.
var (
	// ErrNotFound reports a bundle or item that does not exist.
	ErrNotFound = errors.New("content: not found")
	// ErrNoSuchForm reports a form the item does not carry — asking a
	// never-distilled fragment for FormDistilled, for instance.
	ErrNoSuchForm = errors.New("content: no such form")
	// ErrUnrecognized reports a path no registered SurfaceType claims.
	ErrUnrecognized = errors.New("content: unrecognized item")
	// ErrSurfaceType reports a Surface handed to the wrong SurfaceType, or a
	// surface whose kind has no registered type.
	ErrSurfaceType = errors.New("content: wrong surface type")
	// ErrBadPath reports a component path that cannot be represented safely —
	// one that escapes its bundle, or that cannot be encoded as a line of
	// text (see validComponentPath).
	ErrBadPath = errors.New("content: invalid component path")
	// ErrUnclaimed reports files inside a kind directory that no registered
	// SurfaceType recognises. Enumeration FAILS on these rather than skipping
	// them: a silently dropped file is how a mis-extensioned hook vanishes.
	ErrUnclaimed = errors.New("content: unclaimed file in a kind directory")
)

// validComponentPath refuses a component path that is empty, absolute, escapes
// the bundle root, or carries a newline or backslash (a path is one line of
// text wherever it is listed, and a backslash would read as a separator on
// Windows).
func validComponentPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: empty path", ErrBadPath)
	case strings.ContainsAny(p, "\n\r\\"):
		return fmt.Errorf("%w: %q contains a newline or backslash", ErrBadPath, p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%w: %q is absolute, paths must be bundle-relative", ErrBadPath, p)
	case p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		return fmt.Errorf("%w: %q escapes the bundle root", ErrBadPath, p)
	}
	return nil
}
