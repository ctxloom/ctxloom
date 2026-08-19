package bundles

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Loader composes Readers into one addressable view of everything this session
// can see: project bundles, builtins, companion loadouts and pinned remote
// content, each reported by the reader that owns that source.
//
// It carries NO policy — no form preference and no trust gate. Both are
// PROCESS-stage decisions (docs/design/engine-delivery-seam.design.md, "ALL
// processing lives in the middle") and both live on Pipeline. What the loader
// keeps is the trust FACTS its readers established: a publisher signature is
// verified at read, before any parse, and what that turned out to be travels
// with the content for the process stage to decide on. Verification is reading;
// withholding is not.
//
// One loader therefore serves everyone: management, listing and every exposure
// surface alike.
type Loader struct {
	readers []Reader

	mu     sync.RWMutex
	loaded bool
	cat    Catalog // the resolved set; see Resolve

	// versionResolver materializes a specific historical commit-version of a
	// bundle (multi-version coexistence, trust rework, TR5). nil = no per-version
	// capability; only the version-aware methods (GetFragmentAtVersion /
	// ResolveFragmentVersions) consult it — Load and the default path never do, so
	// the lockfile-pinned default is unchanged.
	versionResolver BundleVersionResolver
	versionMu       sync.Mutex         // Protects versionCache
	versionCache    map[string]*Bundle // canonical-ref+"@"+commit → parsed historical bundle

	// warnOut is where user-facing diagnostics go (os.Stderr by default; see
	// WithWarnWriter). fsStore, which embeds this Loader, writes its
	// signature-invalidation notice here.
	warnOut io.Writer
}

// BundleVersionResolver materializes a specific historical commit-version of a
// bundle, identified by its version-less CANONICAL ref ("<url>@bundles/<path>")
// and an opaque commit revision, returning the parsed bundle as it existed at
// that commit. It backs the loader's per-version resolution: production wires it
// to the remote FetchItem primitive (remote.FetchRefBytes over the local clone
// cache); tests inject a fake. A non-nil error withholds that version
// (fail-closed) — the loader never falls back to a different version on failure.
type BundleVersionResolver func(canonicalRef, commit string) (*Bundle, error)

// remotePathSentinel marks a Bundle.Path that is NOT a filesystem path: pinned
// remote content that lives in a tree, not in a directory this process can walk.
// FSDir refuses it rather than resolving it against the working directory.
const remotePathSentinel = "<remote>:"

// NewLoader composes readers into one loader. Readers are consulted in order
// and a LATER reader's bundle wins a name collision — the same precedence the
// seed map had, where pinned remote content shadowed a stale extracted copy on
// disk.
func NewLoader(readers ...Reader) *Loader {
	return &Loader{readers: readers, warnOut: os.Stderr}
}

// WithWarnWriter redirects this loader/store's user-facing diagnostics (the
// clidiag "ctxloom: warning:" lines) away from stderr, so tests can read what
// the user would have been told. The default is os.Stderr: a warning nobody
// sees is the bug these diagnostics exist to prevent.
//
// ALL of them: the stale local signature a read found, an unresolved bundle
// ref, and an ambiguous bare fragment ask. The last two dedup process-wide (see
// bundleWarner) but still emit through this writer — dedup is the process's
// business, the sink is the caller's.
func (l *Loader) WithWarnWriter(w io.Writer) *Loader {
	l.warnOut = w
	return l
}

// WithVersionResolver attaches the per-commit-version resolver (multi-version
// coexistence, trust rework, TR5) so the loader can materialize a specific
// historical version of a bundle via the version-aware methods. A nil resolver
// (the default) leaves the loader version-unaware: the lockfile-pinned default
// is the only version, and a pinned-version request fails closed.
func (l *Loader) WithVersionResolver(resolver BundleVersionResolver) *Loader {
	l.versionResolver = resolver
	return l
}

// FS returns the filesystem this loader's local content was read from. A
// skill's trust preimage is derived from its on-disk tree
// (BundleSkill.ContentPayload), so a caller computing that preimage for an item
// this loader resolved MUST use this same filesystem — computing it against a
// different fs would produce a different hash for the same skill and silently
// withhold it.
//
// It comes from the first reader that has one (the project reader in every real
// wiring), and falls back to the OS filesystem for a loader composed entirely
// of sources that are not filesystems.
func (l *Loader) FS() afero.Fs {
	for _, r := range l.readers {
		if fsr, ok := r.(interface{ FS() afero.Fs }); ok {
			return fsr.FS()
		}
	}
	return afero.NewOsFs()
}

// index reads every reader once and builds the addressable view.
//
// The read is memoized for the loader's life, which is what makes repeated
// resolution cheap and what keeps a companion probe (an EXEC per companion)
// from running per lookup. A write through fsStore invalidates it, so a
// save-then-read within one command sees the new bytes.
// index resolves every reader once and memoizes the result for this loader's
// life, which is what makes repeated resolution cheap and what keeps a companion
// probe (an EXEC per companion) from running per lookup. A write through fsStore
// invalidates it, so a save-then-read within one command sees the new bytes.
//
// The reading itself lives in Resolve, which holds no loader state: this method
// is now only the memo around it.
func (l *Loader) index() {
	l.mu.RLock()
	loaded := l.loaded
	l.mu.RUnlock()
	if loaded {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded {
		return
	}
	l.cat, l.loaded = Resolve(context.Background(), l.readers...), true
}

// Catalog returns the resolved set, reading the sources on first use. Callers
// that only QUERY should prefer this over holding the loader: a Catalog cannot
// re-read the world, which is precisely the property the loader lacks.
func (l *Loader) Catalog() Catalog {
	l.index()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cat
}

// isSyntheticPath reports whether a Bundle.Path is one of the non-filesystem
// sentinels, so the loader never indexes one as though it were a real path.
func isSyntheticPath(path string) bool {
	for _, prefix := range nonFilesystemPathPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Invalidate drops the memoized read so the next resolution re-reads every
// source.
//
// It is EXPORTED because the obligation is no longer local: one loader is now
// shared for a Config's life, so anything that changes what the readers would
// see — a bundle written through the store, a pull landing new pinned content —
// must say so. It invalidates IN PLACE rather than replacing the loader, so
// every holder of this pointer sees the same fresh view; swapping the object
// would leave a store reading a loader nothing else refers to any more.
func (l *Loader) Invalidate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loaded, l.cat = false, Catalog{}
}

// Reads returns every bundle this loader can see, with the trust facts its
// reader established. It is the honest shape of the read stage — content plus
// facts, nothing dropped on policy grounds — and the seam the process stage
// will decide on.
func (l *Loader) Reads() []BundleRead { return l.Catalog().Reads() }

// Read resolves a bundle by name to the READ a reader produced for it — the
// content plus the trust facts that reader established.
//
// It exists because the decision function keys on those facts (Authorizer's
// Exposure carries a BundleRead), and the executable surfaces resolve a bundle
// by ref without ever going through a Pipeline: config.loadMCPFromBundleRef and
// config.loadHooksFromBundleRef both need the read, not just the content. Load
// remains for callers that genuinely only want the bundle.
func (l *Loader) Read(name string) (BundleRead, bool) {
	return l.lookup(name)
}

// ReadKey resolves a bundle by its EXACT resolution key (Catalog.LookupKey) —
// the load-path counterpart to Read for a caller that already holds a
// trust.BundleKey rather than an ask string. No search, no ambiguity.
func (l *Loader) ReadKey(key trust.BundleKey) (BundleRead, bool) {
	return l.Catalog().LookupKey(key)
}

// LoadKey reads a bundle by its EXACT resolution key. See ReadKey.
func (l *Loader) LoadKey(key trust.BundleKey) (*Bundle, error) {
	read, ok := l.Catalog().LookupKey(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, key)
	}
	return read.Bundle, nil
}

// Load reads a bundle by name.
// Name can be:
//   - Simple name: "go-tools"
//   - Remote-qualified: "alice/go-tools"
//   - Canonical: "https://…@bundles/go-tools", "ctxloom:companion@ltk"
//   - Local canonical: "ctxloom:local@bundles/go-tools" — the qualified identity
//     the assembly pipeline carries (see remote.CanonicalBundleRef), resolved to
//     the same project bundle as the simple name.
func (l *Loader) Load(name string) (*Bundle, error) {
	read, ok := l.lookup(name)
	if !ok {
		return nil, l.missing(name)
	}
	return read.Bundle, nil
}

// missing explains an ask that resolved to nothing.
//
// "Not found" and "found, but unreadable" are different facts and must not
// share an exit: a bundle whose file will not parse is a fault the asker can
// FIX, and reporting it as absent points them at their spelling instead of at
// the file. Readers that can tell the two apart say so through ReadFailure.
func (l *Loader) missing(name string) error {
	for _, r := range l.readers {
		rf, ok := r.(interface{ ReadFailure(string) error })
		if !ok {
			continue
		}
		if err := rf.ReadFailure(name); err != nil {
			return fmt.Errorf("bundle %s could not be read: %w", name, err)
		}
	}
	return fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
}

// lookup resolves a ref to a read: exact identity first, then the version-less
// canonical key (a ref carrying a content version resolves to the pinned one),
// then the local-canonical form's plain path.
func (l *Loader) lookup(name string) (BundleRead, bool) {
	return l.Catalog().Lookup(name)
}

// Find locates the FILE backing a bundle. It exists for the two callers that
// need the path itself — deleting a bundle, and reporting whether a short name
// resolves — and refuses a bundle that has no file, because a synthetic path is
// not one.
func (l *Loader) Find(name string) (string, error) {
	if err := ValidateBundleName(name); err != nil {
		return "", err
	}
	read, ok := l.lookup(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
	}
	if read.Bundle.Path == "" || isSyntheticPath(read.Bundle.Path) {
		return "", fmt.Errorf("bundle %q has no file on this machine (it came from %s)", name, read.Provenance)
	}
	return read.Bundle.Path, nil
}

// List returns every bundle this loader can see, as listing metadata.
//
// A listing that wants a NARROWER set asks the resolved Catalog for it
// (Catalog.Scoped(...).Infos()) rather than filtering this result: BundleInfo
// carries no provenance, so a filter applied out here has nothing to filter on.
func (l *Loader) List() ([]*BundleInfo, error) {
	return l.Catalog().Infos(), nil
}

// BundleInfo holds metadata about a bundle without loading full content.
type BundleInfo struct {
	Name          string
	Path          string
	Version       string
	Description   string
	Tags          []string
	FragmentCount int
	CommandCount  int
	MCPCount      int
	ProfileCount  int
	// Deleted marks a bundle that existed in an installed remote's history but
	// is gone from that repo at the current revision — removed upstream. Such an
	// entry carries only Name (the canonical ref); metadata is unavailable since
	// the content no longer exists to read.
	Deleted bool

	// Held marks a lockfile entry frozen at its recorded SHA (LockEntry.Pinned,
	// toggled by `ctxloom deps hold`/`unhold`): `deps upgrade` leaves it put
	// even when its constraint would allow a newer commit.
	//
	// It is carried into the listing because a hold is a decision someone made
	// ON PURPOSE, and a hold nobody can see is indistinguishable from a broken
	// pull. Without it, "we froze this deliberately" and "the sync is failing"
	// render as identical output, and the only way to tell them apart is diffing
	// lockfiles by hand.
	Held bool

	// Retracted marks a bundle the publisher WITHDREW (LockEntry.Retracted),
	// learned from the remote manifest at the last pull that had the network.
	// RetractedReason is the publisher's stated reason — display only, never a
	// decision input.
	//
	// Silence about this is worse than silence about a hold: the content is
	// still installed and still being served while its publisher has said not to
	// use it.
	Retracted       bool
	RetractedReason string

	// Signer is the VERIFIED publisher identity of this bundle's bytes, or ""
	// when the bundle is unsigned — no signature, or one by a key this machine
	// does not trust to publish. It is Bundle.Signer() carried through, so it is
	// never an unverified claim and never comes from the bundle's own content.
	//
	// It is on the listing because "unsigned" is otherwise invisible: unsigned
	// remote content is withheld from exposure and does NOT appear in the
	// pending-review list (unsigned is not pending), so nothing named the bundle
	// or the reason. See doctorCheckContentTrust.
	Signer string
}
