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
// (the exact path the bundle lands at) or DestDir (a directory the bundle's own
// name lands in) must be set.
//
// A DIRECTORY-form bundle lands as a DIRECTORY under both, because that is what
// it is: OutputFile then names the destination tree's root rather than a file.
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
	srcData, err := afero.ReadFile(fs, bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	// Refuse BEFORE writing anything: a refusal must leave no trace at the
	// destination.
	if err := refuseStaleSignature(fs, dirs, name); err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}

	if filepath.Base(bundle.Path) == bundles.DirectoryFormManifest {
		return exportBundleTree(fs, req, bundle.Path)
	}

	var dest string
	switch {
	case req.OutputFile != "":
		dest = req.OutputFile
		if dir := filepath.Dir(dest); dir != "." {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}
		}
	case req.DestDir != "":
		if err := fs.MkdirAll(req.DestDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create destination directory: %w", err)
		}
		dest = filepath.Join(req.DestDir, filepath.Base(bundle.Path))
	default:
		return nil, fmt.Errorf("either an output file or a destination directory must be specified")
	}

	// No AllowEmpty: srcData is a bundle this same call just loaded through
	// bundles.NewLoader, which never yields empty bytes for a real bundle.
	if err := iox.WriteFileAtomicFs(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	return &ExportBundleResult{Status: "exported", Name: req.Name, Source: bundle.Path, Dest: dest}, nil
}

// exportBundleTree exports a DIRECTORY-form bundle: the whole subtree beneath
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

// ImportBundle validates a bundle — its name (*.yaml, since that is all the
// loader ever looks for in single-file form) and its content
// (bundles.ParseBundle refuses a document that declares nothing) — and copies it
// into the project's committed content bundles directory (symlink-guarded, like
// CreateBundle), carrying its detached `.sig` sibling when one exists. Refuses
// to overwrite without Force.
//
// A DIRECTORY-form bundle is imported WHOLE, by importBundleTree.
func ImportBundle(_ context.Context, cfg *config.Config, req ImportBundleRequest) (*ImportBundleResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
	// A DIRECTORY-form bundle arrives as a tree, addressed either by its own
	// directory or by the manifest inside it, and NEITHER spelling survived the
	// single-file path below. A directory has no extension, so
	// requireLoadableName refused it outright. "<name>/bundle.yaml" PASSED that
	// gate and then landed flat as bundles/bundle.yaml — renamed to "bundle",
	// its items left behind, at a path localFSReader deliberately skips, so
	// nothing would ever have loaded what was written.
	treeSrc, isTree, err := bundleTreeSource(fs, req.SourcePath)
	if err != nil {
		return nil, err
	}
	if isTree {
		return importBundleTree(fs, cfg, req, treeSrc)
	}
	// .yaml ONLY: bundles.Loader.Find stats "<name>.yaml" and
	// "<name>/bundle.yaml" and nothing else, so a ".yml" bundle is as
	// unloadable as a ".txt" one.
	if err := requireLoadableName(req.SourcePath, "bundle", ".yaml"); err != nil {
		return nil, err
	}
	srcData, bundle, err := bundles.EnvelopeAt(fs, req.SourcePath)
	if err != nil {
		if errors.Is(err, bundles.ErrEnvelopeRead) {
			return nil, fmt.Errorf("failed to read source file: %w", err)
		}
		return nil, fmt.Errorf("invalid bundle file: %w", err)
	}

	destPath, _, err := prepareImportDest(fs, cfg, filepath.Base(req.SourcePath), req.Force,
		bundles.BundleLayoutFor(req.SourcePath, bundle))
	if err != nil {
		return nil, err
	}
	// No AllowEmpty: srcData already parsed as a valid bundle above
	// (bundles.ParseBundle refuses a document that declares nothing).
	if err := iox.WriteFileAtomicFs(fs, destPath, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	// Import carries the signature but does NOT judge the pair (unlike export/push
	// below it, which refuse a stale one). It is the CONSUMER side: a broken pair
	// arriving from elsewhere is evidence, and the trust gate must see it at
	// exposure and report it as the tamper finding it is. Refusing or discarding it
	// here would destroy that evidence — and the publisher-side guards mean a
	// broken pair should never have been publishable in the first place.
	return &ImportBundleResult{
		Status:    "imported",
		Source:    req.SourcePath,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Commands:  len(bundle.Commands),
		MCP:       len(bundle.MCP),
	}, nil
}

// bundleTreeSource resolves an import source to the root directory of a
// DIRECTORY-form bundle.
//
// It reports false for a single-file bundle and for a source that does not
// exist — the single-file path already names a missing source well, and giving
// the same mistake two different messages depending on which gate saw it first
// is how a diagnostic stops being trustworthy.
func bundleTreeSource(fs afero.Fs, sourcePath string) (string, bool, error) {
	info, err := fs.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cannot read %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		return filepath.Clean(sourcePath), true, nil
	}
	if filepath.Base(sourcePath) == bundles.DirectoryFormManifest {
		return filepath.Dir(sourcePath), true, nil
	}
	return "", false, nil
}

// importBundleTree copies a DIRECTORY-form bundle into the project's committed
// content bundles directory — whole, and under its OWN name.
//
// The name comes from bundles.ExtractBundleName, the same derivation the loader
// itself uses, rather than from the source's basename. Basename is what the
// single-file path uses and it is right there; for a tree it is the manifest's
// filename, which is "bundle.yaml" for every directory bundle that has ever
// existed, so it would name each of them "bundle" and every import would land on
// top of the last.
func importBundleTree(fs afero.Fs, cfg *config.Config, req ImportBundleRequest, srcDir string) (*ImportBundleResult, error) {
	srcManifest := filepath.Join(srcDir, bundles.DirectoryFormManifest)
	_, bundle, err := bundles.EnvelopeAt(fs, srcManifest)
	if err != nil {
		if errors.Is(err, bundles.ErrEnvelopeRead) {
			return nil, fmt.Errorf("import %s: a directory-form bundle must carry its %s manifest: %w",
				srcDir, bundles.DirectoryFormManifest, err)
		}
		return nil, fmt.Errorf("invalid bundle file: %w", err)
	}
	name := bundles.ExtractBundleName(srcManifest)
	if err := bundles.ValidateBundleName(name); err != nil {
		return nil, fmt.Errorf("import %s: %w", srcDir, err)
	}

	destPath, exists, err := prepareImportDest(fs, cfg, name, req.Force,
		bundles.BundleLayoutFor(srcManifest, bundle))
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
// leaf name under the FORMAT root of this project's committed bundles
// directory. The layout is the CALLER's to state, because only the caller has
// the incoming document's own bytes to judge it by (bundles.BundleLayoutFor) —
// and an import that guessed would land the bundle under a root the reader
// does not search for that form, which is the silent shape of this failure:
// exit 0, "imported", and nothing loadable afterwards. It refuses a
// symlinked component, creating the parent, and refusing an existing
// destination unless force was given. It reports the path and whether something
// is already there.
//
// Both import forms run it, and that is the whole point. The single-file and
// tree paths differ only in what they COPY, so a guard living inside one of
// them is a guard the other silently does without — and the guards here are the
// symlink-traversal refusal and the no-clobber-without-force refusal, neither
// of which has a harmless absence.
func prepareImportDest(fs afero.Fs, cfg *config.Config, leaf string, force bool, layout paths.BundleLayout) (string, bool, error) {
	bundleDir := paths.LocalBundlesPathFor(cfg.GetAppPaths()[0], layout)
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
