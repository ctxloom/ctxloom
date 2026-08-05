package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// RefusedAdvance is one pin `remote upgrade` DECLINED to move, because the
// content at the commit it would have advanced to carries a publisher
// signature that does not verify over those bytes.
//
// It is a REPORT, not a failure: the lockfile keeps the entry it already had,
// so the consumer goes on being served the last content that verified. The
// caller is obliged to say so — see the note on UpgradeResult.Refused.
type RefusedAdvance struct {
	// Identity is the canonical ref of the item whose pin was not moved.
	Identity string
	// KeptSHA is the commit the pin stays at — the last one that verified.
	KeptSHA string
	// ProposedSHA is the commit the constraint resolved to and that was
	// refused.
	ProposedSHA string
	// Detail is the verification failure, in the words of the verifier, so a
	// human is told WHY rather than just that something was refused.
	Detail string
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

	sig, err := fetchRefSibling(ctx, factory, auth, ref, p.Hash, remote.SignatureSuffix)
	if err != nil {
		if errors.Is(err, errs.ErrRemoteContentNotFound) {
			// No .sig at the proposed commit: unsigned content, which is legal
			// and ordinary. Let the advance through — the trust gate decides
			// exposure, and `ctxloom review` can act on it.
			return "", false
		}
		return fmt.Sprintf("its signature could not be read at %s: %v", p.Hash, err), true
	}

	body, err := remote.FetchRefBytes(ctx, factory, auth, ref, p.Hash)
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

// fetchRefSibling reads the file that sits beside ref's own file at the same
// commit, under the given suffix — the detached-signature carrier
// (remote.SignatureSuffix), whose whole point is that the signature and the
// bytes it covers can never come from different commits.
//
// It is remote.FetchRefBytes plus a suffix. The duplication is deliberate and
// small: FetchRefBytes' contract is "the bytes of this ref", and a sibling is
// not that ref.
func fetchRefSibling(ctx context.Context, factory remote.FetcherFactory, auth remote.AuthConfig, ref *remote.Reference, sha, suffix string) ([]byte, error) {
	if sha == "" {
		// Same floor as FetchRefBytes: every Fetcher resolves "" to the default
		// branch tip, which would check a signature at a commit nobody pinned.
		return nil, fmt.Errorf("refusing to fetch %s%s: no SHA pinned", ref.String(), suffix)
	}
	fetcher, err := factory(ref.URL, auth)
	if err != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", ref.URL, err)
	}
	owner, repo, err := remote.ParseOwnerRepo(ref.URL)
	if err != nil {
		return nil, fmt.Errorf("parse repo URL %s: %w", ref.URL, err)
	}
	filePath := ref.BuildFilePath(ref.ItemType) + suffix
	data, err := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	if err != nil {
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, sha, err)
	}
	return data, nil
}
