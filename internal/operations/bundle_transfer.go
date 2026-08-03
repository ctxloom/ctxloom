package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// ExportBundleRequest is the input for ExportBundle. Exactly one of OutputFile
// (a target file path) or DestDir (a directory the bundle's filename lands in)
// must be set.
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
	bundle, err := bundles.NewLoader(dirs, false, bundles.WithFS(fs)).Load(name)
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

	if err := afero.WriteFile(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	sigDest, err := writeSignature(fs, dest, sig)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", req.Name, err)
	}
	return &ExportBundleResult{Status: "exported", Name: req.Name, Source: bundle.Path, Dest: dest, SigDest: sigDest}, nil
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
		"re-sign with `ctxloom sign %s`, or delete %s to publish it unsigned "+
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
	if err := afero.WriteFile(fs, destSig, sig, 0644); err != nil {
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

// ImportBundle validates a bundle file — its name (*.yaml, since that is all
// the loader ever looks for) and its content (bundles.ParseBundle refuses a
// document that declares nothing) — and copies it into the project's
// committed content bundles directory (symlink-guarded, like CreateBundle),
// carrying its detached `.sig` sibling when one exists. Refuses to overwrite
// without Force.
func ImportBundle(_ context.Context, cfg *config.Config, req ImportBundleRequest) (*ImportBundleResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
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

	bundleDir := paths.LocalBundlesPath(cfg.GetAppPaths()[0])
	destPath := filepath.Join(bundleDir, filepath.Base(req.SourcePath))
	if err := requireSafeBundlePath([]string{bundleDir}, destPath); err != nil {
		return nil, err
	}
	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	// The error matters: a destination that cannot be STATTED (permissions, a
	// broken symlink) used to read as "does not exist" and was overwritten
	// without --force — the one outcome this guard exists to prevent. Matches
	// ImportProfile, which already reads it.
	exists, err := afero.Exists(fs, destPath)
	if err != nil {
		return nil, fmt.Errorf("cannot check whether %s already exists: %w", destPath, err)
	}
	if exists && !req.Force {
		return nil, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}
	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
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
