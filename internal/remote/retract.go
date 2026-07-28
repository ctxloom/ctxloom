package remote

import (
	"context"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// RetractionVerdict is CheckRetracted's three-valued outcome. A bool cannot
// express "I could not determine this" without conflating it with one of the
// two knowable answers — that conflation is U088-F01, U095-F02's
// fetch-failure half, and U150-F04's shared root cause: content whose
// retraction status is unknowable was delivered as though positively
// cleared. See docs/trust-model.md's fail-stale policy for how callers must
// treat RetractionUnknown.
type RetractionVerdict int

const (
	// RetractionClean means the manifest was read successfully and ref is not
	// listed as retracted in it.
	RetractionClean RetractionVerdict = iota
	// RetractionRetracted means the manifest was read successfully and ref IS
	// listed as retracted in it (see the accompanying reason string).
	RetractionRetracted
	// RetractionUnknown means no verdict could be established on THIS call:
	// the remote could not be reached (repo/branch/manifest fetch failed) or —
	// indistinguishably at this seam, see the doc below — the repo simply
	// publishes no manifest at all (the ordinary case; most remotes don't).
	// Callers MUST NOT treat Unknown as Clean. The fail-stale policy is: fall
	// back to the last verdict this project itself recorded for the ref, not
	// "assume cleared".
	RetractionUnknown
)

// RetractionStaleAfter is how old a PERSISTED retraction verdict may get
// before a caller falling back to it (because the remote could not be
// reached) must warn that the answer may be out of date. This is a HUMAN
// DECISION (fail-stale with a 14-day warn threshold), not a derived value —
// it is not tuned from sync cadence or fetch-latency data, so do not
// "optimise" it.
const RetractionStaleAfter = 14 * 24 * time.Hour

// CheckRetracted checks if a version is retracted in the manifest.
//
// "Could not determine" is NOT "not retracted" (U150-F04). A retraction is the
// only channel a publisher has to withdraw content they already SIGNED, so a
// fault on this path that resolves to a clean bill of health lets the
// withdrawal lose to the publisher's own signature. The two answers must not
// share a return value — hence RetractionVerdict rather than a bool.
//
// The faults here are not equally knowable, and this function is honest about
// which is which:
//
//   - An unparseable manifest is UNAMBIGUOUS: the file was fetched, it simply
//     does not parse. There is no reading of that under which the publisher
//     retracted nothing, so it is an error (verdict RetractionUnknown, err
//     non-nil) — this half was already fixed and is unchanged here.
//   - A fetch failure is AMBIGUOUS at this seam: Fetcher returns an
//     undifferentiated error, so a repo that publishes no manifest (the
//     ordinary case — most do not) is indistinguishable from a network fault
//     or a revoked token. Telling them apart needs a not-found sentinel on
//     the Fetcher interface, a cross-cutting change to every implementation
//     this fix does not make. Instead it reports RetractionUnknown (err nil)
//     and pushes the distinction to the CALLER, which — unlike this
//     function — knows whether it has ever recorded a verdict for this ref
//     before: fall back to that verdict when one exists (warning if it is
//     stale — RetractionStaleAfter — or has no recorded check time at all),
//     and default to Clean, un-warned, when there is nothing to fall back to
//     (matching the long-standing "most remotes publish no manifest"
//     default). See Puller.resolveRetraction, the caller-side half of this
//     fix, and docs/trust-model.md.
func CheckRetracted(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, itemType ItemType) (RetractionVerdict, string, error) {
	// Try to fetch manifest
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return RetractionUnknown, "", nil // Could not reach the remote at all.
	}

	manifestPath := paths.RepoContentPrefix + "/manifest.yaml"
	content, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err != nil {
		// Ambiguous at this seam (see the doc comment above): "no manifest
		// published" (the ordinary case) and "could not reach this one file"
		// are indistinguishable here without a not-found sentinel on Fetcher.
		// Reporting Unknown rather than Clean pushes the distinction to the
		// caller, which CAN tell them apart (it knows whether it has ever
		// recorded a verdict for this ref before) — see the fail-stale policy
		// in docs/trust-model.md.
		return RetractionUnknown, "", nil
	}

	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return RetractionUnknown, "", fmt.Errorf("retraction manifest %s in %s/%s could not be parsed, so whether this content has been retracted is UNKNOWN: %w",
			manifestPath, owner, repo, err)
	}

	// Check retracted entries. A retraction entry with an empty Version retracts
	// the item at every version; an entry pinned to a specific version only fires
	// when the request asks for that exact version. The earlier `ref.ContentVersion
	// == ""` disjunct was wrong: it flagged any unversioned/"latest" install as
	// retracted on the FIRST retracted version of that name, even when the
	// retracted version was not the one being installed.
	for _, r := range manifest.Retracted {
		if r.Type == itemType && r.Name == ref.Path {
			if r.Version == "" || r.Version == ref.ContentVersion {
				return RetractionRetracted, r.Reason, nil
			}
		}
	}

	return RetractionClean, "", nil
}
