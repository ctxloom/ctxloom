package content

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// maxWalkDepth caps how far below a kind directory the candidate walk descends.
// Only hooks need more than one level ("hooks/<event>/<name>.yaml"); the cap
// exists so a symlink loop or a pathological tree cannot turn enumeration into
// an unbounded walk.
const maxWalkDepth = 8

// Provenance is the source attribution a store stamps onto every ref it
// produces. A SurfaceType never guesses it: the same registered type serves an
// authored local tree, a pinned remote and an embedded builtin, and only the
// store knows which it is.
type Provenance struct {
	RepoURL   string
	IsLocal   bool
	IsBuiltin bool
}

func (p Provenance) validate() error {
	if p.IsLocal && p.IsBuiltin {
		return errors.New("content: provenance cannot be both local and builtin")
	}
	if p.RepoURL != "" && (p.IsLocal || p.IsBuiltin) {
		return errors.New("content: provenance cannot carry a repo URL and also be local or builtin")
	}
	if p.RepoURL == "" && !p.IsLocal && !p.IsBuiltin {
		return errors.New("content: provenance must be local, builtin, or carry a repo URL")
	}
	return nil
}

func (p Provenance) stamp(r trust.Ref) trust.Ref {
	r.RepoURL = p.RepoURL
	r.IsLocal = p.IsLocal
	r.IsBuiltin = p.IsBuiltin
	return r
}

// TreeStore reads (and, as a Writer, writes) bundles laid out as directory
// trees: "<root>/<bundle>/<kind-dir>/<name><ext>", where kind-dir comes from
// the existing trust.ItemKind.Dir() and therefore matches the ref grammar
// unchanged. Addressing is untouched by this package — only RESOLUTION changes,
// from a lookup in a parsed document to a path.
type TreeStore struct {
	fsys afero.Fs
	root string
	prov Provenance
}

var (
	_ Store  = (*TreeStore)(nil)
	_ Writer = (*TreeStore)(nil)
)

// NewTreeStore opens a store rooted at root on fsys.
func NewTreeStore(fsys afero.Fs, root string, prov Provenance) (*TreeStore, error) {
	if fsys == nil {
		return nil, errors.New("content: nil filesystem")
	}
	if root == "" {
		return nil, errors.New("content: empty store root")
	}
	if err := prov.validate(); err != nil {
		return nil, err
	}
	return &TreeStore{fsys: fsys, root: root, prov: prov}, nil
}

// Bundles lists the store's bundles: every immediate subdirectory of the root
// whose name is not dot-prefixed, sorted.
//
// Dot-prefixed directories are skipped because the store's own machinery lives
// in them (.sigs, and whatever a later slice adds) — the one place in this
// package where a dotfile is deliberately ignored rather than deliberately
// included.
func (s *TreeStore) Bundles(ctx context.Context) ([]BundleID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := afero.ReadDir(s.fsys, s.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: store root %q", ErrNotFound, s.root)
		}
		return nil, fmt.Errorf("content: reading store root %q: %w", s.root, err)
	}
	var out []BundleID
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, BundleID(e.Name()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Open returns one bundle.
func (s *TreeStore) Open(ctx context.Context, id BundleID) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBundleID(id); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, string(id))
	ok, err := afero.DirExists(s.fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("content: opening bundle %q: %w", id, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: bundle %q", ErrNotFound, id)
	}
	return &treeBundle{store: s, id: id, dir: dir}, nil
}

// validateBundleID refuses an id that is not a single path segment. A bundle id
// becomes a directory name and a trust.Ref.Bundle, and a traversal in either
// would let one bundle's identity address another's files.
func validateBundleID(id BundleID) error {
	s := string(id)
	switch {
	case s == "":
		return fmt.Errorf("%w: empty bundle id", ErrBadPath)
	case s == "." || s == "..":
		return fmt.Errorf("%w: bundle id %q", ErrBadPath, s)
	case strings.ContainsAny(s, `/\`):
		return fmt.Errorf("%w: bundle id %q must be a single path segment", ErrBadPath, s)
	case strings.HasPrefix(s, "."):
		return fmt.Errorf("%w: bundle id %q must not be dot-prefixed", ErrBadPath, s)
	}
	return nil
}

// treeBundle is one bundle directory.
type treeBundle struct {
	store *TreeStore
	id    BundleID
	dir   string
}

func (b *treeBundle) ID() BundleID { return b.id }

// Refs enumerates the bundle's items. Ordering is by ref key so repeated calls
// agree byte-for-byte.
func (b *treeBundle) Refs(ctx context.Context, kinds ...trust.ItemKind) ([]trust.Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, k := range kinds {
		wanted[k.Dir()] = true
	}
	var out []trust.Ref
	for _, t := range Types() {
		if len(wanted) > 0 && !wanted[t.Dir()] {
			continue
		}
		candidates, unclaimed, err := b.candidates(t)
		if err != nil {
			return nil, err
		}
		// FAIL LOUD. A file inside a kind directory that no SurfaceType claims
		// used to be dropped in silence, which is two bugs wearing one coat: a
		// mis-extensioned hook (guard.yml) simply vanishes — the withheld
		// pre_tool guardrail this design exists to prevent, reachable by typo —
		// and an added file is never enumerated, so nothing ever covers it.
		// Refusing to enumerate at all is the only answer that cannot be
		// ignored.
		if len(unclaimed) > 0 {
			return nil, fmt.Errorf("%w: bundle %q has %d file(s) under %s/ that no surface type recognises: %s",
				ErrUnclaimed, b.id, len(unclaimed), t.Dir(), strings.Join(unclaimed, ", "))
		}
		for _, src := range candidates {
			ref, err := t.RefFor(string(b.id), src)
			if err != nil {
				return nil, fmt.Errorf("content: %s in bundle %q: %w", t.Name(), b.id, err)
			}
			out = append(out, b.store.prov.stamp(ref))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// Item resolves one ref by PATH. There is no document to search: ref.Kind.Dir()
// names the directory, ref.Name names the entry, and grouping by stem in the
// containing directory reassembles the item's components — content file,
// dot-prefixed sidecar, form siblings and same-named subdirectory alike.
func (b *treeBundle) Item(ctx context.Context, ref trust.Ref) (Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t, ok := TypeForKind(ref.Kind)
	if !ok {
		return nil, fmt.Errorf("%w: no surface type registered for kind %q", ErrUnrecognized, ref.Kind)
	}
	if ref.Name == "" {
		return nil, fmt.Errorf("%w: empty item name", ErrBadPath)
	}
	relDir, stem := t.Dir(), ref.Name
	if i := strings.LastIndex(ref.Name, "/"); i >= 0 {
		relDir, stem = relDir+"/"+ref.Name[:i], ref.Name[i+1:]
	}
	if err := validateDigestPath(relDir + "/" + stem); err != nil {
		return nil, err
	}
	paths, err := b.group(relDir, stem)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ref.Key())
	}
	src := newTreeSource(b.store.fsys, b.dir, paths)
	if !t.Detect(src) {
		return nil, fmt.Errorf("%w: %s (files present but not recognised as %s)", ErrNotFound, ref.Key(), t.Name())
	}
	got, err := t.RefFor(string(b.id), src)
	if err != nil {
		return nil, fmt.Errorf("content: %s in bundle %q: %w", t.Name(), b.id, err)
	}
	// Fail closed on disagreement: if the type reads a different identity out
	// of these paths than the caller asked for, returning the item anyway would
	// serve one item's bytes under another's ref.
	if got.Kind != ref.Kind || got.Name != ref.Name {
		return nil, fmt.Errorf("%w: %s resolves to %s", ErrNotFound, ref.Key(), got.Key())
	}
	return &treeItem{bundle: b, stype: t, ref: b.store.prov.stamp(got), src: src}, nil
}

// group returns the bundle-relative paths of one candidate group: the files in
// relDir whose stem matches, plus everything under a same-named subdirectory.
func (b *treeBundle) group(relDir, stem string) ([]string, error) {
	entries, err := afero.ReadDir(b.store.fsys, b.abs(relDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("content: reading %q: %w", relDir, err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() != stem {
				continue
			}
			sub, err := b.filesUnder(relDir + "/" + e.Name())
			if err != nil {
				return nil, err
			}
			paths = append(paths, sub...)
			continue
		}
		if stemOf(e.Name()) == stem {
			paths = append(paths, relDir+"/"+e.Name())
		}
	}
	return paths, nil
}

// candidates enumerates every candidate group under a type's directory, and —
// separately — every file under it that ended up in no group at all.
//
// The unclaimed set is computed by SUBTRACTION rather than by threading a
// reject list through the recursion: every file under the kind directory, minus
// every file some accepted candidate claimed. Subtraction is what makes the
// answer complete by construction — a file cannot be missed by forgetting to
// report it at one of the walk's several decline points, and a file below the
// depth cap is reported too rather than being silently out of reach.
func (b *treeBundle) candidates(t SurfaceType) ([]*treeSource, []string, error) {
	out, err := b.walk(t, t.Dir(), 0)
	if err != nil {
		return nil, nil, err
	}
	all, err := b.filesUnder(t.Dir())
	if err != nil {
		return nil, nil, err
	}
	claimed := make(map[string]struct{}, len(all))
	for _, src := range out {
		paths, err := src.List()
		if err != nil {
			return nil, nil, err
		}
		for _, p := range paths {
			claimed[p] = struct{}{}
		}
	}
	var unclaimed []string
	for _, p := range all {
		if _, ok := claimed[p]; !ok {
			unclaimed = append(unclaimed, p)
		}
	}
	sort.Strings(unclaimed)
	return out, unclaimed, nil
}

// Files enumerates every file in the bundle. See Bundle.Files for why this is
// total and applies no exemption of its own.
func (b *treeBundle) Files(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.filesUnder(".")
}

// ReadFile returns one file's bytes, refusing any path that escapes the bundle.
func (b *treeBundle) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateDigestPath(relPath); err != nil {
		return nil, err
	}
	data, err := afero.ReadFile(b.store.fsys, b.abs(relPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, b.id, relPath)
		}
		return nil, fmt.Errorf("content: reading %q: %w", relPath, err)
	}
	return data, nil
}

// Manifest reads and parses the bundle manifest.
func (b *treeBundle) Manifest(ctx context.Context) (Manifest, error) {
	raw, err := b.ReadFile(ctx, ManifestPath)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Manifest{}, fmt.Errorf("%w: %s", ErrManifestMissing, b.id)
		}
		return Manifest{}, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", b.id, err)
	}
	return m, nil
}

// BundleSignatures returns the signature bytes filed against the manifest.
func (b *treeBundle) BundleSignatures(ctx context.Context) (SigSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return readSignatures(b.store.fsys, b.dir, BundleSigKey)
}

// walk groups the files under relDir into candidates and asks the type to claim
// them.
//
// The grouping rule is STEM WITHIN A DIRECTORY, applied before any type is
// consulted — the walker cannot know that "solid.md" and ".solid.meta.yaml" are
// one item unless it groups first, and a sidecar must be a COMPONENT of its item
// rather than an item of its own. A same-named subdirectory merges into its
// stem's group, which is what lets a skill's package directory and the skill's
// sidecar be one candidate.
//
// A subdirectory with no matching stem is offered to the type whole: the type
// claims it if it is an item (a skill package) and declines if it is a namespace
// segment (a hook event directory), in which case the walk descends. That is the
// entire policy — no kind-specific structure knowledge lives here.
func (b *treeBundle) walk(t SurfaceType, relDir string, depth int) ([]*treeSource, error) {
	if depth > maxWalkDepth {
		return nil, nil
	}
	entries, err := afero.ReadDir(b.store.fsys, b.abs(relDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("content: reading %q: %w", relDir, err)
	}
	groups := map[string][]string{}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
			continue
		}
		stem := stemOf(e.Name())
		groups[stem] = append(groups[stem], relDir+"/"+e.Name())
	}
	sort.Strings(dirs)

	var out []*treeSource
	var unclaimed []string
	for _, d := range dirs {
		sub, err := b.filesUnder(relDir + "/" + d)
		if err != nil {
			return nil, err
		}
		if _, merges := groups[d]; merges {
			groups[d] = append(groups[d], sub...)
			continue
		}
		src := newTreeSource(b.store.fsys, b.dir, sub)
		if len(sub) > 0 && t.Detect(src) {
			out = append(out, src)
			continue
		}
		unclaimed = append(unclaimed, d)
	}
	for _, d := range unclaimed {
		nested, err := b.walk(t, relDir+"/"+d, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}
	stems := make([]string, 0, len(groups))
	for stem := range groups {
		stems = append(stems, stem)
	}
	sort.Strings(stems)
	for _, stem := range stems {
		src := newTreeSource(b.store.fsys, b.dir, groups[stem])
		if t.Detect(src) {
			out = append(out, src)
		}
	}
	return out, nil
}

// filesUnder lists every regular file below relDir, bundle-relative and sorted.
// It walks the directory itself rather than globbing: filepath.Glob("*") does not
// match dot-prefixed names, and a sidecar missed here would silently leave the
// digest.
func (b *treeBundle) filesUnder(relDir string) ([]string, error) {
	var out []string
	base := b.abs(relDir)
	err := afero.Walk(b.store.fsys, base, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil {
			return relErr
		}
		out = append(out, path.Join(relDir, filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("content: walking %q: %w", relDir, err)
	}
	sort.Strings(out)
	return out, nil
}

func (b *treeBundle) abs(rel string) string {
	return filepath.Join(b.dir, filepath.FromSlash(rel))
}

// treeItem is one resolved item.
type treeItem struct {
	bundle *treeBundle
	stype  SurfaceType
	ref    trust.Ref
	src    *treeSource
}

func (i *treeItem) Ref() trust.Ref { return i.ref }

func (i *treeItem) Surface(ctx context.Context) (Surface, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := i.stype.Decode(i.src)
	if err != nil {
		return nil, fmt.Errorf("content: decoding %s: %w", i.ref.Key(), err)
	}
	return s, nil
}

func (i *treeItem) Forms(ctx context.Context) ([]signing.Form, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	forms, err := i.stype.Forms(i.src)
	if err != nil {
		return nil, fmt.Errorf("content: forms of %s: %w", i.ref.Key(), err)
	}
	return forms, nil
}

func (i *treeItem) Form(ctx context.Context, f signing.Form) (Form, error) {
	forms, err := i.Forms(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(forms, f) {
		return nil, fmt.Errorf("%w: %s has no %q form (has %v)", ErrNoSuchForm, i.ref.Key(), f, forms)
	}
	return &treeForm{item: i, form: f, forms: forms}, nil
}

// treeForm is one attestable form of one item.
type treeForm struct {
	item  *treeItem
	form  signing.Form
	forms []signing.Form
}

func (f *treeForm) ContentForm() signing.Form { return f.form }

func (f *treeForm) Surface(ctx context.Context) (Surface, error) { return f.item.Surface(ctx) }

// Components returns this form's components with their exact stored bytes.
//
// The bytes come from the filesystem, never from re-encoding the decoded
// surface: a re-serialization would attest what this build of ctxloom would have
// written rather than what the publisher actually shipped. The MODE, by
// contrast, is a declaration, so it is read back through the type's own Encode —
// which is where a type states its declared modes — and applied to the stored
// bytes.
func (f *treeForm) Components(ctx context.Context) ([]Component, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := f.item.src.List()
	if err != nil {
		return nil, err
	}
	modes, err := f.declaredModes()
	if err != nil {
		return nil, err
	}
	var out []Component
	for _, p := range paths {
		if formOf(p, f.forms) != f.form {
			continue
		}
		data, err := f.item.src.Open(p)
		if err != nil {
			return nil, err
		}
		mode := ModeRegular
		if m, ok := modes[p]; ok && m != "" {
			mode = m
		}
		out = append(out, Component{Path: p, Mode: mode, Bytes: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// declaredModes asks the type what modes its surface declares, by round-tripping
// Decode -> Encode and reading only the modes off the result.
func (f *treeForm) declaredModes() (map[string]ComponentMode, error) {
	s, err := f.item.stype.Decode(f.item.src)
	if err != nil {
		return nil, fmt.Errorf("content: decoding %s: %w", f.item.ref.Key(), err)
	}
	encoded, err := f.item.stype.Encode(s)
	if err != nil {
		return nil, fmt.Errorf("content: reading declared modes of %s: %w", f.item.ref.Key(), err)
	}
	modes := make(map[string]ComponentMode, len(encoded))
	for _, c := range encoded {
		modes[c.Path] = c.Mode
	}
	return modes, nil
}

func (f *treeForm) Content(ctx context.Context) ([]byte, error) {
	components, err := f.Components(ctx)
	if err != nil {
		return nil, err
	}
	if len(components) == 0 {
		// A form with no components would digest to just the version marker,
		// which would then be a stable hash shared by every empty form — an
		// approval of one would match all of them. Refuse instead.
		return nil, fmt.Errorf("content: %s form %q has no components", f.item.ref.Key(), f.form)
	}
	return Digest(components)
}

func (f *treeForm) Signatures(ctx context.Context) (SigSet, error) {
	digest, err := f.Content(ctx)
	if err != nil {
		return nil, err
	}
	return readSignatures(f.item.bundle.store.fsys, f.item.bundle.dir, contentKey(digest))
}

// itemPath is the bundle-relative path a name resolves to inside a kind
// directory, used by the writer.
func itemPath(dir, name, ext string) string {
	return path.Join(dir, name) + ext
}
