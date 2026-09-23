package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ExportBundleRequest is the input for ExportBundle. Exactly one of OutputFile
// (the exact path the bundle's tree lands at) or DestDir (a directory the
// bundle's own name lands in) must be set. A bundle is a tree, so OutputFile
// names the destination tree's root rather than a file.
type ExportBundleRequest struct {
	Name       string `json:"name"`
	OutputFile string `json:"output_file,omitempty"`
	DestDir    string `json:"dest_dir,omitempty"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ExportBundleResult reports the export.
type ExportBundleResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
	// SigDest is the exported tree's .sigs/ store — where its signature
	// travelled to — or "" when the bundle carries none.
	SigDest string `json:"sig_dest,omitempty"`
}

// ExportBundle copies a named bundle out to an arbitrary file or directory (an
// author workflow — e.g. staging for publish). The destination is user-chosen
// and outside the bundles tree, so no symlink guard applies.
//
// A tree's signature is its SHA256SUMS manifest and the .sigs/ entries over
// it, and they travel with the tree. A manifest that no longer covers the
// tree is refused outright (refuseStaleSignature): exporting it would plant a
// tamper alarm at the destination.
func ExportBundle(_ context.Context, cfg *config.Config, req ExportBundleRequest) (*ExportBundleResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no bundles directory found")
	}
	fs := getFS(req.FS)
	// Resolve the authored (committed content) bundle dirs directly rather than
	// via GetBundleDirs, which os.Stat-gates on the real FS, so an injected
	// filesystem works; the loader filters non-existent dirs itself via
	// afero.DirExists.
	var dirs []string
	for _, p := range cfg.GetAppPaths() {
		dirs = append(dirs, paths.LocalBundlesPath(p))
	}
	// Accept a per-remote short "<remote>/<bundle>" name (decision E: a local file
	// of the same spelling still wins); bare/canonical names pass through.
	name := canonicalizeBundleArg(cfg, req.Name, dirs, fs)
	bundle, err := bundles.NewLoader(projectReader(fs, dirs)).Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	// Refuse BEFORE writing anything: a refusal must leave no trace at the
	// destination.
	if err := refuseStaleSignature(fs, dirs, name); err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}

	return exportBundleTree(fs, req, bundle.Path)
}

// exportBundleTree exports a bundle: the whole tree beneath
// its manifest, verbatim, landing under the bundle's own directory name.
//
// Files at the destination are overwritten, but the destination is never
// PRUNED — and the asymmetry with ImportBundle, which does replace wholesale,
// is deliberate. An import destination is a path this package DERIVES inside
// its own bundles tree; an export destination is arbitrary user-chosen space,
// where removing a directory outright would delete files the user never handed
// us.
func exportBundleTree(fs afero.Fs, req ExportBundleRequest, manifestPath string) (*ExportBundleResult, error) {
	srcDir := filepath.Dir(manifestPath)
	var dest string
	switch {
	case req.OutputFile != "":
		dest = req.OutputFile
	case req.DestDir != "":
		dest = filepath.Join(req.DestDir, filepath.Base(srcDir))
	default:
		return nil, fmt.Errorf("either an output file or a destination directory must be specified")
	}
	if err := copyBundleTree(fs, srcDir, dest); err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}
	res := &ExportBundleResult{Status: "exported", Name: req.Name, Source: manifestPath, Dest: dest}
	// The .sigs/ store lives INSIDE the tree, so the copy above already
	// carried it. Naming it here is reporting, not a second write.
	if present, _ := afero.DirExists(fs, filepath.Join(dest, content.SigDirName)); present {
		res.SigDest = filepath.Join(dest, content.SigDirName)
	}
	return res, nil
}

// copyBundleTree copies every file under src to dest, preserving each file's
// path relative to src and its permission bits.
//
// Every file travels, with no filter. A bundle tree states its own integrity
// through a SHA256SUMS covering all of it and a .sigs/ store attesting that
// manifest, so a copy that dropped — or added — a single file would arrive
// reporting tampering rather than arriving incomplete.
func copyBundleTree(fs afero.Fs, src, dest string) error {
	return afero.Walk(fs, src, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			if err := fs.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			return nil
		}
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		// AllowEmpty: the source tree is the authority on what this bundle
		// contains. A legitimately empty file in it is covered by SHA256SUMS
		// like any other, so refusing to copy it would land a tree that reports
		// content MISSING rather than one that failed to write.
		if err := iox.WriteFileAtomicFs(fs, target, data, info.Mode().Perm(), iox.AllowEmpty()); err != nil {
			return err
		}
		return nil
	})
}

// refuseStaleSignature is the one refusal every publishing boundary gives
// when the bundle it would ship carries a signature that no longer covers
// its files: the reader established that fact (SignatureInvalid, the
// stale-manifest row), so the boundary asks the reader rather than verifying
// a second time. It names the remedy, because there is exactly one: re-sign.
// Shipping a stale pair is not on the menu — that is what makes every
// consumer see tampering.
func refuseStaleSignature(fs afero.Fs, dirs []string, name string) error {
	read, err := bundles.NewLoader(projectReader(fs, dirs)).Read(name)
	if err != nil {
		return err
	}
	if read.Signature() != bundles.SignatureInvalid {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrStaleSignature, bundles.StaleSignatureAdvice(read))
}

// ErrStaleSignature is the publishing boundaries' refusal of a bundle whose
// signature no longer covers its files.
var ErrStaleSignature = errors.New("the bundle's signature no longer covers its files — re-sign it before publishing")

// ImportBundleRequest is the input for ImportBundle.
type ImportBundleRequest struct {
	SourcePath string `json:"source_path"`
	Force      bool   `json:"force"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ImportBundleResult reports the import plus a small summary of the bundle.
type ImportBundleResult struct {
	Status    string `json:"status"`
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	Version   string `json:"version"`
	Fragments int    `json:"fragments"`
	Commands  int    `json:"commands"`
	MCP       int    `json:"mcp"`
	// SigDest is the imported tree's .sigs/ store, or "" when the source
	// carried none. Import PLACES the signature but never verifies it:
	// verification belongs to the reader (attest.VerifyBundle) and the trust
	// gate at exposure (composite.Trust), not to the copy step.
	SigDest string `json:"sig_dest,omitempty"`
}

// ImportBundle copies a bundle tree — addressed by its directory or by the
// bundle.yaml inside it — WHOLE into the project's committed content bundles
// directory (symlink-guarded, like CreateBundle), under its own name, with the
// .sigs/ store it carries. Refuses to overwrite without Force.
func ImportBundle(_ context.Context, cfg *config.Config, req ImportBundleRequest) (*ImportBundleResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
	srcDir, err := bundleTreeSource(fs, req.SourcePath)
	if err != nil {
		return nil, err
	}
	return importBundleTree(fs, cfg, req, srcDir)
}

// ErrNotABundleTree refuses an import source that is not a bundle: a bundle is
// a directory holding bundle.yaml, addressed by the directory or by that file.
var ErrNotABundleTree = errors.New("not a bundle: a bundle is a directory holding " + bundles.DirectoryFormManifest)

// bundleTreeSource resolves an import source to the root directory of a
// bundle tree.
func bundleTreeSource(fs afero.Fs, sourcePath string) (string, error) {
	info, err := fs.Stat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to read source %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		return filepath.Clean(sourcePath), nil
	}
	if filepath.Base(sourcePath) == bundles.DirectoryFormManifest {
		return filepath.Dir(sourcePath), nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotABundleTree, sourcePath)
}

// importBundleTree copies a bundle tree into the project's committed content
// bundles directory — whole, and under its OWN name: the source directory's,
// through bundles.ExtractBundleName, the same derivation the loader uses.
func importBundleTree(fs afero.Fs, cfg *config.Config, req ImportBundleRequest, srcDir string) (*ImportBundleResult, error) {
	srcManifest := filepath.Join(srcDir, bundles.DirectoryFormManifest)
	if _, _, err := bundles.EnvelopeAt(fs, srcManifest); err != nil {
		if errors.Is(err, bundles.ErrEnvelopeRead) {
			return nil, fmt.Errorf("import %s: a bundle must carry its %s: %w",
				srcDir, bundles.DirectoryFormManifest, err)
		}
		return nil, fmt.Errorf("invalid bundle file: %w", err)
	}
	name := bundles.ExtractBundleName(srcManifest)
	if err := bundles.ValidateBundleName(name); err != nil {
		return nil, fmt.Errorf("import %s: %w", srcDir, err)
	}

	destPath, exists, err := prepareImportDest(fs, cfg, name, req.Force)
	if err != nil {
		return nil, err
	}
	if exists {
		// Replace the tree wholesale rather than copying over it. A file the
		// incoming version DROPPED would otherwise survive as an extra that the
		// incoming SHA256SUMS does not cover, and every reader of the result
		// would report content added after signing — a tamper finding produced
		// by the import itself.
		if err := fs.RemoveAll(destPath); err != nil {
			return nil, fmt.Errorf("failed to replace existing bundle %s: %w", destPath, err)
		}
	}
	if err := copyBundleTree(fs, srcDir, destPath); err != nil {
		return nil, fmt.Errorf("import %s: %w", srcDir, err)
	}
	// The summary is read from the landed tree: its envelope declares no
	// items, so counting the envelope would report every import as empty.
	bundle, err := bundles.NewLoader(projectReader(fs, []string{paths.LocalBundlesPath(cfg.GetAppPaths()[0])})).Load(name)
	if err != nil {
		return nil, fmt.Errorf("import %s: the imported bundle does not load: %w", srcDir, err)
	}

	res := &ImportBundleResult{
		Status:    "imported",
		Source:    srcDir,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Commands:  len(bundle.Commands),
		MCP:       len(bundle.MCP),
	}
	// Import PLACES a signature and never judges it (see ImportBundleResult).
	// The .sigs/ store travelled inside the copy, so this reports where it
	// landed rather than writing it again.
	sigDest := filepath.Join(destPath, content.SigDirName)
	present, err := afero.DirExists(fs, sigDest)
	if err != nil {
		return nil, fmt.Errorf("import %s: cannot check for %s: %w", srcDir, sigDest, err)
	}
	if present {
		res.SigDest = sigDest
	}
	return res, nil
}

// prepareImportDest resolves and guards the path an import writes to: the given
// leaf name under the format root of this project's committed bundles
// directory. It refuses a symlinked component, creates the parent, and refuses
// an existing destination unless force was given. It reports the path and
// whether something is already there.
func prepareImportDest(fs afero.Fs, cfg *config.Config, leaf string, force bool) (string, bool, error) {
	bundleDir := paths.LocalBundlesPathFor(cfg.GetAppPaths()[0], paths.LayoutV2)
	destPath := filepath.Join(bundleDir, leaf)
	if err := requireSafeBundlePath([]string{bundleDir}, destPath); err != nil {
		return "", false, err
	}
	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return "", false, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	// The error matters: a destination that cannot be STATTED (permissions, a
	// broken symlink) used to read as "does not exist" and was overwritten
	// without --force — the one outcome this guard exists to prevent. Matches
	// ImportProfile, which already reads it.
	exists, err := afero.Exists(fs, destPath)
	if err != nil {
		return "", false, fmt.Errorf("cannot check whether %s already exists: %w", destPath, err)
	}
	if exists && !force {
		return "", false, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}
	return destPath, exists, nil
}
