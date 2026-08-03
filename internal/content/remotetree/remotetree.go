// Package remotetree is the pinned-remote backend for the content access
// surface: a read-only content.Store over a bundle tree living in a git
// repository at one commit SHA.
//
// # It is the backend the L0 interface exists for
//
// The other three backends all sit on a filesystem of some kind — an authored
// tree, an embedded fs.FS, an extracted archive. This one does not. A remote
// tree is BYTES REACHED BY PATH: a listing call and a read call against a
// forge, with no directory handle, no stat and no fs.File anywhere. That is why
// content.TreeFS is two methods and why nothing in the walker takes an
// afero.Fs. This package implements those two methods and inherits the entire
// traversal — stem grouping, sidecar merging, the fail-loud unclaimed-file
// rule, the digest — without a line of walking of its own. If it had needed a
// second walker, the seam would have been wrong.
//
// # Fetch-then-serve, not read-through
//
// The tree is fetched ONCE at construction and served from memory afterwards,
// the same shape the archive backend uses and for a sharper reason: a signature
// covers the exact bytes of a specific tree, and a store that re-read the forge
// per access could enumerate one tree and digest another. A commit SHA is
// immutable, so a snapshot of it is not a staleness risk; it is the only way to
// be sure the bytes that were counted are the bytes that were served.
//
// # It verifies nothing
//
// Layer 0 knows only where bytes live. This package fetches them, bounds them
// and refuses to lie about where they came from — it stamps remote provenance,
// never local — and it says nothing whatsoever about whether any signature over
// them verifies. That question belongs to layer 2.
package remotetree

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// The package's error vocabulary. Callers match with errors.Is.
var (
	// ErrNotPinned reports a spec whose ref is not a full commit SHA.
	ErrNotPinned = errors.New("content/remotetree: not pinned to a commit SHA")
	// ErrEmptyTree reports a content root that exists and holds no files.
	ErrEmptyTree = errors.New("content/remotetree: the pinned tree has no files")
	// ErrTooLarge reports a tree that exceeded one of the fetch budgets.
	ErrTooLarge = errors.New("content/remotetree: the pinned tree exceeds its fetch budget")
)

// fullSHA matches a complete 40-character hex commit SHA and nothing else.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Spec identifies one pinned content tree.
type Spec struct {
	// Owner and Repo name the repository, as the Fetcher understands them.
	Owner string
	Repo  string

	// SHA is a FULL 40-hex commit SHA. A branch or tag name is refused with
	// ErrNotPinned, and that refusal is the point of this type.
	//
	// A mutable ref can be repointed between the listing that decided what the
	// tree contains and the reads that produced its bytes, so the tree a digest
	// covers need not be the tree that was served — with no error anywhere,
	// because every individual call succeeded. Resolving a ref to a SHA is the
	// caller's job (Fetcher.ResolveRef) precisely so that the moment a pin
	// stops being a pin is visible in the caller's code rather than buried
	// here.
	SHA string

	// Root is the repository-relative path of the content root whose immediate
	// subdirectories are bundles. Empty means the repository root.
	Root string

	// RepoURL is the provenance stamped onto every ref. It is REQUIRED: a ref
	// with no origin would be neither local, builtin nor remote, and
	// content.Provenance refuses it rather than defaulting — a store must never
	// claim an origin it was not given.
	RepoURL string

	// Limits bounds the fetch. The zero value uses the defaults.
	Limits Limits
}

// Limits bounds how much of a publisher-described tree will be fetched.
//
// The listing that says how big the tree is comes FROM THE PUBLISHER, so its
// size claim is exactly as trustworthy as they are. Without a budget, a hostile
// or simply broken listing turns one pull into an unbounded fetch loop. This is
// the same argument HardenedExtract makes about archive entries, applied to the
// one transport that has no archive to inspect up front.
type Limits struct {
	// MaxFiles caps how many files are fetched. <=0 uses DefaultMaxFiles.
	MaxFiles int
	// MaxBytes caps the total fetched bytes. <=0 uses DefaultMaxBytes.
	MaxBytes int64
	// MaxDepth caps directory nesting below the content root. <=0 uses
	// DefaultMaxDepth.
	MaxDepth int
}

// The default budgets. They deliberately match the archive backend's, because
// the same bundle arriving as a tarball and as a git tree must not be subject
// to two different size policies — a publisher would simply pick the looser
// transport.
const (
	// DefaultMaxFiles caps the number of files fetched from one tree.
	DefaultMaxFiles = 4096
	// DefaultMaxBytes caps total fetched bytes.
	DefaultMaxBytes int64 = 30 * 1024 * 1024
	// DefaultMaxDepth caps directory nesting below the content root. A skill
	// package's references/ or scripts/ subtree is legitimately several levels
	// down; nothing legitimate is anywhere near this.
	DefaultMaxDepth = 16
)

func (l Limits) withDefaults() Limits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxFiles
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// New fetches the tree at spec.SHA and serves it as a read-only content.Store.
//
// The returned store is a plain *content.FSStore — this package adds a
// transport, not a second store implementation, and there is deliberately no
// wrapper type here to accumulate behaviour that the other backends would then
// not have.
func New(ctx context.Context, f remote.Fetcher, spec Spec) (*content.FSStore, error) {
	if f == nil {
		return nil, errors.New("content/remotetree: nil fetcher")
	}
	if !fullSHA.MatchString(spec.SHA) {
		return nil, fmt.Errorf("%w: %q is not a full 40-hex commit SHA", ErrNotPinned, spec.SHA)
	}
	if spec.RepoURL == "" {
		return nil, errors.New("content/remotetree: spec has no repo URL, so its content would claim no origin")
	}
	files, err := FetchFiles(ctx, f, spec)
	if err != nil {
		return nil, err
	}
	tfs, err := content.NewMapTreeFS(bytesOf(files))
	if err != nil {
		return nil, err
	}
	return content.NewFSStore(tfs, content.Provenance{RepoURL: spec.RepoURL})
}

// Fetch materializes the pinned tree as store-relative paths to bytes.
//
// It is exported separately from New because the bytes are what a caller
// verifying a published tree needs to hold — the manifest check and the
// signature check both run over exactly this map — and re-deriving it by
// walking the store back out would be a second fetch of the same commit.
func Fetch(ctx context.Context, f remote.Fetcher, spec Spec) (map[string][]byte, error) {
	files, err := FetchFiles(ctx, f, spec)
	if err != nil {
		return nil, err
	}
	return bytesOf(files), nil
}

// FetchFiles is Fetch with the publisher's exec bit still attached.
//
// The two exist separately because their callers want genuinely different
// things. The content layer wants BYTES: a store enumerates items, digests them
// and hands them to a signature check, and a mode is not part of any of that.
// An INSTALLER wants the file as published — a skill package ships scripts the
// model runs on its own, and a 0755 script that lands 0644 is delivered content
// the agent cannot use, with nothing anywhere reporting a failure.
//
// Only the exec bit is carried, because only the exec bit exists: git records a
// blob as 100644 or 100755 and nothing else (see remote.TreeFile).
func FetchFiles(ctx context.Context, f remote.Fetcher, spec Spec) (map[string]remote.TreeFile, error) {
	if f == nil {
		return nil, errors.New("content/remotetree: nil fetcher")
	}
	if !fullSHA.MatchString(spec.SHA) {
		return nil, fmt.Errorf("%w: %q is not a full 40-hex commit SHA", ErrNotPinned, spec.SHA)
	}
	limits := spec.Limits.withDefaults()
	out := map[string]remote.TreeFile{}
	var bytesFetched int64
	if err := fetchDir(ctx, f, spec, limits, "", 0, out, &bytesFetched); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// A root that lists nothing is not an empty store. It is a wrong root,
		// a wrong SHA, or a publisher who published a file where a directory
		// was expected. Handing back a store that enumerates zero items would
		// report success and deliver nothing.
		return nil, fmt.Errorf("%w: %s at %s", ErrEmptyTree, repoPath(spec, ""), spec.SHA)
	}
	return out, nil
}

// bytesOf drops the mode, for the callers whose question is only "what bytes".
func bytesOf(files map[string]remote.TreeFile) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for rel, f := range files {
		out[rel] = f.Data
	}
	return out
}

// fetchDir lists one directory of the pinned tree and recurses, accumulating
// files keyed by STORE-relative path.
func fetchDir(ctx context.Context, f remote.Fetcher, spec Spec, limits Limits, rel string, depth int, out map[string]remote.TreeFile, bytesFetched *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > limits.MaxDepth {
		return fmt.Errorf("%w: %q nests more than %d levels below the content root", ErrTooLarge, rel, limits.MaxDepth)
	}
	entries, err := f.ListDir(ctx, spec.Owner, spec.Repo, repoPath(spec, rel), spec.SHA)
	if err != nil {
		return listError(spec, rel, err)
	}
	for _, e := range entries {
		if err := validateEntryName(e.Name, rel); err != nil {
			return err
		}
		child := e.Name
		if rel != "" {
			child = rel + "/" + e.Name
		}
		if e.IsDir {
			err = fetchDir(ctx, f, spec, limits, child, depth+1, out, bytesFetched)
		} else {
			err = fetchFile(ctx, f, spec, limits, child, e.Executable, out, bytesFetched)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fetchFile reads one file of the pinned tree, charging it against both
// budgets. The count is checked BEFORE the fetch and the byte total AFTER it:
// a file's size is not known until it arrives, so the only honest place to
// notice an over-budget one is once it has.
func fetchFile(ctx context.Context, f remote.Fetcher, spec Spec, limits Limits, rel string, executable bool, out map[string]remote.TreeFile, bytesFetched *int64) error {
	if len(out) >= limits.MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrTooLarge, limits.MaxFiles)
	}
	data, err := f.FetchFile(ctx, spec.Owner, spec.Repo, repoPath(spec, rel), spec.SHA)
	if err != nil {
		return fmt.Errorf("content/remotetree: fetching %s at %s: %w", repoPath(spec, rel), spec.SHA, err)
	}
	*bytesFetched += int64(len(data))
	if *bytesFetched > limits.MaxBytes {
		return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limits.MaxBytes)
	}
	// The exec bit comes from the LISTING, not from a second stat: git already
	// told us the blob's mode when it named the entry, and asking again would be
	// a second read of the same tree that could answer differently.
	out[rel] = remote.TreeFile{Data: data, Executable: executable}
	return nil
}

// listError distinguishes the three ways a listing can fail. Collapsing them is
// how a publisher's mistake reaches the user as a bare "remote content not
// found" with nothing to act on — which is precisely the J20 symptom.
func listError(spec Spec, rel string, err error) error {
	if !errors.Is(err, errs.ErrRemoteContentNotFound) {
		return fmt.Errorf("content/remotetree: listing %s at %s: %w", repoPath(spec, rel), spec.SHA, err)
	}
	if rel == "" {
		// The content root itself is absent at this SHA — a different answer
		// from a root that is there and empty.
		return fmt.Errorf("%w: %s at %s", content.ErrNotFound, repoPath(spec, ""), spec.SHA)
	}
	// Below the root, absence means the listing described a directory that is
	// not there: the tree is not what it claimed, which is a failure rather
	// than an empty level.
	return fmt.Errorf("content/remotetree: %s was listed as a directory but does not exist at %s", repoPath(spec, rel), spec.SHA)
}

// validateEntryName refuses a listing entry that is not a plain single path
// segment.
//
// Entry names arrive from the forge, so they are exactly as trustworthy as the
// publisher. A ".." that reached the path join would fetch — and then serve,
// under this bundle's identity — bytes from outside the pinned content root.
// content.NewMapTreeFS would refuse the assembled path too, but refusing it
// HERE is what stops the fetch from being issued at all, and names the entry
// that caused it.
func validateEntryName(name, parent string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: an entry under %q has an empty name", content.ErrBadPath, parent)
	case name == "." || name == "..":
		return fmt.Errorf("%w: entry %q under %q is a relative path, not a name", content.ErrBadPath, name, parent)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%w: entry %q under %q must be a single path segment", content.ErrBadPath, name, parent)
	}
	return nil
}

// repoPath maps a store-relative path to its repository path. An empty Root
// means the bundles sit at the repository top level, which is a legitimate
// layout and must not become a leading "/".
func repoPath(spec Spec, rel string) string {
	switch {
	case spec.Root == "":
		return rel
	case rel == "":
		return spec.Root
	default:
		return path.Join(spec.Root, rel)
	}
}

// PullTreeFetcher is this package as a remote.TreeFetchFunc: the seam a Puller
// installs a directory-form bundle through.
//
// It exists so the pull path and the content path walk a pinned tree with ONE
// implementation. internal/remote cannot import this package (the content layer
// sits above it), so the alternative was a second recursive fetcher inside
// remote — a duplicate of the budget checks, the traversal-refusal rule and the
// three-way listing-error diagnosis, free to drift from this one in exactly the
// cases nobody tests.
//
// The Spec is assembled rather than taken because a pull already knows every
// field of it and there is nothing left to choose: root is ONE bundle's
// directory (not a bundles root holding many), and the SHA is the lockfile pin,
// which FetchFiles refuses unless it is a full commit SHA.
func PullTreeFetcher(ctx context.Context, f remote.Fetcher, owner, repo, root, sha, repoURL string) (map[string]remote.TreeFile, error) {
	return FetchFiles(ctx, f, Spec{
		Owner:   owner,
		Repo:    repo,
		SHA:     sha,
		Root:    root,
		RepoURL: repoURL,
	})
}

// Compile-time proof that this package satisfies the pull seam. Without it the
// two signatures could drift and only the wiring site would notice.
var _ remote.TreeFetchFunc = PullTreeFetcher
