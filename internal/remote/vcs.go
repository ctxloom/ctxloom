package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
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

	// ListItems returns the repo-relative item paths under
	// .ctxloom/content/<kind>/ at the source's CURRENT state, with the
	// .ctxloom/content/<kind>/ prefix and the .yaml suffix stripped (so
	// "lang/go/testing", not ".ctxloom/content/bundles/lang/go/testing.yaml").
	// A source with no such directory returns an empty list, not an error. Listing
	// past revisions — and surfacing items DELETED since — is the optional
	// Versioned capability (ListDeletedItems), not part of the minimal surface.
	ListItems(ctx context.Context, kind ItemType) ([]string, error)
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
	// ListDeletedItems returns item paths under ctxloom/<kind>/ that existed at
	// some past revision but are gone at the current state — content removed
	// upstream. Same path shape as VCS.ListItems (relative, suffix stripped). A
	// backend with no history of the kind returns an empty list. This is the
	// history counterpart to ListItems: ListItems sees what IS, ListDeletedItems
	// sees what WAS-but-isn't.
	ListDeletedItems(ctx context.Context, kind ItemType) ([]string, error)
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
		// U095-F17: some backends degrade to a Versioned-less VCS for a
		// specific, known reason (e.g. LocalGitVCSFactory falling back to
		// fsVCS when the enclosing directory isn't a usable git repo).
		// Surface that cause when available instead of only the generic
		// "does not support revisions" message.
		if dc, ok := vcs.(degradationCause); ok {
			if cause := dc.DegradationCause(); cause != nil {
				return nil, fmt.Errorf("source does not support revisions (limited to current/HEAD): cannot read %s@%s: %w", path, version, cause)
			}
		}
		return nil, fmt.Errorf("source does not support revisions (limited to current/HEAD): cannot read %s@%s", path, version)
	}
	return v.ReadFileAt(ctx, path, version)
}

// degradationCause is the OPTIONAL capability a VCS may implement to explain
// WHY it fell back to a lesser capability set (e.g. current-only instead of
// Versioned), so readItemAt's "no history" error can name the real cause
// instead of only a generic message. Probed by type assertion, mirroring
// Versioned/itemHistorySource/DeletedItemLister elsewhere in this file.
type degradationCause interface {
	// DegradationCause returns the error that caused the degradation, or nil
	// if the backend was never degraded.
	DegradationCause() error
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

// ListItems walks .ctxloom/content/<kind>/ in the repo tree at the default
// branch, recursing into subdirectories, and returns each .yaml item's path
// relative to that base (suffix stripped). A repo with no such directory
// lists empty.
func (v *gitForgeVCS) ListItems(ctx context.Context, kind ItemType) ([]string, error) {
	base := paths.RepoContentPrefix + "/" + kind.DirName()
	var items []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := v.fetcher.ListDir(ctx, v.owner, v.repo, dir, "")
		if err != nil {
			// A missing directory is not an error — the repo simply has no items
			// of this kind (or none under this subtree).
			if errors.Is(err, errs.ErrRemoteContentNotFound) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			full := dir + "/" + e.Name
			if e.IsDir {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			if strings.HasSuffix(e.Name, ".yaml") {
				rel := strings.TrimSuffix(strings.TrimPrefix(full, base+"/"), ".yaml")
				items = append(items, rel)
			}
		}
		return nil
	}
	if err := walk(base); err != nil {
		return nil, err
	}
	sort.Strings(items)
	return items, nil
}

// itemHistorySource is the optional history capability a forge Fetcher backend
// may provide: the local-clone GitCloneFetcher walks go-git history, while the
// forge-API fetcher cannot. gitForgeVCS probes for it by type assertion.
type itemHistorySource interface {
	ListDeletedItems(ctx context.Context, kind ItemType) ([]string, error)
}

// ListDeletedItems surfaces items removed upstream when the underlying fetcher
// can walk history (the local clone); otherwise it returns nothing, since a
// backend without history cannot know what was deleted.
func (v *gitForgeVCS) ListDeletedItems(ctx context.Context, kind ItemType) ([]string, error) {
	hist, ok := v.fetcher.(itemHistorySource)
	if !ok {
		return nil, nil
	}
	return hist.ListDeletedItems(ctx, kind)
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

// ListItems walks <root>/<kind>/ on the filesystem and returns each .yaml
// item's path relative to that base (suffix stripped). A root without the
// directory lists empty. Current working set only — fsVCS has no history.
//
// U095-F03: "cannot tell if the directory exists" (a stat failure — the
// unreadable/permission-denied case) and "the directory genuinely does not
// exist" are DIFFERENT facts. Local content is auto-trusted (ctxloom:local,
// EffectiveTrust step 3), so collapsing a permission failure into "no items"
// silently hides content that should have been found — success reported
// regardless — rather than surfacing that the listing could not actually
// run. Only a genuine, cleanly-answered "not there" degrades to empty.
func (v *fsVCS) ListItems(_ context.Context, kind ItemType) ([]string, error) {
	base := filepath.Join(v.root, kind.DirName())
	exists, err := afero.DirExists(v.fs, base)
	if err != nil {
		return nil, fmt.Errorf("check %s exists: %w", base, err)
	}
	if !exists {
		return nil, nil
	}
	var items []string
	walkErr := afero.Walk(v.fs, base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", p, err)
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil {
			return fmt.Errorf("relativize %s under %s: %w", p, base, relErr)
		}
		items = append(items, strings.TrimSuffix(filepath.ToSlash(rel), ".yaml"))
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(items)
	return items, nil
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

// localGitVCS is a working-copy VCS over a directory that lives INSIDE a git
// project (the committed .ctxloom/content/ tree). It reads the directory's CURRENT
// state from the filesystem exactly like fsVCS (embedded), but because the
// enclosing project is itself under git it ALSO satisfies Versioned: a pinned
// read returns the file's bytes as of a revision in the PROJECT'S OWN history —
// the local equivalent of `git show <rev>:<path>`. That is precisely what
// "version" means for project-authored ctxloom:local content: the surrounding
// project's VCS state, which you are already inside.
//
// The historical read is inherently OS-backed (go-git opens the on-disk .git),
// so it does NOT honor the embedded fsVCS's afero.Fs; that fs backs only the
// current-state reads (ReadFile/ListItems), matching fsVCS.
type localGitVCS struct {
	fsVCS                    // current-state ReadFile + ListItems (filesystem)
	repo     *git.Repository // the enclosing project repository
	repoRoot string          // its worktree root, for repo-relative path lookups
}

// repoRelPath rebases a local-content-relative path (e.g. "bundles/x.yaml",
// relative to the local-content root) onto the project repo root and returns it
// as a forward-slash, repo-relative path — the shape a go-git tree lookup wants.
func (v *localGitVCS) repoRelPath(path string) (string, error) {
	abs := filepath.Join(v.root, filepath.FromSlash(path))
	rel, err := filepath.Rel(v.repoRoot, abs)
	if err != nil {
		return "", fmt.Errorf("locate %s under repo root %s: %w", abs, v.repoRoot, err)
	}
	return filepath.ToSlash(rel), nil
}

// ReadFileAt returns path's bytes as of git revision rev in the project repo.
// path is relative to the local-content root; it is rebased onto the repo root
// before the tree lookup. A rev that won't resolve, or a path absent at that rev,
// errors (fail-closed) — the caller withholds rather than serving a wrong or
// current-state version.
func (v *localGitVCS) ReadFileAt(_ context.Context, path, rev string) ([]byte, error) {
	repoPath, err := v.repoRelPath(path)
	if err != nil {
		return nil, err
	}
	hash, err := v.repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return nil, fmt.Errorf("resolve revision %q: %w", rev, err)
	}
	commit, err := v.repo.CommitObject(*hash)
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree at %s: %w", rev, err)
	}
	file, err := tree.File(repoPath)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, fmt.Errorf("%s not present at %s: %w", repoPath, rev, errs.ErrRemoteContentNotFound)
		}
		return nil, fmt.Errorf("read %s at %s: %w", repoPath, rev, err)
	}
	contents, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", repoPath, rev, err)
	}
	return []byte(contents), nil
}

// ListDeletedItems is the history-walk capability of Versioned. Project-authored
// local content has no "upstream" to be removed from — the project IS the source —
// so it surfaces no deletions, implemented only to satisfy Versioned alongside the
// load-bearing ReadFileAt/ResolveRevision.
func (v *localGitVCS) ListDeletedItems(_ context.Context, _ ItemType) ([]string, error) {
	return nil, nil
}

var (
	_ VCS       = (*localGitVCS)(nil)
	_ Versioned = (*localGitVCS)(nil)
)

// LocalGitVCSFactory builds a VCSFactory for project-authored ctxloom:local
// content that adds revision history + pinning WHEN the project is itself under
// git — the "working-copy VCS" the local RefFetcher's doc anticipates. For each
// opened source (loc = the committed local-content root) it locates the enclosing
// git repository:
//
//   - inside a git repo  → a Versioned localGitVCS, so a pinned ref reads the
//     item bytes at that project revision;
//   - not a git repo, or git won't open → a plain current-only fsVCS, so a pinned
//     read fails closed via readItemAt (the historical fetch is withheld) while
//     the unversioned working-copy path keeps working.
//
// Like the remote clone cache, the historical read is OS-backed (go-git opens the
// real .git), so it does not honor fs; fs backs only the current-state fallback,
// matching FSVCSFactory.
func LocalGitVCSFactory(fs afero.Fs) VCSFactory {
	return func(loc string) (VCS, error) {
		base := fsVCS{fs: fs, root: loc}
		repo, err := git.PlainOpenWithOptions(loc, &git.PlainOpenOptions{DetectDotGit: true})
		if err != nil {
			// Not under version control (or unreadable .git): degrade to
			// current-only. A pinned read then fails closed via readItemAt,
			// which now (U095-F17) surfaces this cause instead of discarding
			// it.
			return &degradedFsVCS{fsVCS: base, cause: fmt.Errorf("open enclosing git repository: %w", err)}, nil
		}
		wt, err := repo.Worktree()
		if err != nil {
			return &degradedFsVCS{fsVCS: base, cause: fmt.Errorf("get worktree of enclosing git repository: %w", err)}, nil
		}
		return &localGitVCS{fsVCS: base, repo: repo, repoRoot: wt.Filesystem.Root()}, nil
	}
}

// degradedFsVCS is a current-only fsVCS that remembers WHY it lost the
// Versioned capability it would otherwise have had (LocalGitVCSFactory could
// not open the enclosing git repo, or could not get its worktree). It
// satisfies degradationCause so readItemAt's error names the real cause
// instead of a generic "does not support revisions" message (U095-F17).
type degradedFsVCS struct {
	fsVCS
	cause error
}

// DegradationCause returns the error that caused the fallback to current-only.
func (v *degradedFsVCS) DegradationCause() error { return v.cause }

var _ degradationCause = (*degradedFsVCS)(nil)

// ClonedRepoVCSFactory opens a VCS over the repository's EXISTING local clone in
// the cache, reading through go-git with zero network. Unlike the cached
// FetcherFactory (which clones on first touch via EnsureRef), this NEVER fetches:
// if the clone is absent, git.PlainOpen fails and the factory returns an error,
// so a not-yet-downloaded remote is skipped (and warned) rather than silently
// materialized. This is the backend for listing/reads that must only see what is
// already downloaded.
func ClonedRepoVCSFactory(cache *RepoCache) VCSFactory {
	return func(loc string) (VCS, error) {
		forgeType, _, err := DetectForge(loc)
		if err != nil {
			return nil, fmt.Errorf("detect forge for %s: %w", loc, err)
		}
		owner, repo, err := ParseOwnerRepo(loc)
		if err != nil {
			return nil, fmt.Errorf("parse repo URL %s: %w", loc, err)
		}
		// PlainOpen of the cache dir — no clone/fetch. Absent clone → error.
		repoDir, err := cache.RepoDirForURL(loc)
		if err != nil {
			return nil, fmt.Errorf("resolve cache dir for %s: %w", loc, err)
		}
		fetcher, err := NewGitCloneFetcher(repoDir, normalizeCloneURL(loc), forgeType, nil)
		if err != nil {
			return nil, err
		}
		return &gitForgeVCS{fetcher: fetcher, owner: owner, repo: repo}, nil
	}
}

// GitForgeVCSFactory adapts a git-forge FetcherFactory into a VCSFactory: each
// opened VCS is bound to the repository identified by loc (a repo URL) and
// reads through the forge Fetcher. This is the git backend wiring for remote
// references. The factory MUST be a cached factory in production so a repo is
// cloned once and never re-fetched per call — see operations.NewCachedFetcherFactory.
func GitForgeVCSFactory(factory FetcherFactory, auth AuthConfig) VCSFactory {
	return func(loc string) (VCS, error) {
		owner, repo, err := ParseOwnerRepo(loc)
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
