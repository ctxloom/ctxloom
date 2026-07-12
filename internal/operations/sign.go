// Publisher-side signing (signature-envelope spec §7A): resolving a ref to
// the local bundle FILE it names, and signing that file's exact on-disk
// bytes. This file is deliberately separate from trust.go (which owns the
// verification-side ReviewRecords/EffectiveTrust machinery) — it reuses
// trust.go's unexported ref-grammar helpers (parseTrustItemRef,
// looksLikeSourceRef, builtinSourcePrefix) directly, being in the same
// package, rather than duplicating the grammar (ADR 0032: one ref grammar).
package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// SignTarget is a ref resolved down to the local bundle it names. Spec
// §7A.1: a publisher signature covers the whole bundle FILE, so an item ref
// (<bundle>#fragments/x) resolves to its CONTAINING bundle and signs that —
// ItemNote carries what the ref actually named, for the "resolves to
// containing bundle" message the CLI prints.
type SignTarget struct {
	// BundleName is the local bundle name (as bundles.Store.Load expects:
	// no extension, no directory).
	BundleName string
	// ItemNote is "<kind>/<name>" when ref named an item within the bundle
	// ("" when ref was already a bare bundle ref).
	ItemNote string
}

// ResolveSignTarget parses ref using the SAME grammar operations/trust.go's
// parseTrustItemRef and remote.ParseReference already implement — never a
// second grammar (ADR 0032) — and resolves it to the local bundle
// `ctxloom sign` should write a `.sig` sibling for.
//
// Only LOCAL bundles resolve successfully: ctxloom has no write access to a
// remote's git tree (only that remote's own publisher can sign it), and a
// builtin bundle must never be signed at all — signing bytes compiled into
// the very binary that verifies them is circular (spec §4.5). Both are
// reported as clear, actionable errors rather than silently skipped.
func ResolveSignTarget(ref string) (SignTarget, error) {
	if ref == "" {
		return SignTarget{}, fmt.Errorf("ref is required")
	}

	if strings.Contains(ref, "#") {
		tRef, _, _, err := parseTrustItemRef(ref)
		if err != nil {
			return SignTarget{}, err
		}
		if tRef.IsBuiltin {
			return SignTarget{}, fmt.Errorf("ctxloom sign: %q is a builtin bundle — builtins are never signed "+
				"(signing bytes compiled into the binary that verifies them is circular)", ref)
		}
		if !tRef.IsLocal {
			return SignTarget{}, fmt.Errorf("ctxloom sign: %q does not resolve to a bundle you author locally — "+
				"ctxloom cannot sign a remote bundle's tree from here; only that remote's own publisher can", ref)
		}
		return SignTarget{
			BundleName: tRef.Bundle,
			ItemNote:   tRef.Kind.Dir() + "/" + tRef.Name,
		}, nil
	}

	// No "#<kind>/<name>" selector: ref is a bare bundle ref. Mirror
	// parseTrustItemRef's own base-ref resolution (same functions, same
	// package) rather than re-deriving it.
	if parsed, err := remote.ParseReference(ref); err == nil {
		if !parsed.IsLocal {
			return SignTarget{}, fmt.Errorf("ctxloom sign: %q does not resolve to a bundle you author locally — "+
				"ctxloom cannot sign a remote bundle's tree from here; only that remote's own publisher can", ref)
		}
		return SignTarget{BundleName: parsed.Path}, nil
	}
	if _, ok := strings.CutPrefix(ref, builtinSourcePrefix); ok {
		return SignTarget{}, fmt.Errorf("ctxloom sign: %q is a builtin bundle — builtins are never signed "+
			"(signing bytes compiled into the binary that verifies them is circular)", ref)
	}
	if looksLikeSourceRef(ref) {
		return SignTarget{}, fmt.Errorf("ctxloom sign: %q is not a valid canonical or ctxloom:local reference", ref)
	}
	// A bare token with no scheme marker: a plain local bundle name — the
	// overwhelmingly common case ("ctxloom sign my-tools").
	return SignTarget{BundleName: ref}, nil
}

// SignBundleRequest is the input to SignBundleFile.
type SignBundleRequest struct {
	Target SignTarget
	// Signer signs the payload; SignBundleFile never resolves one itself
	// (key discovery is internal/signing/agentkey's job, invoked by the
	// caller) so this function stays pure and trivially testable with a
	// fake signer.
	Signer ssh.Signer
	// Store overrides the default filesystem bundle store (ADR 0026); nil
	// uses bundles.NewFSStore(cfg.GetBundleDirs(), ...).
	Store bundles.Store
	// FS overrides the default OS filesystem (afero); nil uses
	// afero.NewOsFs().
	FS afero.Fs
}

// SignBundleResult reports what was signed and where the signature landed.
type SignBundleResult struct {
	BundleName string
	BundlePath string
	SigPath    string
	// ItemNote carries SignTarget.ItemNote through, for CLI display.
	ItemNote string
}

// SignBundleFile signs the EXACT bytes of a local bundle file as they exist
// on disk right now (spec §3.1: publisher payload = the bundle file bytes,
// verbatim — nothing prepended, appended, or re-serialized) and writes a
// detached sibling `<path>.sig` (spec §4.2, local filesystem bundles).
//
// Signing failure is always returned as an error — there is no code path
// here that degrades to "wrote the bundle without a .sig"; the whole
// operation either produces a verifiable signature or nothing changes on
// disk.
func SignBundleFile(cfg *config.Config, req SignBundleRequest) (*SignBundleResult, error) {
	if req.Signer == nil {
		return nil, fmt.Errorf("sign %s: no signer supplied", req.Target.BundleName)
	}

	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Target.BundleName)
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	data, err := afero.ReadFile(fs, bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("sign %s: read bundle file %s: %w", req.Target.BundleName, bundle.Path, err)
	}

	armored, err := signing.Sign(data, req.Signer, signing.NamespacePublish)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}

	sigPath := bundle.Path + ".sig"
	if err := afero.WriteFile(fs, sigPath, armored, 0o644); err != nil {
		return nil, fmt.Errorf("sign %s: write %s: %w", req.Target.BundleName, sigPath, err)
	}

	return &SignBundleResult{
		BundleName: req.Target.BundleName,
		BundlePath: bundle.Path,
		SigPath:    sigPath,
		ItemNote:   req.Target.ItemNote,
	}, nil
}

// ListLocalBundleNames returns every bundle name found in the project's
// local bundle directories (cfg.GetBundleDirs()) — the set `ctxloom sign
// --all` signs. Remote (seeded) and builtin bundles are never included:
// this project only has write access to its own locally authored bundle
// files. Sorted for deterministic --all output.
func ListLocalBundleNames(cfg *config.Config, fs afero.Fs) []string {
	fs = getFS(fs)
	var names []string
	seen := map[string]bool{}
	for _, dir := range cfg.GetBundleDirs() {
		entries, err := afero.ReadDir(fs, dir)
		if err != nil {
			continue // absent/unreadable dir: no local bundles here, not fatal
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}
