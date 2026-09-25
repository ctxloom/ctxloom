package operations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// lockfileFSOptions threads the config's injected filesystem into a remote
// lockfile manager so lockfile reads honor it alongside the registry reads.
func lockfileFSOptions(cfg *config.Config) []remote.LockfileOption {
	if fs := cfg.FS(); fs != nil {
		return []remote.LockfileOption{remote.WithLockfileFS(fs)}
	}
	return nil
}

// treeBundleReaders builds one reader per lockfile entry, over the tree
// `deps pull` installed.
//
// The set is every entry in the lockfile: format v2 publishes ONLY trees, so
// there is no longer a per-pin fact to filter on (that was LockEntry.Tree,
// removed with format v1 — every bundle is directory-form now). This makes
// the sibling read path in remoteBundleReaders (remote.LoadAllBytes over a
// BundleReader built with no tree fetcher) permanently refuse every entry it
// is handed; treeBundleReaders is what actually resolves all of them, by
// clearing each refusal below rather than by a positive selection.
//
// Any byte-source failure recorded against an entry is therefore CLEARED once
// its reader is built here: it described a road not taken. A failure left in
// the map after this runs is a REAL failure — nothing else claims it.
//
// A tree whose directory cannot even be opened REPLACES that entry's failure
// with its own, so the user is told what actually went wrong. A tree that opens
// but does not match what its publisher signed is the READER's answer, not this
// function's: it is a fact about bytes, established where the bytes are read.
func treeBundleReaders(cfg *config.Config, lock *remote.Lockfile, root trust.TrustRoot, failures map[string]error) []bundles.Reader {
	trees := make([]string, 0, len(lock.Bundles))
	for canonical := range lock.Bundles {
		trees = append(trees, string(canonical))
	}
	sort.Strings(trees) // deterministic reader order across runs

	var out []bundles.Reader
	for _, canonical := range trees {
		reader, err := treeBundleReader(cfg, canonical, lock.Bundles[trust.BundleKey(canonical)], root)
		if err != nil {
			failures[canonical] = err
			continue
		}
		out = append(out, reader)
		delete(failures, canonical)
	}
	return out
}

// treeBundleReader points a pinned-tree reader at the worktree `deps pull`
// checked out for one lockfile entry.
//
// WHY THE INSTALLED TREE AND NOT THE CLONE AT THE PINNED SHA — walking the
// tree in the git clone at entry.SHA is the obvious alternative. Two things
// rule it out:
//
//   - a bundle's SKILLS are files on disk. bundles.Bundle.FSDir has to return a
//     real directory or a skill package is unloadable (it refuses the synthetic
//     "<remote>:…" path outright), and the checked-out worktree is the only real
//     directory a tree bundle has. That is what WithInstalledDir carries.
//   - verifying the installed tree is STRICTLY STRONGER than trusting the pin.
//     The reader checks the publisher's signature over the manifest AND the tree
//     against that manifest in both directions, so an edit to the installed
//     cache is caught. The pin cannot see the cache at all.
//
// The pin is not thereby abandoned: it is what `deps pull` fetched at, and it
// still decides WHICH bytes were installed. What changed is that integrity is
// now checked where the bytes are actually read from.
//
// The tree is rooted at the bundle directory's PARENT: a bundle id must be a
// single path segment (content.validateBundleID), and the rest of the bundle's
// repository path is absorbed by the root rather than smuggled into the id.
// That parent is inside the worktree, because a sparse checkout lays the bundle
// out at its repository path — see Reference.LocalTreePath.
func treeBundleReader(cfg *config.Config, canonical string, entry remote.LockEntry, root trust.TrustRoot) (bundles.Reader, error) {
	if len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	dir, err := treeBundleDir(cfg.GetAppPaths()[0], canonical)
	if err != nil {
		return nil, err
	}
	// Check the installed tree is THERE before handing a reader a root it will
	// fail to list later. The two failures are the same fact, but only here is
	// the fix knowable: a lockfile entry with no tree on disk is a pull that has
	// not happened, and the message has to say so — "cannot list this directory"
	// reaches the user as a bug in ctxloom.
	if ok, derr := afero.DirExists(getFS(cfg.FS()), dir); derr != nil || !ok {
		return nil, fmt.Errorf("the lockfile records %q as a directory-form bundle but its tree is not installed at %s "+
			"(run `ctxloom deps pull`)", canonical, dir)
	}
	tree, err := content.NewAferoTreeFS(getFS(cfg.FS()), filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("the tree installed for %q at %s cannot be opened: %w", canonical, dir, err)
	}
	return bundles.NewRepoFSReader(tree, canonical,
		bundles.WithTrustRoot(root),
		bundles.WithReaderReporter(cfg.Reporter()),
		bundles.WithInstalledDir(dir),
		bundles.WithPinnedRevision(entry.SHA),
		bundles.WithRepoURL(entry.URL)), nil
}

// treeBundleDir resolves the directory `deps pull` checked a tree bundle out
// into, from its canonical lockfile key. It goes through the same
// Reference.LocalTreePath the installer used rather than re-assembling the path,
// so a layout change cannot make the writer and the reader disagree.
func treeBundleDir(baseDir, canonical string) (string, error) {
	ref, err := remote.ParseReference(canonical)
	if err != nil {
		return "", fmt.Errorf("invalid lockfile bundle key %q: %w", canonical, err)
	}
	if !ref.IsCanonical() {
		return "", fmt.Errorf("invalid lockfile bundle key %q: not a canonical ref", canonical)
	}
	return ref.LocalTreePath(baseDir)
}

// reportBundleLoadFailures records one fatal-class finding per lockfile-active
// bundle whose bytes could not be read.
//
// Fatal-class in strict mode because the user PINNED these: content silently
// missing from a session is exactly the failure fail-loudly exists to catch. It
// warns and continues in degraded mode.
//
// A WITHHELD tree is reported differently, and deliberately: its bytes are on
// disk and re-pulling would fetch the same ones, so the default fix cannot fix
// it. It is also not a delivery problem at all — the content disagrees with what
// its publisher signed — so it is classed as a trust failure rather than a
// delivery one. This is the only path on which installed remote bytes can be
// refused for disagreeing with their signature. A fix line that cannot fix the
// thing it is attached to is worse than no fix line at all.
func reportBundleLoadFailures(failures map[string]error) {
	for name, err := range failures {
		if errors.Is(err, bundles.ErrTreeBundleWithheld) {
			strictness.FailOnce(strictness.ClassTrust, withheldRemedy(err),
				"remote bundle %q was installed but withheld: %v", name, err)
			continue
		}
		strictness.FailOnce(strictness.ClassBundle, "ctxloom deps pull (or remove the bundle from its profiles)",
			"failed to load remote bundle %q from cache: %v", name, err)
	}
}

// The fix lines a withheld tree can carry. The withhold itself is decided by
// the reader; these only choose what to tell the user about it.
const (
	remedyWithheldTampered = "re-pull the bundle, or investigate the source — the installed tree does not match the manifest its publisher signed"
	// A pin at a commit signed in the retired format stays withheld for as long
	// as the pin does, and `deps pull` keeps the pin — so only an upgrade, which
	// moves it to the publisher's re-signed commit, can fix it.
	remedyWithheldSuperseded = "ctxloom deps upgrade — the pinned commit predates its publisher's re-sign in the current manifest format, and `deps pull` keeps the pin"
)

// withheldRemedy selects the fix line for a withheld tree from the error's
// typed cause.
func withheldRemedy(err error) string {
	if errors.Is(err, content.ErrManifestSuperseded) {
		return remedyWithheldSuperseded
	}
	return remedyWithheldTampered
}

// remoteBundleReaders builds one pinned-tree reader per lockfile-listed bundle:
// the bytes come from the tree `deps pull` installed, and each reader does its
// OWN signature checking over exactly those bytes.
//
// Canonical refs are the sole resolution identity: profiles author canonical
// refs and resolve straight to these readers' content, so each reader is
// constructed FOR one canonical ref rather than discovering names from paths.
//
// Returns nil when there is no lockfile or registry — no remote bundles, just
// the project's own.
func RemoteBundleReaders(cfg *config.Config) []bundles.Reader {
	if len(cfg.GetAppPaths()) == 0 {
		return nil
	}
	// PINS RIDE A PROJECT, NEVER HOME. When findAppDir fell back to
	// ~/.ctxloom there is no project, and a lockfile there pins a closure
	// nothing declares — home config carries settings (llm configs,
	// delegation), not bundles or profiles.
	//
	// This is not symmetry with the config chain, and deliberately so: config
	// LAYERS because settings merge sensibly, but two dependency closures do
	// not merge — their union is a set neither side asked for. So home may
	// supply CONFIG; only a project supplies a CLOSURE.
	//
	// It is also the only way this state can stay true. `deps` writes at
	// project scope and would never revisit a home lock, so a home lock is
	// read-but-never-written — and that always rots. The one on this machine
	// pinned 33 bundles against ZERO declarations for six weeks, until both
	// pinned revisions stopped parsing against the current bundle schema and
	// every project-less launch aborted on findings no supported command
	// could clear. Refusing to read it here is what makes the reader agree
	// with `deps check`, which already reports nothing installed there.
	if cfg.Source() == config.SourceHome {
		return nil
	}
	baseDir := cfg.GetAppPaths()[0]

	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir), remote.WithRegistryFS(cfg.FS()))
	if err != nil {
		// A real error here (corrupt remotes.yaml, unreadable dir) is not "no
		// remotes registered" — the doc comment's nil-return case above — so it
		// fails loud instead of silently vanishing every lockfile-pinned remote
		// bundle from assembly/hooks/MCP/commands.
		strictness.FailOnce(strictness.ClassBundle, "check the remotes registry under .ctxloom, or re-run `ctxloom remote add`",
			"failed to open the remotes registry; no remote bundles loaded: %v", err)
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir, lockfileFSOptions(cfg)...).Load()
	if err != nil {
		strictness.FailOnce(strictness.ClassBundle, "run `ctxloom deps pull` to regenerate the lockfile, or fix it by hand",
			"failed to load the remote lockfile; no remote bundles loaded: %v", err)
		return nil
	}
	if lock.IsEmpty() {
		return nil
	}
	// Auth config and the git clone cache are inherently OS-backed (the cache
	// shells out to git), so they intentionally do not honor cfg.FS().
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	// Wrap in the caching decorator so repeated loader constructions within a
	// session don't re-walk the clone for the same SHAs.
	reader := remote.NewCachingBundleReader(remote.NewBundleReader(registry, factory, auth, lock))

	ctx := context.Background()
	_, failures := remote.LoadAllBytes(ctx, reader)

	// The trust root (embedded + user + project allowed_signers) is resolved once
	// for the whole set and handed to every reader, so no two pinned bundles are
	// judged against different roots.
	root := cfg.Trust().Root()

	// EVERY remote bundle is a TREE, so treeBundleReaders is the whole set.
	//
	// Presenting a tree's bundle.yaml as a lone document drops the items beside it — the
	// fragments, skills and prompts that live as FILES in the tree — while
	// checking a signature over the manifest alone rather than over the tree.
	// That is not hypothetical: leaving the loop unguarded is exactly what made
	// a published fragment stop reaching the consumer's assistant while every
	// other surface kind still arrived.
	out := treeBundleReaders(cfg, lock, root, failures)
	reportBundleLoadFailures(failures)
	return out
}

// bundleVersionResolver returns a bundles.BundleVersionResolver that materializes
// a bundle at a specific commit and parses the bytes into a Bundle. It dispatches
// by the ref's SOURCE — the loader's multi-version coexistence backed end to end:
//
//   - remote/canonical ref → the whole pinned TREE out of the local git clone
//     cache (bundles.ReadRemoteRef), verified before it is interpreted;
//   - ctxloom:local ref → the file's bytes as of <commit> in the PROJECT'S OWN
//     git history (the committed .ctxloom/content/ tree), via the local working-copy
//     VCS — `git show <commit>:<path>` semantics. The unversioned local path is
//     untouched: the loader only invokes the resolver for an explicit "@<commit>".
//
// Given a version-less canonical ref and an opaque commit, it reads exactly that
// historical version. Returns nil when there is no app dir to anchor either
// source. The fetch is lazy — nothing happens until a version-aware loader method
// actually requests a pinned commit — and any failure (unknown rev, non-git
// project, path-absent-at-rev) fails closed: the caller withholds just that item.
//
// Auth and both git backends are inherently OS-backed (the remote cache shells
// out to git; the local backend opens the on-disk project .git), so they do not
// honor cfg.FS() — matching loadRemoteBundleSeed.
func BundleVersionResolver(cfg *config.Config) bundles.BundleVersionResolver {
	if len(cfg.GetAppPaths()) == 0 {
		return nil
	}
	baseDir := cfg.GetAppPaths()[0]
	// Defer the auth read + clone-cache construction to the FIRST actual remote
	// version fetch: the default (lockfile) path never invokes the resolver, and a
	// local-only pin never touches the remote cache, so neither pays for it.
	var (
		once    sync.Once
		factory remote.FetcherFactory
		auth    remote.AuthConfig
	)
	return func(canonicalRef, commit string, root trust.TrustRoot) (*bundles.Bundle, error) {
		ref, err := remote.ParseReference(canonicalRef)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", canonicalRef, err)
		}

		// Local (project-authored) refs version against the PROJECT'S own git
		// history, not the remote clone cache. The committed .ctxloom/content/ tree
		// is read at <commit> through the working-copy VCS; a non-git project,
		// unknown rev, or path-absent-at-rev errors here and the caller withholds.
		if ref.IsLocal {
			data, err := remote.NewLocalRefFetcher(
				remote.LocalGitVCSFactory(afero.NewOsFs()),
				paths.LocalPath(baseDir),
			).FetchItem(context.Background(), ref, commit)
			if err != nil {
				return nil, err
			}
			return bundles.ParseBundle(data)
		}

		// Remote/canonical refs: FetchItem over the local clone cache (auth +
		// cache built once, lazily, on the first remote pin).
		once.Do(func() {
			auth = remote.LoadAuth(baseDir)
			cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
			factory = remote.NewCachedFetcherFactory(cache)
		})
		// The WHOLE tree, not its manifest: a tree bundle's fragments, commands
		// and skills are FILES beside its bundle.yaml, so reading the manifest
		// alone resolved every @<commit>-pinned tree bundle to a bundle with
		// zero items — the real product bundle, silently empty.
		b, _, err := bundles.ReadRemoteRef(context.Background(), factory, auth, ref, commit, remotetree.PullTreeFetcher, root)
		return b, err
	}
}

// projectReader is the one-off local reader an operation opens to touch a
// single bundle by name outside a config generation; its read-time findings
// render through the process's diagnostic sink.
func projectReader(fs afero.Fs, dirs []string) bundles.Reader {
	return bundles.NewProjectReader(fs, dirs, bundles.WithReaderReporter(strictness.Sink("ctxloom")))
}
