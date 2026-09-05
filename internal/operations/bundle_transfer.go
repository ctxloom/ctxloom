package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
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
	// SigDest is where the bundle's detached publisher signature landed, or ""
	// when the source bundle carried none.
	SigDest string `json:"sig_dest,omitempty"`
}

// ExportBundle copies a named bundle out to an arbitrary file or directory (an
// author workflow — e.g. staging for publish). The destination is user-chosen
// and outside the bundles tree, so no symlink guard applies.
//
// A bundle's publisher signature lives in a detached `<file>.yaml.sig` sibling
// (spec §4.2), so copying the YAML alone would silently strip the bundle's
// trust — the copy would arrive unverifiable. Export therefore carries the .sig
// alongside whenever the source has one — but only after proving it covers the
// bytes being copied. A signature over a bundle's PREVIOUS contents is refused
// outright (staleSignatureError): exporting that pair would plant a tamper alarm
// at the destination.
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
	bundle, err := bundles.NewLoader(bundles.NewProjectReader(fs, dirs)).Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	srcData, err := afero.ReadFile(fs, bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	// Verify the pair BEFORE writing anything. A refusal must leave no trace at
	// the destination: half-exporting a signed bundle as a bare YAML would be the
	// silent trust downgrade this function exists to prevent, only with the export
	// reported as failed.
	sig, err := PublisherSignature(fs, bundle.Path, srcData)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}

	// A DIRECTORY-form bundle's manifest is only its envelope: the items live in
	// sibling files, with the SHA256SUMS that covers them and the .sigs/ store
	// that attests it. Copying bundle.yaml alone wrote a destination file
	// literally NAMED "bundle.yaml", holding an envelope whose content was no
	// longer beside it — exit 0, "exported", payload gone.
	if filepath.Base(bundle.Path) == bundles.DirectoryFormManifest {
		return exportBundleTree(fs, req, bundle.Path, sig)
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

	sigDest, err := writeSignature(fs, dest, sig)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}
	return &ExportBundleResult{Status: "exported", Name: req.Name, Source: bundle.Path, Dest: dest, SigDest: sigDest}, nil
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
func exportBundleTree(fs afero.Fs, req ExportBundleRequest, manifestPath string, sig []byte) (*ExportBundleResult, error) {
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
	if sig != nil {
		// The detached sibling lives INSIDE the tree, beside the manifest, so
		// the copy above already carried it. Naming it here is reporting, not a
		// second write.
		res.SigDest = filepath.Join(dest, bundles.DirectoryFormManifest+sigSuffix)
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

// sigSuffix is the detached-signature sibling suffix (spec §4.2): the armored
// publisher signature for `foo.yaml` is `foo.yaml.sig`. One spelling, owned by
// the bundle store that writes the pair.
const sigSuffix = bundles.SigSuffix

// staleSignatureError is the one message every publishing boundary gives when it
// is handed a signature that does not cover the bytes it would ship with. It
// names the remedy, because there is exactly one: re-sign, or drop the signature
// and publish unsigned. Shipping the pair is not on the menu — that is what
// makes every consumer see tampering.
func staleSignatureError(bundlePath string, err error) error {
	base := filepath.Base(bundlePath)
	name := strings.TrimSuffix(base, ".yaml")
	return fmt.Errorf("%s no longer covers %s — the bundle changed after it was signed; "+
		"re-sign with `ctxloom bundle sign %s`, or delete %s to publish it unsigned "+
		"(publishing this pair would make every consumer see tampering): %w",
		base+sigSuffix, base, name, base+sigSuffix, err)
}

// PublisherSignature is THE answer to "what signature artifact travels with
// this bundle, and does it cover the bytes about to be published?" — the one
// seam every publishing boundary asks, so that `bundle push`, `bundle move` and
// `bundle export` cannot drift apart again (which is exactly what they had
// done: move carried the sidecar, push ignored it).
//
// It returns nil for an unsigned bundle (normal, and the input to the review
// model — unsigned third-party content defaults to pending, it is never
// refused), the sidecar bytes verbatim when they cover bundleBytes, and
// staleSignatureError when they do not. Never a silent downgrade to unsigned:
// an unreadable sidecar and a non-covering one are both hard errors, because
// "publish it without the signature" is precisely the move an attacker would
// make (spec §10.2) and precisely the mistake an author would not notice.
//
// It answers only the PUBLISHER question. Whether the signer is TRUSTED is the
// consumer's decision at review time (operations.EffectiveTrust) and is
// deliberately not consulted here — a publisher must be able to ship content
// signed by a key their own machine does not trust for install.
//
// bundleBytes may be nil, in which case bundlePath is read; callers that
// already hold the exact bytes they will publish should pass them, so the
// verification and the publication cannot be looking at different files.
//
// FUTURE (excusable-flatness): when a bundle becomes a tree and signing becomes
// per-file `.sig`s plus a signed manifest-of-hashes, THIS is the function that
// grows a multi-artifact return; the publishing paths above it should not have
// to change.
func PublisherSignature(fs afero.Fs, bundlePath string, bundleBytes []byte) ([]byte, error) {
	fs = getFS(fs)
	sig, err := readSignature(fs, bundlePath)
	if err != nil || sig == nil {
		return nil, err
	}
	if bundleBytes == nil {
		bundleBytes, err = afero.ReadFile(fs, bundlePath)
		if err != nil {
			return nil, fmt.Errorf("read bundle %s: %w", bundlePath, err)
		}
	}
	if verr := signing.CoversBytes(bundleBytes, sig, signing.NamespacePublish); verr != nil {
		return nil, staleSignatureError(bundlePath, verr)
	}
	return sig, nil
}

// readSignature returns srcBundle's detached `.sig` sibling, or nil when the
// bundle is unsigned — which is normal, and never an error. A signature that
// exists but cannot be READ is an error: treating it as absent would silently
// downgrade a signed bundle to an unsigned one, which is exactly the move an
// attacker would make (spec §10.2).
func readSignature(fs afero.Fs, srcBundle string) ([]byte, error) {
	srcSig := srcBundle + sigSuffix
	exists, err := afero.Exists(fs, srcSig)
	if err != nil {
		// Absent is not the same state as unreadable. afero.Exists reports
		// (false, err) for any non-IsNotExist stat failure — EACCES on the
		// directory, an I/O error — and collapsing that into "unsigned" is
		// precisely the downgrade this function exists to prevent.
		return nil, fmt.Errorf("stat signature %s: %w", srcSig, err)
	}
	if !exists {
		return nil, nil
	}
	data, err := afero.ReadFile(fs, srcSig)
	if err != nil {
		return nil, fmt.Errorf("read signature %s: %w", srcSig, err)
	}
	return data, nil
}

// writeSignature places sig next to destBundle (a no-op returning "" when sig is
// nil), byte-for-byte — a signature is only ever copied, never regenerated. A
// signature that cannot be written IS an error: the destination would otherwise
// hold content whose trust was quietly stripped en route.
func writeSignature(fs afero.Fs, destBundle string, sig []byte) (string, error) {
	if sig == nil {
		return "", nil
	}
	destSig := destBundle + sigSuffix
	// No AllowEmpty: sig == nil already short-circuited above, so a non-nil sig
	// here is real signature bytes, never empty.
	if err := iox.WriteFileAtomicFs(fs, destSig, sig, 0644); err != nil {
		return "", fmt.Errorf("write signature %s: %w", destSig, err)
	}
	return destSig, nil
}

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
	// SigDest is where the imported bundle's detached publisher signature
	// landed, or "" when the source carried none. Import PLACES the signature
	// but never verifies it: verification belongs to the trust gate at exposure
	// (EffectiveTrust), not to the copy step.
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
	srcData, err := afero.ReadFile(fs, req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
		return nil, fmt.Errorf("invalid bundle file: %w", err)
	}

	destPath, _, err := prepareImportDest(fs, cfg, filepath.Base(req.SourcePath), req.Force)
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
	sig, err := readSignature(fs, req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("import %s: %w", req.SourcePath, err)
	}
	sigDest, err := writeSignature(fs, destPath, sig)
	if err != nil {
		return nil, fmt.Errorf("import %s: %w", req.SourcePath, err)
	}

	return &ImportBundleResult{
		Status:    "imported",
		Source:    req.SourcePath,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Commands:  len(bundle.Commands),
		MCP:       len(bundle.MCP),
		SigDest:   sigDest,
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
	srcData, err := afero.ReadFile(fs, srcManifest)
	if err != nil {
		return nil, fmt.Errorf("import %s: a directory-form bundle must carry its %s manifest: %w",
			srcDir, bundles.DirectoryFormManifest, err)
	}
	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
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

	res := &ImportBundleResult{
		Status:    "imported",
		Source:    srcDir,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Commands:  len(bundle.Commands),
		MCP:       len(bundle.MCP),
	}
	// Import PLACES a signature and never judges it (see ImportBundleResult). For
	// a tree the detached sibling travelled inside the copy, so this reports
	// where it landed rather than writing it again.
	sigDest := filepath.Join(destPath, bundles.DirectoryFormManifest+sigSuffix)
	present, err := afero.Exists(fs, sigDest)
	if err != nil {
		return nil, fmt.Errorf("import %s: cannot check for %s: %w", srcDir, sigDest, err)
	}
	if present {
		res.SigDest = sigDest
	}
	return res, nil
}

// prepareImportDest resolves and guards the path an import writes to: the given
// leaf name under this project's committed bundles directory, refusing a
// symlinked component, creating the parent, and refusing an existing
// destination unless force was given. It reports the path and whether something
// is already there.
//
// Both import forms run it, and that is the whole point. The single-file and
// tree paths differ only in what they COPY, so a guard living inside one of
// them is a guard the other silently does without — and the guards here are the
// symlink-traversal refusal and the no-clobber-without-force refusal, neither
// of which has a harmless absence.
func prepareImportDest(fs afero.Fs, cfg *config.Config, leaf string, force bool) (string, bool, error) {
	bundleDir := paths.LocalBundlesPath(cfg.GetAppPaths()[0])
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
