package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// RefusedAdvance is one pin `deps upgrade` DECLINED to move, because the
// content at the commit it would have advanced to carries a publisher
// signature that does not verify over those bytes.
//
// It is a REPORT, not a failure: the lockfile keeps the entry it already had,
// so the consumer goes on being served the last content that verified. The
// caller is obliged to say so — see the note on UpgradeResult.Refused.
type RefusedAdvance struct {
	// Identity is the canonical ref of the item whose pin was not moved.
	Identity string `json:"identity"`
	// KeptSHA is the commit the pin stays at — the last one that verified.
	KeptSHA string `json:"kept_sha"`
	// ProposedSHA is the commit the constraint resolved to and that was
	// refused.
	ProposedSHA string `json:"proposed_sha"`
	// Detail is the verification failure, in the words of the verifier, so a
	// human is told WHY rather than just that something was refused.
	Detail string `json:"detail"`
}

// verifyAdvance decides whether upgrade may move a pin onto ref at proposed.
//
// THE RULE, decided by the human (taskloom unearned-cornea) and narrower than
// "verify everything": an advance is refused when the proposed content carries
// a publisher signature that FAILS to verify — signing.ErrSignatureTampered,
// the one outcome signing.VerifyPublisher calls never benign. It is NOT refused
// for unsigned content or for content signed by a key this machine does not
// trust to publish: VerifyPublisher reports both as the quiet ("", nil)
// "unsigned to you", they take the review path, and `ctxloom review` really
// does list them. Those a human can act on; a signature that does not cover its
// bytes is deliberately not reviewable (bundles.Reason.NeedsReview), so
// advancing onto it strands the user with nothing — the new copy withheld as
// tampered and the old copy unreachable past the moved pin.
//
// It is fail-closed in both directions it can be: any error reading either half
// of the signed pair refuses the advance, because "I could not check" is not
// "it checks out". The single exception is a MISSING signature, which is not an
// error at all but how "this bundle is unsigned" is spelled at the transport
// (remote.BundleReader.ReadBundleSignature, spec §4.1/§10.1) — and refusing
// every unsigned advance would be a different decision than the one taken.
//
// A non-bundle (a remote parent profile) is never refused here: publisher
// signatures cover bundle files, and a profile has no detached sibling to check.
func verifyAdvance(ctx context.Context, cfg *config.Config, factory remote.FetcherFactory, auth remote.AuthConfig, p PinnedRef) (detail string, refuse bool) {
	if p.Type != remote.ItemTypeBundle {
		return "", false
	}
	ref, err := remote.ParseReference(p.Identity)
	if err != nil || !ref.IsCanonical() {
		// Nothing addressable to fetch a signature for. This is not a signature
		// failure and must not be reported as one; an unaddressable ref is the
		// exposure gate's problem (bundles.ReasonUnaddressable), not upgrade's.
		return "", false
	}

	// Read through a TREE-AWARE reader pinned to the PROPOSED sha, not by
	// hand-fetching a ".sig" sibling of the ref's path.
	//
	// A bundle is a TREE: its envelope is <root>/bundle.yaml and its signature
	// is <root>/bundle.yaml.sig INSIDE that tree. A sibling of the ref path
	// ("<root>.sig") does not exist and never will, so the sibling fetch
	// returned not-found for every bundle — which this function reads as
	// "unsigned, let it through". The effect was that `deps upgrade` could not
	// refuse a single stale or tampered signature advance: a trust check that
	// silently stopped checking.
	//
	// The lockfile here is SYNTHETIC and one entry wide because the pin being
	// verified is the PROPOSED one, which by definition is not what the real
	// lockfile holds yet.
	rdr := advanceReader(factory, auth, p.Identity, ref.URL, p.Hash)
	sig, err := rdr.ReadBundleSignature(ctx, p.Identity)
	if err != nil {
		// ABSENT IS NOT BROKEN, and it arrives spelled TWO ways.
		//
		// errs.ErrRemoteContentNotFound is the transport's "no such blob".
		// content.ErrNotFound is what the TREE probe returns when the bundle's
		// directory is wholly absent at this sha — a different layer's sentinel
		// for the same fact, by deliberate layering in remotetree.
		//
		// Matching only the first is what made this refuse every advance whose
		// bundle simply had no tree at the proposed commit: the "unsigned is
		// legal" branch could never be reached, so ordinary unsigned content
		// was reported as a signature that could not be read.
		if errors.Is(err, errs.ErrRemoteContentNotFound) || errors.Is(err, content.ErrNotFound) {
			// No signature at the proposed commit: unsigned content, which is
			// legal and ordinary. Let the advance through — the trust gate
			// decides exposure, and `ctxloom review` can act on it.
			return "", false
		}
		return fmt.Sprintf("its signature could not be read at %s: %v", p.Hash, err), true
	}

	body, err := rdr.ReadBundleBytes(ctx, p.Identity)
	if err != nil {
		// A signature exists but the bytes it claims to cover cannot be read.
		// Nothing can be verified, so nothing may be advanced onto.
		return fmt.Sprintf("it carries a signature but its bytes could not be read at %s: %v", p.Hash, err), true
	}

	if _, verr := signing.VerifyPublisher(body, sig, cfg.TrustRoot(), time.Now()); verr != nil {
		return verr.Error(), true
	}
	return "", false
}

// advanceReader is a tree-aware byte source pinned to ONE proposed sha.
//
// It exists because verification happens BEFORE the advance: the sha being
// checked is not in the lockfile yet, so the project's ordinary reader — which
// resolves everything through the pinned lockfile — cannot reach it. A synthetic
// one-entry lock is the smallest honest way to say "read this bundle, at this
// commit, and nothing else".
//
// The tree fetcher is composed here for the same reason NewBundleReaderForConfig
// composes it: the walker lives in the content layer, above remote.
func advanceReader(factory remote.FetcherFactory, auth remote.AuthConfig, identity, url, sha string) *remote.BundleReader {
	return remote.NewBundleReader(nil, factory, auth,
		&remote.Lockfile{
			Version: 1,
			Bundles: map[string]remote.LockEntry{identity: {SHA: sha, URL: url}},
		},
		remote.WithReaderTreeFetcher(remotetree.PullTreeFetcher),
	)
}
