package operations

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	// SigDest is where the tree's .sigs/ store landed, or "" when the
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
	layout, err := moveSourceLayout(fs, src)
	if err != nil {
		return nil, err
	}
	dest, err := resolveMoveDest(cfg, fs, req.To, layout)
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
	// The source file is gone from this project's bundles tree; what the
	// readers would see has changed, and the caller announces that by
	// publishing the next generation (config.Owner.Reload).
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
	bundle, err := bundles.NewLoader(projectReader(fs, dirs)).Load(name)
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
func resolveMoveDest(cfg *config.Config, fs afero.Fs, to string, layout paths.BundleLayout) (moveDest, error) {
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
		return moveDest{Kind: moveDestPath, Dir: destBundlesDir(fs, to, layout)}, nil
	}
	return moveDest{}, fmt.Errorf("destination %q is neither a configured remote nor an existing directory%s",
		to, knownRemotesHint(registry))
}

// destBundlesDir maps a destination directory to the directory the bundle
// actually lands in: a ctxloom project checkout (or a .ctxloom directory itself)
// takes the bundle into the FORMAT root of its committed content tree; any
// other directory takes it as-is, because a plain directory has no layout.
func destBundlesDir(fs afero.Fs, dir string, layout paths.BundleLayout) string {
	if filepath.Base(dir) == paths.AppDirName {
		return paths.LocalBundlesPathFor(dir, layout)
	}
	if isDir, _ := afero.DirExists(fs, filepath.Join(dir, paths.AppDirName)); isDir {
		return paths.LocalBundlesPathFor(filepath.Join(dir, paths.AppDirName), layout)
	}
	return dir
}

// moveSourceLayout reports which FORMAT root the moved bundle belongs in once
// it lands in another project.
//
// The destination root follows the bundle's OWN format, not the root it was
// sitting in here: a move is a copy plus a deletion, and a copy filed under the
// wrong format root is a bundle the receiving project cannot load — at exit 0,
// with the source already gone.
func moveSourceLayout(fs afero.Fs, src string) (paths.BundleLayout, error) {
	_, env, err := bundles.EnvelopeAt(fs, src)
	if err != nil {
		return paths.LayoutUnknown, err
	}
	return bundles.BundleLayoutFor(src, env), nil
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
	return &MoveBundleResult{
		Status:   "moved",
		Name:     name,
		Source:   src,
		DestKind: string(moveDestPath),
		Dest:     res.Dest,
		SigDest:  res.SigDest,
	}, nil
}

// moveToRemote publishes the bundle to a configured remote via the same
// PushBundle path `ctxloom bundle push` uses. A tree's signature — its
// SHA256SUMS manifest and .sigs/ entries — travels inside the tree; a stale
// one is refused by PushBundle before anything is written.
func moveToRemote(ctx context.Context, cfg *config.Config, fs afero.Fs, req MoveBundleRequest, name, src, remoteName string) (*MoveBundleResult, error) {
	res, err := PushBundle(ctx, cfg, PushBundleRequest{
		Path:           src,
		Remote:         remoteName,
		Message:        req.Message,
		PublishManager: req.PublishManager,
	})
	if err != nil {
		return nil, err
	}

	sigDest := ""
	if res.Signed {
		sigDest = path.Join(res.TargetPath, content.SigDirName)
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

// removeMoveSource deletes the source bundle — the last step of a move,
// reached only once the destination holds the whole thing.
//
// A DIRECTORY-form bundle's source is its WHOLE directory, not just the
// manifest and its sidecar: moveToRemote (runTreePush) and moveToPath
// (exportBundleTree) both already carry every file beneath it, so leaving
// fragments/, skills/ etc. behind here would strand exactly what the publish
// side just proved it could carry — orphaned at the source, at exit 0, with
// no warning. For a single-file bundle the manifest and its detached .sig
// sibling ARE the whole source.
//
// Both failures name the DESTINATION, because the two states they leave behind
// need opposite responses and the user cannot tell them apart otherwise:
//   - the source is still here — the bundle now exists in two places, and the
//     move can be re-run or the duplicate deleted;
//   - the source is gone (directory form) or only an orphan .sig remains
//     (single-file) — the move HAPPENED. Re-running it cannot work (there is
//     no source left to move) and the only remaining action is cleaning up by
//     hand. Saying so is the difference between a user cleaning up and a user
//     retrying a command that will now tell them the bundle does not exist.
func removeMoveSource(fs afero.Fs, src, dest string) error {
	if filepath.Base(src) == bundles.DirectoryFormManifest {
		dir := filepath.Dir(src)
		if err := fs.RemoveAll(dir); err != nil {
			return fmt.Errorf("the bundle was written to %s but the source directory %s could not be removed — it now exists in both places: %w", dest, dir, err)
		}
		return nil
	}
	if err := fs.Remove(src); err != nil {
		return fmt.Errorf("the bundle was written to %s but the source %s could not be removed — it now exists in both places: %w", dest, src, err)
	}
	return nil
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
