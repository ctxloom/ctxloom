package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// MoveDestRemote and MoveDestPath are the two values MoveBundleResult.DestKind
// takes. They are exported because DestKind is part of this result's contract:
// a frontend branching on the outcome (the CLI's printMoveResult annotates a
// remote destination with the remote's name) would otherwise have to spell the
// literal itself, with nothing tying the two spellings together.
const (
	MoveDestRemote = "remote"
	MoveDestPath   = "path"
)

// moveDestKind distinguishes the two destinations a bundle can move to.
type moveDestKind string

const (
	moveDestRemote moveDestKind = MoveDestRemote
	moveDestPath   moveDestKind = MoveDestPath
)

// moveDest is a resolved `--to`: exactly one of Remote (a configured registry
// name) or Dir (an existing local directory) is set.
type moveDest struct {
	Kind   moveDestKind
	Remote string
	Dir    string
}

// MoveBundleRequest is the input for MoveBundle.
type MoveBundleRequest struct {
	// Name is the authored bundle to relocate (in .ctxloom/content/bundles).
	Name string `json:"name"`
	// To is the destination: a configured remote NAME, or a local directory
	// path. See resolveMoveDest for the (deliberately unambiguous) rule.
	To string `json:"to"`
	// Force overwrites an existing bundle of the same name at a local
	// destination. Never applies to a remote (a push updates in place).
	Force bool `json:"force,omitempty"`
	// Message is the commit subject/body for a remote destination.
	Message string `json:"message,omitempty"`

	// FS is an optional filesystem (defaults to the OS filesystem). Only the
	// local-path destination honours it end-to-end; a remote move publishes
	// through PushBundle, which reads the real filesystem.
	FS afero.Fs `json:"-"`

	// PublishManager overrides the default registry-built one for a remote
	// destination (tests inject one backed by a mock Publisher).
	PublishManager *remote.PublishManager `json:"-"`
}

// MoveBundleResult reports where the bundle went and that the source is gone.
type MoveBundleResult struct {
	Status   string `json:"status"` // "moved"
	Name     string `json:"name"`
	Source   string `json:"source"`
	DestKind string `json:"dest_kind"` // "remote" | "path"
	// Dest is the local destination file, or the path inside the remote repo.
	Dest string `json:"dest"`
	// SigDest is where the detached publisher signature landed, or "" when the
	// bundle was never signed.
	SigDest string `json:"sig_dest,omitempty"`
	// Remote/CommitSHA/Signed are set for a remote destination only.
	Remote    string `json:"remote,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Signed    bool   `json:"signed,omitempty"`
}

// MoveBundle relocates an authored bundle out of this project — to a configured
// remote (a publish) or to another local directory / ctxloom checkout (a copy) —
// and then removes the source.
//
// Bytes are carried VERBATIM. A bundle's publisher signature covers the bundle
// file's exact bytes (spec §3.1) and nothing between publisher and verifier may
// re-serialize them (spec §3.0), so move never parses-and-re-emits, and never
// re-signs: the existing detached `<name>.yaml.sig` stays valid at the
// destination precisely because the bytes don't change. A signature that exists
// but cannot be carried is an error — landing the bundle unsigned would be a
// silent trust downgrade (spec §7A.4).
//
// ORDERING (the invariant that stops this command eating someone's work): the
// source is removed ONLY after the destination write has fully succeeded —
// bundle bytes AND, when present, the signature. Every failure path above
// returns early with the source untouched, so a move that dies half-way is a
// no-op locally, never a deletion.
func MoveBundle(ctx context.Context, cfg *config.Config, req MoveBundleRequest) (*MoveBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.To == "" {
		return nil, fmt.Errorf("destination is required: pass --to <remote|path>")
	}
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)

	name, src, err := loadMoveSource(cfg, fs, req.Name)
	if err != nil {
		return nil, err
	}
	// Before anything is written or removed: refuse a move that cannot be whole.
	if err := requireWholeMovable(fs, name, src); err != nil {
		return nil, err
	}
	dest, err := resolveMoveDest(cfg, fs, req.To)
	if err != nil {
		return nil, err
	}

	result, err := moveByDest(ctx, cfg, fs, req, name, src, dest)
	if err != nil {
		return nil, err
	}

	// Destination write succeeded — and only now is the source removed.
	if err := removeMoveSource(fs, src, result.Dest); err != nil {
		return nil, err
	}
	return result, nil
}

// moveByDest routes a resolved destination to the writer that services it.
//
// The switch is EXHAUSTIVE over moveDestKind, and an unrecognised kind is an
// error rather than the local-copy branch. That matters more here than it looks:
// MoveBundle deletes the source the moment this returns without an error, so a
// kind that fell through to the local copy would be handed whatever Dir happened
// to be set — "" for any destination that does not populate it — and the source
// would be removed behind a write that went somewhere nobody chose. A
// destination this function does not understand must stop the move.
func moveByDest(ctx context.Context, cfg *config.Config, fs afero.Fs, req MoveBundleRequest, name, src string, dest moveDest) (*MoveBundleResult, error) {
	switch dest.Kind {
	case moveDestRemote:
		return moveToRemote(ctx, cfg, fs, req, name, src, dest.Remote)
	case moveDestPath:
		return moveToPath(ctx, cfg, fs, req, name, src, dest.Dir)
	default:
		return nil, fmt.Errorf("unsupported move destination kind %q", dest.Kind)
	}
}

// requireWholeMovable refuses a move whose source carries files the move cannot
// take with it (taskloom hurried-showplace).
//
// A move is a publish-or-copy followed by a DELETION of the source, and both
// writers carry exactly two files: the bundle manifest and its detached .sig.
// For a single-file bundle that is the whole bundle. For a DIRECTORY-form
// bundle it is not — everything else in the directory stays behind and is then
// orphaned when the manifest is deleted, leaving neither copy whole, at exit 0,
// with no warning. Skills are the only reason directory form exists at all
// (bundles.Loader refuses `skills:` in single-file form), so the failure lands
// squarely on the thing the shape was created to carry.
//
// The check is by PAYLOAD, not by shape: a directory-form deps holding nothing
// but its manifest loses nothing and still moves. And it is stated as "anything
// that is not the manifest or its signature" rather than as "skills/", so it
// covers a tree-form bundle's fragments/ and prompts/ too — publish cannot carry
// those either, and a skills-only guard would have let them vanish the same way.
//
// It runs BEFORE the destination is even resolved. A guard that fired after the
// write would have already produced the split state it exists to prevent.
//
// This is a REFUSAL, not the fix. The fix is a publish that writes a whole tree
// (taskloom excusable-flatness); until that exists, not starting is the only
// outcome that leaves the user's bundle intact.
func requireWholeMovable(fs afero.Fs, name, src string) error {
	if filepath.Base(src) != bundles.DirectoryFormManifest {
		return nil
	}
	dir := filepath.Dir(src)
	var stranded []string
	err := afero.Walk(fs, dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if p == src || p == src+sigSuffix {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		stranded = append(stranded, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect the %q bundle directory %s before moving it: %w", name, dir, err)
	}
	if len(stranded) == 0 {
		return nil
	}
	sort.Strings(stranded)
	return fmt.Errorf("refusing to move %q: it is a directory-form bundle and moving carries only %s (and its signature), "+
		"so %d file(s) would be left behind and orphaned when the source manifest is deleted — %s. "+
		"Publishing a whole bundle tree is not built yet; copy the directory by hand, or keep the bundle here until it is",
		name, bundles.DirectoryFormManifest, len(stranded), strings.Join(stranded, ", "))
}

// loadMoveSource resolves the authored bundle to move, returning its canonical
// name and file path. It reads the COMMITTED content tree (LocalBundlesPath) —
// the gitignored cache holds fetched copies of other people's bundles, which are
// not ours to move.
func loadMoveSource(cfg *config.Config, fs afero.Fs, arg string) (name, path string, err error) {
	var dirs []string
	for _, p := range cfg.GetAppPaths() {
		dirs = append(dirs, paths.LocalBundlesPath(p))
	}
	name = canonicalizeBundleArg(cfg, arg, dirs, fs)
	bundle, err := bundles.NewLoader(bundles.NewProjectReader(fs, dirs)).Load(name)
	if err != nil {
		return "", "", fmt.Errorf("bundle %q not found: %w", arg, err)
	}
	// Same symlink guard DeleteBundle applies: the source is about to be
	// removed, so a symlinked component under the bundles tree must not steer
	// the removal at a file outside it.
	if err := requireSafeBundlePath(dirs, bundle.Path); err != nil {
		return "", "", err
	}
	return name, bundle.Path, nil
}

// resolveMoveDest turns `--to` into a destination, and never guesses. A
// configured remote NAME wins (that is the documented rule — a bundle repo is
// addressed by name, not by whatever directory happens to share its spelling);
// otherwise the argument must be an existing directory. Anything else is an
// error listing what was available, rather than a silent misfire.
func resolveMoveDest(cfg *config.Config, fs afero.Fs, to string) (moveDest, error) {
	registry, err := getRegistry(cfg, remote.WithRegistryFS(fs))
	if err != nil {
		return moveDest{}, fmt.Errorf("load registry: %w", err)
	}
	if registry.Has(to) {
		return moveDest{Kind: moveDestRemote, Remote: to}, nil
	}
	isDir, err := afero.DirExists(fs, to)
	if err != nil {
		return moveDest{}, fmt.Errorf("inspect destination %q: %w", to, err)
	}
	if isDir {
		return moveDest{Kind: moveDestPath, Dir: destBundlesDir(fs, to)}, nil
	}
	return moveDest{}, fmt.Errorf("destination %q is neither a configured remote nor an existing directory%s",
		to, knownRemotesHint(registry))
}

// destBundlesDir maps a destination directory to the directory the bundle
// actually lands in: a ctxloom project checkout (or a .ctxloom directory itself)
// takes the bundle into its committed content tree; any other directory takes it
// as-is.
func destBundlesDir(fs afero.Fs, dir string) string {
	if filepath.Base(dir) == paths.AppDirName {
		return paths.LocalBundlesPath(dir)
	}
	if isDir, _ := afero.DirExists(fs, filepath.Join(dir, paths.AppDirName)); isDir {
		return paths.LocalBundlesPath(filepath.Join(dir, paths.AppDirName))
	}
	return dir
}

// knownRemotesHint lists the configured remote names for an error message.
func knownRemotesHint(registry *remote.Registry) string {
	all := registry.List()
	if len(all) == 0 {
		return " (no remotes configured)"
	}
	names := make([]string, 0, len(all))
	for _, r := range all {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf(" (configured remotes: %s)", strings.Join(names, ", "))
}

// moveToPath copies the bundle (and its signature) into a local directory. The
// copy itself is ExportBundle — one verbatim-bytes copier, signature-carrying
// included — so move re-implements none of it.
func moveToPath(ctx context.Context, cfg *config.Config, fs afero.Fs, req MoveBundleRequest, name, src, destDir string) (*MoveBundleResult, error) {
	destFile := filepath.Join(destDir, filepath.Base(src))
	if sameMovePath(destFile, src) {
		return nil, fmt.Errorf("destination is the bundle's own directory: %s", destDir)
	}
	if exists, _ := afero.Exists(fs, destFile); exists && !req.Force {
		return nil, fmt.Errorf("bundle already exists at destination: %s (use --force to overwrite)", destFile)
	}

	res, err := ExportBundle(ctx, cfg, ExportBundleRequest{Name: name, DestDir: destDir, FS: fs})
	if err != nil {
		return nil, err
	}
	// An unsigned bundle overwriting a signed one would leave the old .sig
	// behind, "signing" bytes it never covered. Drop it with the content.
	if res.SigDest == "" {
		if err := removeIfExists(fs, destFile+sigSuffix); err != nil {
			return nil, fmt.Errorf("remove stale destination signature: %w", err)
		}
	}
	return &MoveBundleResult{
		Status:   "moved",
		Name:     name,
		Source:   src,
		DestKind: string(moveDestPath),
		Dest:     res.Dest,
		SigDest:  res.SigDest,
	}, nil
}

// moveToRemote publishes the bundle to a configured remote, carrying its
// existing signature, via the same PushBundle path `ctxloom bundle push` uses.
func moveToRemote(ctx context.Context, cfg *config.Config, fs afero.Fs, req MoveBundleRequest, name, src, remoteName string) (*MoveBundleResult, error) {
	// The detached signature travels as-is — but only once it has been proven to
	// cover the bytes publish will write. It usually does: nothing re-serializes
	// them (spec §3.0), so re-signing would be pointless churn needing a key the
	// mover may not have to hand. What it does NOT survive is the bundle being
	// edited after it was signed, which leaves a signature over bytes that no
	// longer exist. Publishing that pair is worse than publishing nothing: the key
	// is trusted, the bytes do not match, and every consumer reads it as tampering
	// and withholds the bundle. PushBundle verifies the pair and refuses it, so a
	// stale signature stops here rather than at the user's tamper alarm.
	//
	// Read through PublisherSignature — the one seam every publishing boundary
	// shares — rather than a local exists-then-read: a stat error on the .sig
	// used to be discarded, which left signature nil, which skipped the
	// "published but its signature did not" guard below, which let
	// removeMoveSource delete the signed local source. An unreadable signature
	// now stops the move, and so does a stale one (PushBundle re-checks below,
	// but the refusal belongs at the boundary that decided to carry it).
	signature, err := PublisherSignature(fs, src, nil)
	if err != nil {
		return nil, fmt.Errorf("move %q: %w", name, err)
	}

	res, err := PushBundle(ctx, cfg, PushBundleRequest{
		Path:           src,
		Remote:         remoteName,
		Message:        req.Message,
		Signature:      signature,
		PublishManager: req.PublishManager,
	})
	if err != nil {
		return nil, err
	}
	// Publish reports Signed only when the sibling .sig actually landed; a
	// carried signature that didn't land is a lost signature, so refuse to
	// delete the source behind it.
	if signature != nil && !res.Signed {
		return nil, fmt.Errorf("bundle %q published to %s but its signature was not: source left in place", name, remoteName)
	}

	sigDest := ""
	if res.Signed {
		sigDest = res.TargetPath + sigSuffix
	}
	return &MoveBundleResult{
		Status:    "moved",
		Name:      name,
		Source:    src,
		DestKind:  string(moveDestRemote),
		Dest:      res.TargetPath,
		SigDest:   sigDest,
		Remote:    res.Remote,
		CommitSHA: res.CommitSHA,
		Signed:    res.Signed,
	}, nil
}

// removeMoveSource deletes the source bundle and its signature — the last step
// of a move, reached only once the destination holds both.
//
// The YAML goes first: if that removal fails we abort with the signed pair still
// intact and internally consistent, rather than a bundle stripped of its
// signature.
//
// Both failures name the DESTINATION, because the two states they leave behind
// need opposite responses and the user cannot tell them apart otherwise:
//   - the YAML is still here — the bundle now exists in two places, and the move
//     can be re-run or the duplicate deleted;
//   - the YAML is gone and only the orphan .sig remains — the move HAPPENED.
//     Re-running it cannot work (there is no source left to move) and the only
//     remaining action is deleting the stray signature by hand. Saying so is the
//     difference between a user cleaning up and a user retrying a command that
//     will now tell them the bundle does not exist.
func removeMoveSource(fs afero.Fs, src, dest string) error {
	if err := fs.Remove(src); err != nil {
		return fmt.Errorf("the bundle was written to %s but the source %s could not be removed — it now exists in both places: %w", dest, src, err)
	}
	if err := removeIfExists(fs, src+sigSuffix); err != nil {
		return fmt.Errorf("the bundle was moved to %s and the source is gone, but the stray source signature %s could not be removed — delete it by hand; re-running the move will not work: %w",
			dest, src+sigSuffix, err)
	}
	return nil
}

// removeIfExists removes path when present; absence is not an error.
func removeIfExists(fs afero.Fs, path string) error {
	exists, err := afero.Exists(fs, path)
	if err != nil || !exists {
		return err
	}
	return fs.Remove(path)
}

// sameMovePath reports whether two paths name the same file (cleaned + absolute
// where possible) — the guard against a "move" that copies a bundle onto itself
// and then deletes it.
func sameMovePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}
