package remote

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
)

// VCS abstracts reads from a single version-controlled source — one repository
// or working copy. It is deliberately neutral across version-control systems:
// git is the only backend today, but the surface assumes nothing git-specific,
// so an hg or svn backend could satisfy it. A VCS instance is bound to one
// source; callers address content by path.
//
// VCS itself is the MINIMAL capability every backend must provide: read the
// source's CURRENT state. Revision history and pinning are an OPTIONAL extra
// (see Versioned). A backend that offers only VCS is limited to current/HEAD —
// which is fine: local content in particular is usually versionless, because
// its "version" is just the surrounding project's own VCS state, which you are
// already inside.
type VCS interface {
	// ReadFile returns path's bytes at the source's current state — a remote's
	// default-branch tip, or a local working copy. Always supported.
	ReadFile(ctx context.Context, path string) ([]byte, error)
}

// Versioned is the OPTIONAL VCS capability for addressing past revisions and
// pinning. A backend that implements it can read content at an arbitrary
// revision and resolve a symbolic revision to a concrete, recordable id; a
// backend that does NOT is limited to current/HEAD through VCS.ReadFile.
//
// Callers probe for it with a type assertion (`v, ok := vcs.(Versioned)`),
// exactly like io.ReaderAt extends io.Reader. Without it you are stuck at
// current — not an error in itself, only an error if a caller actually asks
// for a pinned/historical revision (see readItemAt).
//
// "Revision" is an opaque, backend-defined string (a git SHA/tag, an hg
// changeset, an svn rev). The interface does not interpret it.
type Versioned interface {
	// ReadFileAt reads path as of rev (concrete or symbolic).
	ReadFileAt(ctx context.Context, path, rev string) ([]byte, error)
	// ResolveRevision resolves a symbolic rev (tag/branch) to a concrete,
	// pinnable id.
	ResolveRevision(ctx context.Context, rev string) (string, error)
}

// readItemAt reads path from vcs at version, routing the read by capability:
//   - empty version → the source's current state via VCS.ReadFile (always works);
//   - non-empty version → the optional Versioned capability.
//
// When a pinned/historical read is requested of a backend that has no Versioned
// support, it returns a clear "stuck at current" error rather than silently
// serving HEAD. This is the single home for version routing, shared by every
// RefFetcher scheme so the policy is not duplicated per backend.
func readItemAt(ctx context.Context, vcs VCS, path, version string) ([]byte, error) {
	if version == "" {
		return vcs.ReadFile(ctx, path)
	}
	v, ok := vcs.(Versioned)
	if !ok {
		return nil, fmt.Errorf("source does not support revisions (limited to current/HEAD): cannot read %s@%s", path, version)
	}
	return v.ReadFileAt(ctx, path, version)
}

// VCSFactory opens a VCS handle for the source located at loc — a repository
// URL for a remote source, a working-copy path for a local one. It is the
// general seam through which a RefFetcher obtains its backend without knowing
// which version-control system (or transport) is in play.
type VCSFactory func(loc string) (VCS, error)

// gitForgeVCS adapts the git-forge Fetcher (bound to one owner/repo) to the
// VCS interface, including the optional Versioned capability — git always
// supports history. Reads and revision resolution delegate to the Fetcher.
type gitForgeVCS struct {
	fetcher     Fetcher
	owner, repo string
}

// ReadFile reads path at the repo's default branch (empty git ref).
func (v *gitForgeVCS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return v.fetcher.FetchFile(ctx, v.owner, v.repo, path, "")
}

// ReadFileAt reads path at the given git revision (tag/branch/SHA).
func (v *gitForgeVCS) ReadFileAt(ctx context.Context, path, rev string) ([]byte, error) {
	return v.fetcher.FetchFile(ctx, v.owner, v.repo, path, rev)
}

// ResolveRevision resolves a tag/branch to its commit SHA via the forge.
func (v *gitForgeVCS) ResolveRevision(ctx context.Context, rev string) (string, error) {
	return v.fetcher.ResolveRef(ctx, v.owner, v.repo, rev)
}

var (
	_ VCS       = (*gitForgeVCS)(nil)
	_ Versioned = (*gitForgeVCS)(nil)
)

// fsVCS is a filesystem-backed VCS: it reads files under a fixed root directory
// from an afero.Fs, at the source's current state only. It implements the core
// VCS but deliberately NOT Versioned — a plain filesystem has no revision
// history. This is the simplest local backend: content not under version
// control, or whose only "version" is the surrounding project's working copy.
// A pinned/historical read against it fails cleanly via readItemAt.
type fsVCS struct {
	fs   afero.Fs
	root string
}

// ReadFile reads path (relative to root) from the filesystem.
func (v *fsVCS) ReadFile(_ context.Context, path string) ([]byte, error) {
	full := filepath.Join(v.root, filepath.FromSlash(path))
	data, err := afero.ReadFile(v.fs, full)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", full, err)
	}
	return data, nil
}

var _ VCS = (*fsVCS)(nil)

// FSVCSFactory builds a VCSFactory over an afero.Fs: each opened VCS reads files
// under the directory passed as loc. Current-state only — the returned VCS does
// not implement Versioned, so pinned reads error per readItemAt. This is the
// minimal local backend; the local RefFetcher (ctxloom:local) will pass the
// committed local content dir as loc.
func FSVCSFactory(fs afero.Fs) VCSFactory {
	return func(loc string) (VCS, error) {
		return &fsVCS{fs: fs, root: loc}, nil
	}
}

// GitForgeVCSFactory adapts a git-forge FetcherFactory into a VCSFactory: each
// opened VCS is bound to the repository identified by loc (a repo URL) and
// reads through the forge Fetcher. This is the git backend wiring for remote
// references. The factory MUST be a cached factory in production so a repo is
// cloned once and never re-fetched per call — see operations.NewCachedFetcherFactory.
func GitForgeVCSFactory(factory FetcherFactory, auth AuthConfig) VCSFactory {
	return func(loc string) (VCS, error) {
		owner, repo, err := ParseRepoURL(loc)
		if err != nil {
			return nil, fmt.Errorf("parse repo URL %s: %w", loc, err)
		}
		fetcher, err := factory(loc, auth)
		if err != nil {
			return nil, fmt.Errorf("open fetcher for %s: %w", loc, err)
		}
		return &gitForgeVCS{fetcher: fetcher, owner: owner, repo: repo}, nil
	}
}
