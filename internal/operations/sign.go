// Publisher-side signing (signature-envelope spec §7A): resolving a ref to
// the local bundle FILE it names, and signing that file's exact on-disk
// bytes. This file is deliberately separate from trust.go (which owns the
// verification-side ReviewRecords/EffectiveTrust machinery) — it reuses
// trust.go's unexported ref-grammar helpers (trust.ParseItemRef) directly,
// being in the same package, plus the shared remote.IsSelfContainedRef marker
// list, rather than duplicating the grammar (ADR 0032: one ref grammar).
package operations

import (
	"context"
	"errors"
	"fmt"
	fs2 "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
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
// trust.ParseItemRef and remote.ParseReference already implement — never a
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
		tRef, _, _, err := trust.ParseItemRef(ref)
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
	// trust.ParseItemRef's own base-ref resolution (same functions, same
	// package) rather than re-deriving it.
	if parsed, err := remote.ParseReference(ref); err == nil {
		if !parsed.IsLocal {
			return SignTarget{}, fmt.Errorf("ctxloom sign: %q does not resolve to a bundle you author locally — "+
				"ctxloom cannot sign a remote bundle's tree from here; only that remote's own publisher can", ref)
		}
		return SignTarget{BundleName: parsed.Path}, nil
	}
	// The RETIRED builtin source-ref spelling, "builtin:<name>", with no
	// selector. This recognizer refuses that spelling and nothing more; the
	// canonical "ctxloom+builtin:<name>" arm belongs to the slice that retires
	// the old input grammar, and ResolveSignTarget takes no catalog because a
	// publishing repo signs bundles that are not in one.
	//
	// The literal is inline deliberately: no shared constant spells a RETIRED
	// grammar, because a constant invites reuse and this spelling must not
	// spread to a new call site.
	if _, ok := strings.CutPrefix(ref, "builtin:"); ok {
		return SignTarget{}, fmt.Errorf("ctxloom sign: %q is a builtin bundle — builtins are never signed "+
			"(signing bytes compiled into the binary that verifies them is circular)", ref)
	}
	if remote.IsSelfContainedRef(ref) {
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
	// SignerSource describes where Signer came from ("git config
	// user.signingkey", "--key", ...), for the refusal AuthorizePublisher
	// raises when the repository does not authorise that key. Optional: the
	// fingerprint is always named, this says which link of the discovery chain
	// produced it — the difference between "wrong key" and "your git config is
	// pointing at your personal key".
	SignerSource string
	// Store overrides the default filesystem bundle store (ADR 0026); nil
	// uses bundles.NewFSStore(fs, cfg.GetBundleDirs()).
	Store bundles.Store
	// FS overrides the default OS filesystem (afero); nil uses
	// afero.NewOsFs().
	FS afero.Fs
}

// SignBundleResult reports what was signed and where the signature landed.
type SignBundleResult struct {
	BundleName string
	BundlePath string
	// SigPath is where the signature landed: the detached "<path>.yaml.sig"
	// sibling for a single-file bundle, or the bundle's .sigs/ STORE DIRECTORY
	// for a tree (a tree's signature filename is derived from the signature's own
	// bytes, so there is no single stable path to name).
	SigPath string
	// Tree reports that a DIRECTORY-form bundle was signed as a tree — manifest
	// plus a signature filed against it — rather than as one file's bytes. The
	// two attest different things, and a caller that printed "signed" without
	// saying which would leave an author unable to tell whether their content was
	// covered at all.
	Tree bool
	// ManifestPath is the SHA256SUMS the signature covers. Empty unless Tree.
	ManifestPath string
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
	// Checked HERE rather than in the CLI so no signing caller can bypass it —
	// `bundle push --sign` resolves its own key through the same discovery chain
	// and would otherwise inherit the exact defect this refuses. It runs before
	// the bundle is even loaded: the whole point is that nothing is written.
	if err := AuthorizePublisher(cfg, req.FS, req.Signer, req.SignerSource); err != nil {
		return nil, err
	}

	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Target.BundleName)
	if err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	if filepath.Base(bundle.Path) == bundles.DirectoryFormManifest {
		return signBundleTree(req, bundle, fs)
	}
	item := &bundleSignable{bundle: bundle, fs: fs}
	if err := SignItem(fs, item, req.Signer); err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}

	return &SignBundleResult{
		BundleName: req.Target.BundleName,
		BundlePath: bundle.Path,
		SigPath:    item.SigPath(),
		ItemNote:   req.Target.ItemNote,
	}, nil
}

// signBundleTree signs a DIRECTORY-form bundle: it refreshes the detached
// `bundle.yaml.sig` sibling beside the manifest, then builds the SHA256SUMS
// manifest over every covered file and files a publisher signature against it
// in the bundle's own .sigs/ store.
//
// BOTH, because a directory bundle is read through both. The tree attestation
// is what config.loadTreeBundle/attest.VerifyBundle check for a pulled bundle;
// the detached sibling is what bundles' own localFSReader.signatureFactsFor
// reads for an authored one, and what a publishing repo's CI checks
// (.github/verify-signatures.sh runs `ssh-keygen -Y verify` against exactly
// that file). Signing only the tree left the sibling ABSENT on a first signing
// and STALE on a re-signing — so `ctxloom bundle sign` reported success and
// every reader of the sibling went on seeing an unsigned or, worse, a
// tampered-looking bundle. Measured on ctxloom-personal's `unattended` bundle,
// where the fix was to run ssh-keygen by hand.
//
// ORDER IS LOAD-BEARING: the sibling is written FIRST, before the manifest is
// built. content.ManifestCovers exempts exactly two paths — SHA256SUMS itself
// and everything under .sigs/ — so `bundle.yaml.sig` sits at the bundle root
// and IS manifest-covered. Written after the manifest, it would be an Unclaimed
// file the manifest never mentions, and every consumer's VerifyContents would
// report the freshly signed bundle as content-added. Written first, the
// manifest covers it and the two attestations agree. There is no cycle: the
// sibling covers bundle.yaml, the manifest covers the sibling, and nothing
// covers the manifest but its own .sigs/ entry.
//
// This is not a variant spelling of the single-file path, it is the only one
// that attests anything. A directory-form bundle's content lives in files BESIDE
// bundle.yaml, so signing bundle.yaml's bytes covers the manifest and nothing
// else — and the consumer's verification path (attest.VerifyBundle, reached from
// config.loadTreeBundle for every pulled tree) reads SHA256SUMS and .sigs/ and
// never looks at a .yaml.sig sibling. Before this, `ctxloom bundle sign` on a
// directory bundle exited 0, wrote a .sig, and left every consumer reading the
// bundle as UNATTESTED: success, an artifact on disk, nothing attested.
//
// It goes through attest.SignBundle — the same object the consumer verifies —
// rather than assembling a manifest here, so the two halves cannot drift into
// disagreeing about what a signature covers.
//
// attest.SignBundle REFUSES a tree holding files no surface type recognises, and
// that refusal is wanted here: publishing is the last moment a mis-extensioned
// hook is cheap to fix, and the manifest would otherwise happily cover
// `guard.yml` by path and produce a perfectly signed bundle in which the
// guardrail does not exist.
func signBundleTree(req SignBundleRequest, bundle *bundles.Bundle, fs afero.Fs) (*SignBundleResult, error) {
	manifestPath := bundle.Path
	sibling := &bundleSignable{bundle: bundle, fs: fs}
	if err := SignItem(fs, sibling, req.Signer); err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}
	dir := filepath.Dir(manifestPath)
	store, err := content.NewTreeStore(fs, filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return nil, fmt.Errorf("sign %s: open the bundle tree at %s: %w", req.Target.BundleName, dir, err)
	}
	ctx := context.Background()
	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	if err != nil {
		return nil, fmt.Errorf("sign %s: open the bundle tree at %s: %w", req.Target.BundleName, dir, err)
	}
	if err := attest.SignBundle(ctx, store, tree, req.Signer); err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}
	return &SignBundleResult{
		BundleName:   req.Target.BundleName,
		BundlePath:   manifestPath,
		SigPath:      filepath.Join(dir, content.SigDirName),
		Tree:         true,
		ManifestPath: filepath.Join(dir, content.ManifestPath),
		ItemNote:     req.Target.ItemNote,
	}, nil
}

// ListLocalBundleNames returns every bundle name found in the project's
// authored bundle directories (cfg.GetBundleDirs() — the committed
// .ctxloom/content/bundles tree) — the set `ctxloom sign --all` signs. In a
// publishing repo that set IS the repo's shipped content, which is the whole
// point: the thing you publish must be the thing you can sign. Remote (seeded)
// and builtin bundles are never included, nor is anything in the gitignored
// cache: this project only has write access to its own authored bundle files.
// Sorted for deterministic --all output.
//
// The enumeration MIRRORS bundles.Loader.List: both bundle shapes, walked
// recursively, named by slash-joined path relative to the search dir.
// Listing only top-level `*.yaml` files skipped every DIRECTORY-form bundle —
// which is exactly the shape that can ship skills (skills.go requires
// directory form) — so a bundle with skills was unsignable via --all while
// the command reported success having signed a subset. It is mirrored rather
// than delegated because the loader's walk deliberately swallows read errors
// (a corrupt bundle must not blank a listing), whereas this set decides what
// gets SIGNED: an unreadable authored dir has to stay loud here.
func ListLocalBundleNames(cfg *config.Config, fs afero.Fs) ([]string, error) {
	fs = getFS(fs)
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, dir := range cfg.GetBundleDirs() {
		// An ABSENT dir is legitimately nothing to list; an unreadable one
		// (wrong permissions, a file where a directory should be, an I/O
		// error) is a failure to find out, and swallowing it made a
		// misconfigured GetBundleDirs indistinguishable from an empty
		// project — `sign --all` then reported "no local bundles to sign"
		// and exited 0.
		if _, err := afero.ReadDir(fs, dir); err != nil {
			if errors.Is(err, fs2.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("list local bundles: read %s: %w", dir, err)
		}
		walkErr := afero.Walk(fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				return fmt.Errorf("resolve %s under %s: %w", path, dir, relErr)
			}
			rel = filepath.ToSlash(rel)
			if info.IsDir() {
				// Directory form: <name>/bundle.yaml, at any depth.
				if rel == "." {
					return nil
				}
				if ok, _ := afero.Exists(fs, filepath.Join(path, "bundle.yaml")); ok {
					add(rel)
				}
				return nil
			}
			// File form: <name>.yaml, at any depth; bundle.yaml is the
			// directory form's manifest, already counted above.
			if strings.HasSuffix(info.Name(), ".yaml") && info.Name() != "bundle.yaml" {
				add(strings.TrimSuffix(rel, ".yaml"))
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("list local bundles: %w", walkErr)
		}
	}
	sort.Strings(names)
	return names, nil
}
