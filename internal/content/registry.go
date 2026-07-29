package content

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Surface is the decoded, typed representation of one item, carrying that
// kind's OWN authored fields. There is no uniform metadata struct and no
// per-kind union: a Fragment has a body and distillation fields, an MCP has a
// command and an environment, and neither pretends to be the other.
type Surface interface {
	// Kind identifies the surface. For the five trust-gated kinds this is the
	// matching trust.ItemKind constant; for a profile it is KindProfile, a
	// value this package defines and the trust gate never sees.
	Kind() trust.ItemKind
}

// TrustGated is an OPTIONAL interface: a kind the trust gate governs
// implements it, and a kind it does not govern simply does not. Profiles do
// not implement it. Making participation structural rather than a nullable
// field means "profiles are not trust-gated" cannot be forgotten by whoever
// writes the next nil check.
type TrustGated interface {
	TrustKind() trust.ItemKind
}

// Source is the ONE access abstraction Detect and Decode take. It is
// deliberately not an afero.Fs: the remote backend fetches bytes at a pinned
// SHA and is not a filesystem at all, and a skill is a DIRECTORY that cannot be
// decoded from a single byte slice.
//
// Paths are bundle-relative with forward slashes — the same path space as
// Component.Path and the digest. A SurfaceType strips its own Dir() prefix when
// it needs the name within its kind directory.
type Source interface {
	// List returns every path in this candidate group, sorted.
	List() ([]string, error)
	// Open returns one component's bytes. relPath must be one of List's paths.
	Open(relPath string) ([]byte, error)
}

// SurfaceType registers one kind. The REGISTRATION IS THE VOCABULARY: there is
// no parallel content.Kind enum and no data table mirroring these methods.
type SurfaceType interface {
	// Name is the kind's identity in this registry, e.g. "fragments".
	Name() string
	// Dir is the bundle-relative directory its items live in. It must equal
	// trust.ItemKind.Dir() for the kind's ItemKind, because that equality is
	// how a ref is mapped back to a type without a second lookup table.
	Dir() string
	// Detect reports whether this candidate group holds one of my items. It is
	// the type's own recognition code — the reason no walker in this package
	// needs to know what a sidecar, a form suffix or an event directory is.
	Detect(src Source) bool
	// Decode reads the whole item, every form it carries, into a Surface.
	Decode(src Source) (Surface, error)
	// Encode is the write-side inverse: it emits every component of every form
	// the surface carries. The caller filters by form (see formOf), so a type
	// never needs to know which form is being written.
	Encode(s Surface) ([]Component, error)
	// Forms reports the forms present in this candidate group. The FIRST entry
	// is the base form — the one that unsuffixed component filenames belong
	// to. See formOf for the layout convention this establishes.
	Forms(src Source) ([]signing.Form, error)
	// RefFor translates a candidate group's paths into a ref. Path-to-ref
	// translation lives here because it is kind-specific: a hook's name is
	// "<event>/<name>", two path segments, where every other kind's is one.
	// The caller overwrites the ref's provenance fields (RepoURL, IsLocal,
	// IsBuiltin), so a type must not try to guess them.
	RefFor(bundle string, src Source) (trust.Ref, error)
}

// KindProfile is the item kind for a bundle-shipped profile.
//
// It is defined HERE, not in the trust package, on purpose. Profiles are not
// trust-gated — Profile does not implement TrustGated — so this value never
// reaches the trust decision function, and promoting it to a trust.ItemKind
// constant would put a non-gated kind into the gate's vocabulary. The string is
// "profiles" rather than "profile" so that ItemKind.Dir()'s default branch,
// which returns the kind verbatim, yields the right directory with no change to
// the trust package.
const KindProfile trust.ItemKind = "profiles"

var registry struct {
	mu     sync.RWMutex
	byName map[string]SurfaceType
	byDir  map[string]SurfaceType
}

// Register adds a surface type. It panics on a duplicate name or directory, or
// on an empty one: a silently-ignored registration would mean a whole kind
// vanishes from every enumeration with no diagnostic, which is exactly this
// project's characteristic failure shape.
func Register(t SurfaceType) {
	if t == nil {
		panic("content.Register: nil surface type")
	}
	name, dir := t.Name(), t.Dir()
	if name == "" || dir == "" {
		panic(fmt.Sprintf("content.Register: surface type must have a name and a dir, got name=%q dir=%q", name, dir))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.byName == nil {
		registry.byName = map[string]SurfaceType{}
		registry.byDir = map[string]SurfaceType{}
	}
	if _, dup := registry.byName[name]; dup {
		panic(fmt.Sprintf("content.Register: surface type %q already registered", name))
	}
	if prev, dup := registry.byDir[dir]; dup {
		panic(fmt.Sprintf("content.Register: dir %q already claimed by surface type %q", dir, prev.Name()))
	}
	registry.byName[name] = t
	registry.byDir[dir] = t
}

// Unregister removes a surface type, reporting whether it was registered.
//
// It exists for tests: the registry is process-global, so a test that registers
// its own kind must be able to remove it again in t.Cleanup rather than leaking
// it into every later test in the binary. Production code has no reason to call
// it.
func Unregister(name string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	t, ok := registry.byName[name]
	if !ok {
		return false
	}
	delete(registry.byName, name)
	delete(registry.byDir, t.Dir())
	return true
}

// Types returns every registered surface type, ordered by directory so that
// enumeration is deterministic regardless of registration order.
func Types() []SurfaceType {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]SurfaceType, 0, len(registry.byDir))
	for _, t := range registry.byDir {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir() < out[j].Dir() })
	return out
}

// TypeForDir returns the type claiming a bundle-relative directory.
func TypeForDir(dir string) (SurfaceType, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	t, ok := registry.byDir[dir]
	return t, ok
}

// TypeForKind returns the type serving an item kind. The mapping goes through
// ItemKind.Dir(), which is why SurfaceType.Dir() must agree with it: one
// convention, no reverse table to drift.
func TypeForKind(k trust.ItemKind) (SurfaceType, bool) {
	return TypeForDir(k.Dir())
}

// As returns a form's surface as its concrete type.
//
//	frag, err := content.As[content.Fragment](ctx, form)
//
// It is a package-level function because Go interfaces cannot carry generic
// methods.
func As[T Surface](ctx context.Context, f Form) (T, error) {
	var zero T
	if f == nil {
		return zero, fmt.Errorf("content.As: nil form")
	}
	s, err := f.Surface(ctx)
	if err != nil {
		return zero, err
	}
	typed, ok := s.(T)
	if !ok {
		return zero, fmt.Errorf("%w: surface of kind %q is %T, not %T", ErrSurfaceType, s.Kind(), s, zero)
	}
	return typed, nil
}
