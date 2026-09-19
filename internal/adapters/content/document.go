package content

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// DocumentStore is a read-only Store over bundles that have NO FILESYSTEM: their
// contents arrive as a map of paths to bytes.
//
// This is the backend companion loadouts need. A companion is an independently
// released binary that ctxloom probes by running `<bin> loadout --format json`; the
// answer is ONE document, and there is no directory anywhere to walk. Before this,
// that shape was the standing objection to a tree format — "companion loadouts have
// no filesystem, so the bundle format must remain expressible as a single
// document". It does not: the same model with a different TRANSPORT is enough, and
// this is NOT a second format. Every registered SurfaceType, the candidate walker,
// the digest and the form partition are all reused unchanged; only where the bytes
// come from differs.
//
// # The boundary, stated deliberately
//
// This type takes BYTES, never a command line. Probing companions — discovery,
// exec, timeouts, the signed loadout envelope, the withhold-on-failure policy —
// lives in the companion layer and must stay there: it needs to exec processes and
// to parse a bundle document, and pulling either into this package would make the
// content layer depend on the things that are meant to depend on IT. The adapter
// is therefore a caller: it probes, produces a path->bytes map per companion, and
// hands it here.
//
// It deliberately does NOT implement Writer. A probed companion is a read-only
// source by construction — there is nothing to write back to.
type DocumentStore struct {
	inner *TreeStore
}

var _ Store = (*DocumentStore)(nil)

// DocumentBundle is one bundle's contents as bundle-relative paths to bytes, e.g.
// {"mcp/postgres.yaml": ..., "mcp/.postgres.meta.yaml": ...}.
type DocumentBundle map[string][]byte

// NewDocumentStore builds a read-only store from in-memory bundle contents.
//
// Paths are validated with the SAME rule the digest uses, so a path this store
// would accept but the digest could not encode is refused up front rather than at
// signing time.
func NewDocumentStore(bundles map[BundleID]DocumentBundle, prov Provenance) (*DocumentStore, error) {
	if err := prov.validate(); err != nil {
		return nil, err
	}
	const root = "/"
	fsys := afero.NewMemMapFs()
	// Sort so construction is deterministic and an error names the first offender
	// in a stable order rather than whichever map iteration happened to reach it.
	ids := make([]BundleID, 0, len(bundles))
	for id := range bundles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		if err := validateBundleID(id); err != nil {
			return nil, err
		}
		files := bundles[id]
		if len(files) == 0 {
			return nil, fmt.Errorf("%w: bundle %q has no contents", ErrNotFound, id)
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if err := validateDigestPath(p); err != nil {
				return nil, fmt.Errorf("bundle %q: %w", id, err)
			}
			target := filepath.Join(root, string(id), filepath.FromSlash(p))
			if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, fmt.Errorf("content: staging %q: %w", p, err)
			}
			// AllowEmpty: an individual companion-loadout file can legitimately
			// be zero bytes even though the bundle as a whole (len(files)==0,
			// checked above) may not be.
			if err := iox.WriteFileAtomicFs(fsys, target, files[p], 0o644, iox.AllowEmpty()); err != nil {
				return nil, fmt.Errorf("content: staging %q: %w", p, err)
			}
		}
	}
	inner, err := NewTreeStore(fsys, root, prov)
	if err != nil {
		return nil, err
	}
	return &DocumentStore{inner: inner}, nil
}

func (s *DocumentStore) Bundles(ctx context.Context) ([]BundleID, error) {
	return s.inner.Bundles(ctx)
}

func (s *DocumentStore) Open(ctx context.Context, id BundleID) (Bundle, error) {
	return s.inner.Open(ctx, id)
}
