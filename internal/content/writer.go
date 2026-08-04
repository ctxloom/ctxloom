package content

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Put writes the components of s that belong to form f.
//
// The surface is encoded WHOLE — every form it carries — and then filtered, so a
// type never needs to know which form is being written and writing the distilled
// form cannot disturb the raw file. The encoded paths are handed back through the
// type's own Detect and RefFor before anything touches the disk: if the surface's
// own name disagrees with the ref it is being written under, that is a caller bug
// that would otherwise store one item's bytes at another item's address.
func (s *TreeStore) Put(ctx context.Context, ref trust.Ref, f signing.Form, surface Surface) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	if err := validateBundleID(BundleID(ref.Bundle)); err != nil {
		return err
	}
	t, ok := TypeForKind(ref.Kind)
	if !ok {
		return fmt.Errorf("%w: no surface type registered for kind %q", ErrUnrecognized, ref.Kind)
	}
	if surface == nil {
		return fmt.Errorf("%w: nil surface", ErrSurfaceType)
	}
	if surface.Kind() != ref.Kind {
		return fmt.Errorf("%w: surface kind %q does not match ref kind %q", ErrSurfaceType, surface.Kind(), ref.Kind)
	}
	components, err := t.Encode(surface)
	if err != nil {
		return fmt.Errorf("content: encoding %s: %w", ref.Key(), err)
	}
	if len(components) == 0 {
		return fmt.Errorf("content: encoding %s produced no components", ref.Key())
	}
	for _, c := range components {
		if err := validateDigestPath(c.Path); err != nil {
			return err
		}
	}
	check := newMemSource(components)
	if !t.Detect(check) {
		return fmt.Errorf("%w: encoded components of %s are not recognised as %s", ErrSurfaceType, ref.Key(), t.Name())
	}
	got, err := t.RefFor(ref.Bundle, check)
	if err != nil {
		return fmt.Errorf("content: %s: %w", ref.Key(), err)
	}
	if got.Kind != ref.Kind || got.Name != ref.Name {
		return fmt.Errorf("%w: surface encodes to %s, not %s", ErrSurfaceType, got.Key(), ref.Key())
	}
	forms, err := t.Forms(check)
	if err != nil {
		return fmt.Errorf("content: forms of %s: %w", ref.Key(), err)
	}
	written := 0
	bundleDir := s.osPath(ref.Bundle)
	for _, c := range components {
		if formOf(c.Path, forms) != f {
			continue
		}
		target := filepath.Join(bundleDir, filepath.FromSlash(c.Path))
		if err := s.fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("content: creating %q: %w", filepath.Dir(target), err)
		}
		perm := fileMode(c.Mode)
		if err := afero.WriteFile(s.fsys, target, c.Bytes, perm); err != nil {
			return fmt.Errorf("content: writing %q: %w", target, err)
		}
		written++
	}
	if written == 0 {
		return fmt.Errorf("%w: %s carries no %q form to write", ErrNoSuchForm, ref.Key(), f)
	}
	return nil
}

// fileMode maps a declared ComponentMode to filesystem permissions. The
// declaration is the source of truth; the filesystem bit is derived from it,
// never the other way round.
func fileMode(m ComponentMode) os.FileMode {
	if m == ModeExecutable {
		return 0o755
	}
	return 0o644
}

// Delete removes every component of an item, in every form: content files, form
// siblings, dot-prefixed metadata sidecars, and a same-named package directory.
//
// Stored signatures are deliberately left alone. They are keyed by content hash
// precisely so that they outlive the file, and a rejection that a file deletion
// could remove would mean you could un-blacklist content by deleting it.
func (s *TreeStore) Delete(ctx context.Context, ref trust.Ref) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	bundle, err := s.Open(ctx, BundleID(ref.Bundle))
	if err != nil {
		return err
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		return err
	}
	ti, ok := item.(*treeItem)
	if !ok {
		return fmt.Errorf("content: unexpected item implementation %T", item)
	}
	paths, err := ti.src.List()
	if err != nil {
		return err
	}
	dirs := map[string]struct{}{}
	for _, p := range paths {
		target := s.osPath(path.Join(ti.bundle.dir, p))
		if err := s.fsys.Remove(target); err != nil {
			return fmt.Errorf("content: removing %q: %w", target, err)
		}
		dirs[filepath.Dir(target)] = struct{}{}
	}
	// Prune directories the item owned, deepest first — but ONLY when they are
	// genuinely empty, checked explicitly.
	//
	// Relying on Remove to refuse a non-empty directory does not hold across
	// backends: the OS filesystem refuses, but afero's in-memory filesystem
	// deletes the entry and orphans its children. Under that backend, deleting one
	// hook would have silently taken every sibling hook in the same event
	// directory with it — a data loss with no error, found by the test that asserts
	// the siblings survive.
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	kindRoot := s.osPath(path.Join(ti.bundle.dir, ti.stype.Dir()))
	for _, d := range ordered {
		if d == kindRoot || !strings.HasPrefix(d, kindRoot+string(filepath.Separator)) {
			continue
		}
		remaining, err := afero.ReadDir(s.fsys, d)
		if err != nil || len(remaining) > 0 {
			continue
		}
		_ = s.fsys.Remove(d)
	}
	return nil
}

// PutSignature stores signature bytes against a form's content digest. It does
// not verify anything: layer 0 knows only where signature bytes live.
func (s *TreeStore) PutSignature(ctx context.Context, ref trust.Ref, f signing.Form, ns Namespace, sig []byte) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	bundle, err := s.Open(ctx, BundleID(ref.Bundle))
	if err != nil {
		return err
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		return err
	}
	form, err := item.Form(ctx, f)
	if err != nil {
		return err
	}
	digest, err := form.Content(ctx)
	if err != nil {
		return err
	}
	return writeSignature(s.fsys, s.osPath(ref.Bundle), contentKey(digest), ns, sig)
}

// PutManifest writes a bundle's manifest.
//
// An empty manifest is refused rather than written: its bytes would be just the
// version marker, a constant shared by every empty bundle, so one bundle's
// signature over it would verify another's. Writing nothing while reporting
// success is this project's characteristic failure and is exactly what a
// zero-value Manifest would produce here.
func (s *TreeStore) PutManifest(ctx context.Context, id BundleID, m Manifest) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	if err := validateBundleID(id); err != nil {
		return err
	}
	if m.IsZero() {
		return fmt.Errorf("content: refusing to write an empty manifest for bundle %q", id)
	}
	ok, err := s.dirExists(string(id))
	if err != nil {
		return fmt.Errorf("content: opening bundle %q: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("%w: bundle %q", ErrNotFound, id)
	}
	target := s.osPath(path.Join(string(id), ManifestPath))
	if err := afero.WriteFile(s.fsys, target, m.Bytes(), 0o644); err != nil {
		return fmt.Errorf("content: writing manifest %q: %w", target, err)
	}
	return nil
}

// PutRootFile writes one non-item file at the bundle root.
func (s *TreeStore) PutRootFile(ctx context.Context, id BundleID, name string, data []byte) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	if err := validateBundleID(id); err != nil {
		return err
	}
	if err := validateRootFileName(name); err != nil {
		return err
	}
	if len(data) == 0 {
		// A zero-byte envelope parses as a valid empty bundle and a zero-byte
		// README is noise the manifest then attests. Neither is ever intended.
		return fmt.Errorf("content: refusing to write an empty %q for bundle %q", name, id)
	}
	ok, err := s.dirExists(string(id))
	if err != nil {
		return fmt.Errorf("content: opening bundle %q: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("%w: bundle %q", ErrNotFound, id)
	}
	target := s.osPath(path.Join(string(id), name))
	if err := afero.WriteFile(s.fsys, target, data, 0o644); err != nil {
		return fmt.Errorf("content: writing %q: %w", target, err)
	}
	return nil
}

// validateRootFileName refuses anything that is not a single bare filename.
// A directory component would place the file inside a kind directory (where an
// item must go through Put so its surface type encodes it) or inside the
// signature store (which PutBundleSignature owns).
func validateRootFileName(name string) error {
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return fmt.Errorf("%w: %q is not a bundle-root file name", ErrBadPath, name)
	}
	return nil
}

// PutBundleSignature stores signature bytes over the bundle's manifest.
//
// It files them under the FIXED BundleSigKey rather than a content-derived one
// — see BundleSigKey for why the bundle level diverges from content-keying.
func (s *TreeStore) PutBundleSignature(ctx context.Context, id BundleID, ns Namespace, sig []byte) error {
	if err := s.beginWrite(ctx); err != nil {
		return err
	}
	if err := validateBundleID(id); err != nil {
		return err
	}
	bundleDir := s.osPath(string(id))
	ok, err := s.dirExists(string(id))
	if err != nil {
		return fmt.Errorf("content: opening bundle %q: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("%w: bundle %q", ErrNotFound, id)
	}
	return writeSignature(s.fsys, bundleDir, BundleSigKey, ns, sig)
}
