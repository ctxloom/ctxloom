// Stage 1 of the canonical-signing feature: a polymorphic signing seam.
// Today the ONLY implementation is bundleSignable, wired under
// SignBundleFile so publisher-signing a bundle is byte-identical to before
// this file existed (spec §3.1/§4.2). Later stages add further Signable
// kinds (e.g. a skill's canonical exposure surface) without touching the
// signing mechanics below — that is the whole point of the seam.
package operations

import (
	"fmt"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Signable is the seam every publisher-signable thing implements: it names
// its own kind (from the shared item-kind taxonomy, internal/trust — never a
// parallel enum), the exact bytes a publisher signature covers, and where
// the detached ".sig" sibling lives.
type Signable interface {
	// Kind names what is being signed, from trust.ItemKind.
	//
	// PLACEMENT NOTE (escalate, do not restructure): trust.ItemKind today has
	// no member for "a whole bundle file" — only fragment/prompt/mcp/hook/skill,
	// which address items WITHIN a bundle (see internal/trust/trust.go). Rather
	// than add a new package-level constant to internal/trust (a different
	// package, and a taxonomy decision bigger than this behavior-preserving
	// refactor), bundleSignable.Kind() below returns the literal
	// trust.ItemKind("bundle") — the existing TYPE, no new type introduced,
	// just no declared constant for this value yet. Whether "bundle" belongs
	// in internal/trust as a real constant (and what it should mean for
	// EffectiveTrust, which never gates a bundle-as-a-whole today) is a stage-2
	// placement question for a human to decide, not something to resolve by
	// restructuring trust.go here.
	Kind() trust.ItemKind
	// PublisherPreimage returns the EXACT bytes a publisher signature must
	// cover under signing.NamespacePublish (spec §3.1: unframed, verbatim —
	// nothing prepended, appended, or re-serialized).
	PublisherPreimage() ([]byte, error)
	// SigPath returns the detached ".sig" sibling path to write/read (spec
	// §4.2, local filesystem bundles).
	SigPath() string
}

// bundleSignable adapts a loaded bundle to Signable. Its preimage is the
// bundle file's exact on-disk bytes and its sig sibling is
// bundle.Path + bundles.SigSuffix — exactly what SignBundleFile computed
// inline before this seam existed.
type bundleSignable struct {
	bundle *bundles.Bundle
	fs     afero.Fs
}

func (b *bundleSignable) Kind() trust.ItemKind {
	// See the PLACEMENT NOTE on Signable.Kind: no trust.ItemKind constant
	// names "a whole bundle" yet.
	return trust.ItemKind("bundle")
}

func (b *bundleSignable) PublisherPreimage() ([]byte, error) {
	data, err := afero.ReadFile(b.fs, b.bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("read bundle file %s: %w", b.bundle.Path, err)
	}
	return data, nil
}

func (b *bundleSignable) SigPath() string {
	return b.bundle.Path + bundles.SigSuffix
}

// SignItem signs item's publisher preimage with signer and writes the
// armored detached signature to item.SigPath() at 0644 — the one factored-out
// operation every Signable kind shares. This is exactly the sequence
// SignBundleFile ran inline before this seam existed: read preimage bytes,
// signing.Sign under signing.NamespacePublish, write the armored blob to the
// ".sig" sibling.
//
// Signing failure is always returned as an error — there is no code path
// that degrades to "wrote without a .sig"; the whole operation either
// produces a verifiable signature or nothing changes on disk.
func SignItem(fs afero.Fs, item Signable, signer ssh.Signer) error {
	if signer == nil {
		return fmt.Errorf("sign: no signer supplied")
	}
	preimage, err := item.PublisherPreimage()
	if err != nil {
		return err
	}
	armored, err := signing.Sign(preimage, signer, signing.NamespacePublish)
	if err != nil {
		return err
	}
	sigPath := item.SigPath()
	// No AllowEmpty: armored is signing.Sign's output, never empty.
	if err := iox.WriteFileAtomicFs(fs, sigPath, armored, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", sigPath, err)
	}
	return nil
}
