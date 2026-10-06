package operations

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// RefusalCause is why `deps upgrade` declined to move a pin. It is persisted in
// the refused-advances record, which is why it is a named value rather than
// implied by the record's existence.
type RefusalCause string

// RefusalUnreadable: the proposed content could not be read as a bundle —
// fetched, opened, checked or parsed — by the reader every consumer uses, so
// moving the pin onto it would leave the consumer with nothing.
const RefusalUnreadable RefusalCause = "unreadable"

// RefusedAdvance is one pin `deps upgrade` DECLINED to move, because the
// content at the commit it would have advanced to could not be read.
//
// It is a REPORT, not a failure: the lockfile keeps the entry it already had,
// so the consumer goes on being served the content it already had. The
// caller is obliged to say so — see the note on UpgradeResult.Refused.
type RefusedAdvance struct {
	// Identity is the canonical ref of the item whose pin was not moved.
	Identity string `json:"identity"`
	// KeptSHA is the commit the pin stays at.
	KeptSHA string `json:"kept_sha"`
	// ProposedSHA is the commit the constraint resolved to and that was
	// refused.
	ProposedSHA string `json:"proposed_sha"`
	// Detail is the read failure, in the reader's own words, so a human is
	// told WHY rather than just that something was refused.
	Detail string `json:"detail"`
	// Cause is why the advance was refused.
	Cause RefusalCause `json:"cause"`
}

// verifyAdvance decides whether a lock writer may move a pin onto p: the
// proposed content must be readable by the one reader every consumer uses
// (bundles.ReadRemoteRef), because moving a pin onto content that reader
// refuses leaves the consumer with nothing — the new copy refused and the old
// copy unreachable past the moved pin.
//
// Content that is not there at all, or that nobody this machine trusts
// signed, carries nothing to refuse: it is not less readable at the proposed
// pin than at the kept one.
//
// A non-bundle (a remote parent profile) is never refused here: only a bundle
// has a tree to read.
//
// A non-nil error is the refusal, in the reader's own words.
func verifyAdvance(ctx context.Context, cfg *config.Config, factory remote.FetcherFactory, auth remote.AuthConfig, p PinnedRef) error {
	if p.Type != remote.ItemTypeBundle {
		return nil
	}
	ref, err := remote.ParseReference(string(p.Identity))
	if err != nil || !ref.IsCanonical() {
		return nil
	}
	_, _, err = bundles.ReadRemoteRef(ctx, factory, auth, ref, p.Hash, remotetree.PullTreeFetcher, cfg.TrustRoot())
	switch {
	case err == nil,
		errors.Is(err, errs.ErrRemoteContentNotFound) || errors.Is(err, content.ErrNotFound),
		errors.Is(err, bundles.ErrTreeUnattested):
		return nil
	default:
		return err
	}
}
