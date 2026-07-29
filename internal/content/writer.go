package content

import (
	"context"
	"fmt"
	"os"
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
	if err := ctx.Err(); err != nil {
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
	bundleDir := filepath.Join(s.root, ref.Bundle)
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
	if err := ctx.Err(); err != nil {
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
		target := filepath.Join(ti.bundle.dir, filepath.FromSlash(p))
		if err := s.fsys.Remove(target); err != nil {
			return fmt.Errorf("content: removing %q: %w", target, err)
		}
		dirs[filepath.Dir(target)] = struct{}{}
	}
	// Prune directories the item owned, deepest first. Remove fails on a
	// non-empty directory, which is exactly the wanted guard: a shared
	// directory (a hook event directory with siblings left in it) survives.
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	kindRoot := filepath.Join(ti.bundle.dir, filepath.FromSlash(ti.stype.Dir()))
	for _, d := range ordered {
		if d == kindRoot || !strings.HasPrefix(d, kindRoot) {
			continue
		}
		_ = s.fsys.Remove(d)
	}
	return nil
}

// PutSignature stores signature bytes against a form's content digest. It does
// not verify anything: layer 0 knows only where signature bytes live.
func (s *TreeStore) PutSignature(ctx context.Context, ref trust.Ref, f signing.Form, ns Namespace, sig []byte) error {
	if err := ctx.Err(); err != nil {
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
	return writeSignature(s.fsys, filepath.Join(s.root, ref.Bundle), contentKey(digest), ns, sig)
}
