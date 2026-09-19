package bundles

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// Loader is one resolved GENERATION of everything a session can see —
// project bundles, builtins, companion loadouts and pinned remote content,
// each reported by the reader that owns that source — plus the version seam,
// whose space is unbounded and so can never be part of a set.
//
// It carries NO policy — no form preference and no trust gate. Both are
// PROCESS-stage decisions (docs/design/engine-delivery-seam.design.md, "ALL
// processing lives in the middle") and both live on Pipeline. What the loader
// keeps is the trust FACTS its readers established: a publisher signature is
// verified at read, before any parse, and what that turned out to be travels
// with the content for the process stage to decide on.
//
// A Loader never re-reads the world: it is built from readers resolved ONCE,
// or over a Catalog someone else resolved (the config Owner's generation).
// A write that changes what the readers would see is announced by building
// the next generation (config.Owner.Reload), never by invalidating this one.
// Every query method is answered by Catalog — a caller that only queries
// should hold that instead.
type Loader struct {
	cat Catalog

	// versionResolver materializes a specific historical commit-version of a
	// remote bundle (FetchItem); nil = version-unaware.
	versionResolver BundleVersionResolver
	versionMu       sync.Mutex         // protects versionCache
	versionCache    map[string]*Bundle // canonical-ref+"@"+commit → parsed historical bundle

	// warnOut receives the read-time diagnostics (a stale local signature, an
	// unresolved ref, an ambiguous bare ask); os.Stderr unless redirected.
	warnOut io.Writer
}

// BundleVersionResolver materializes one pinned historical version of a
// remote bundle: canonicalRef at commit, parsed. Injected because fetching is
// an adapter's job; the loader only caches what it returns.
type BundleVersionResolver func(canonicalRef, commit string) (*Bundle, error)

// remotePathSentinel prefixes the synthetic Path a remote read reports; see
// isSyntheticPath.
const remotePathSentinel = "<remote>:"

// NewLoader resolves readers ONCE into a Loader.
func NewLoader(readers ...Reader) *Loader {
	return LoaderOf(Resolve(context.Background(), readers...))
}

// LoaderOf wraps an already-resolved Catalog — the config Owner's generation
// — so Loader-shaped callers see exactly what the Snapshot carries.
func LoaderOf(cat Catalog) *Loader {
	return &Loader{cat: cat, warnOut: os.Stderr}
}

// WithWarnWriter redirects the read-time diagnostics (stale local signature,
// unresolved ref, ambiguous bare ask — the same lines clidiag prints as
// "ctxloom: warning:") away from stderr, so tests can read what the user
// would have been told. The default is os.Stderr: a warning nobody sees is
// the bug these diagnostics exist to prevent.
func (l *Loader) WithWarnWriter(w io.Writer) *Loader {
	l.warnOut = w
	return l
}

// WithVersionResolver attaches the per-commit-version resolver so the
// version-aware methods can materialize a historical version of a bundle. A
// nil resolver (the default) leaves the loader version-unaware: the
// lockfile-pinned default is the only version, and a pinned-version request
// fails closed.
func (l *Loader) WithVersionResolver(resolver BundleVersionResolver) *Loader {
	l.versionResolver = resolver
	return l
}

// FS returns the filesystem this loader's local content was read from. A
// skill's trust preimage is derived from its on-disk tree
// (BundleSkill.ContentPayload), so a caller computing that preimage for an
// item this loader resolved MUST use this same filesystem — a different fs
// would hash the same skill differently and silently withhold it.
func (l *Loader) FS() afero.Fs { return l.cat.FS() }

// Catalog is the resolved set, with this loader's warning sink attached.
func (l *Loader) Catalog() Catalog { return l.cat.WithWarnWriter(l.warnOut) }

// isSyntheticPath reports whether path is a reader's synthetic marker rather
// than a filesystem location.
func isSyntheticPath(path string) bool {
	for _, prefix := range nonFilesystemPathPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (l *Loader) Reads() []BundleRead { return l.Catalog().Reads() }

// Read resolves a bundle by name to the READ a reader produced for it — the
// content plus the trust facts that reader established.
//
// It exists because the decision function keys on those facts (Authorizer's
// Exposure carries a BundleRead), and the executable surfaces resolve a bundle
// by ref without ever going through a Pipeline: config.loadMCPFromBundleRef and
// config.loadHooksFromBundleRef both need the read, not just the content. Load
// remains for callers that genuinely only want the bundle.
func (l *Loader) Read(name string) (BundleRead, error) { return l.Catalog().Read(name) }

// ReadKey resolves a bundle by its EXACT resolution key (Catalog.LookupKey) —
// the load-path counterpart to Read for a caller that already holds a
// trust.BundleKey rather than an ask string. No search, no ambiguity.
func (l *Loader) ReadKey(key trust.BundleKey) (BundleRead, bool) {
	return l.Catalog().LookupKey(key)
}

// LoadKey reads a bundle by its EXACT resolution key. See ReadKey.
func (l *Loader) LoadKey(key trust.BundleKey) (*Bundle, error) {
	return l.Catalog().LoadKey(key)
}

// Load reads a bundle by name. See Catalog.Lookup for what an ask may be and
// which asks are refused rather than resolved.
func (l *Loader) Load(name string) (*Bundle, error) { return l.Catalog().Load(name) }

// Find locates the FILE backing a bundle. It exists for the two callers that
// need the path itself — deleting a bundle, and reporting whether a short name
// resolves — and refuses a bundle that has no file, because a synthetic path is
// not one.
func (l *Loader) Find(name string) (string, error) { return l.Catalog().Find(name) }

// The queries below hold nothing back for themselves: each one is answered by
// the resolved set (Catalog), and the loader's part is only to produce that
// set. A caller that already holds one should ask it directly.

func (l *Loader) ListAllFragments() ([]ContentInfo, error) { return l.Catalog().ListAllFragments() }
func (l *Loader) ListAllCommands() ([]ContentInfo, error)  { return l.Catalog().ListAllCommands() }
func (l *Loader) ListAllSkills() ([]SkillInfo, error)      { return l.Catalog().ListAllSkills() }
func (l *Loader) ListByTags(tags []string) ([]ContentInfo, error) {
	return l.Catalog().ByTags(tags)
}
func (l *Loader) ReadFragment(name string) ([]*ItemRead, error) {
	return l.Catalog().ReadFragment(name)
}
func (l *Loader) ReadCommand(name string) ([]*ItemRead, error) {
	return l.Catalog().ReadCommand(name)
}
func (l *Loader) ReadSkill(name string) ([]*LoadedSkill, error) { return l.Catalog().ReadSkill(name) }
func (l *Loader) ReadAllSkills() ([]*LoadedSkill, error)        { return l.Catalog().ReadAllSkills() }
func (l *Loader) ReadBundleCommands(bundleRef string) []*ItemRead {
	return l.Catalog().ReadBundleCommands(bundleRef)
}
func (l *Loader) ReadBundleSkills(bundleRef string) []*LoadedSkill {
	return l.Catalog().ReadBundleSkills(bundleRef)
}
func (l *Loader) ResolveFragmentAsk(name string) string { return l.Catalog().ResolveFragmentAsk(name) }

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
	// Name is the ASK name — what a listing shows and a user types back.
	// It is not unique: two bundles read from different sources may share it.
	Name string

	// Ref is the canonical URI, the unambiguous handle. It is what a user
	// copies when two rows share a Name, and the only spelling that resolves
	// exactly.
	Ref trust.BundleKey

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
