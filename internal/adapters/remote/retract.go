package remote

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// RetractionVerdict is CheckRetracted's three-valued outcome. A bool cannot
// express "I could not determine this" without conflating it with one of the
// two knowable answers — that conflation used to mean content whose
// retraction status is unknowable was delivered as though positively
// cleared. See docs/trust-model.md's fail-stale policy for how callers must
// treat RetractionUnknown.
type RetractionVerdict int

const (
	// RetractionClean means a trusted publisher's signed release at the tip
	// neither withdraws the bundle nor retracts the pinned version.
	RetractionClean RetractionVerdict = iota
	// RetractionRetracted means it does (see the accompanying reason string).
	RetractionRetracted
	// RetractionUnknown means no verdict could be established on THIS call:
	// the tip could not be read, or nothing a trusted publisher signed is
	// there. Callers MUST NOT treat Unknown as Clean. The fail-stale policy
	// is: fall back to the last verdict this project itself recorded for the
	// ref, not "assume cleared".
	RetractionUnknown
	// RetractionRollback means the tip IS signed by a trusted publisher, at a
	// version below the one this project pinned: the branch was rewound. It
	// is a finding in its own right, and like Unknown it never clears a
	// recorded verdict — the caller falls back to it and says why.
	RetractionRollback
)

// RetractionStaleAfter is how old a PERSISTED retraction verdict may get
// before a caller falling back to it (because the remote could not be
// reached) must warn that the answer may be out of date. This is a HUMAN
// DECISION (fail-stale with a 14-day warn threshold), not a derived value —
// it is not tuned from sync cadence or fetch-latency data, so do not
// "optimise" it.
const RetractionStaleAfter = 14 * 24 * time.Hour

// ManifestVerifyFunc verifies a bundle's SHA256SUMS fetched on its own, with
// the signature files filed against it in the bundle's .sigs/ directory
// (keyed by file name). It is wired in from above for the same reason
// TreeVerifyFunc is: the signed-manifest format and the trust root live in
// layers remote cannot import.
//
// It returns an error for bytes that are not a manifest or whose signature
// does not cover them, and Verified{} with a nil error for a manifest no key
// this machine trusts to publish has signed.
type ManifestVerifyFunc func(manifest []byte, sigFiles map[string][]byte) (Verified, error)

// CheckRetracted asks the newest release of ref's bundle whether the pinned
// content has been withdrawn.
//
// It reads ONE thing: the SHA256SUMS at the tip of the default branch, with
// its signatures — never the tree, never an unsigned index. Only a release a
// trusted publisher signed can retract. Any key trusted to publish may (ruled:
// not only the key that signed the pin), because the question a retraction
// answers is "has a publisher you trust withdrawn this", and a co-maintainer's
// withdrawal is that answer.
//
//   - A tip signed by a trusted key, for THIS bundle: Retracted when it is
//     withdrawn or its retractions name pinned.SignedVersion; Clean otherwise.
//   - A signed tip whose version is BELOW pinned.SignedVersion:
//     RetractionRollback. The branch was rewound to an older release; it
//     says nothing about what was retracted since, so it clears nothing.
//   - Anything else — no manifest, unreachable, unsigned, signed by nobody
//     trusted, tampered, or a trusted manifest for ANOTHER bundle served at
//     this path — is RetractionUnknown, and the caller's fail-stale policy
//     applies (docs/trust-model.md).
//
// Why unsigned is Unknown and not Clean: a retraction is the only channel a
// publisher has to withdraw content they already SIGNED, and whoever controls
// the repository can serve any unsigned bytes they like. If an unsigned tip
// could clear a verdict, stripping the signature would strip the retraction.
func CheckRetracted(ctx context.Context, fetcher Fetcher, owner, repo string, ref *Reference, pinned LockEntry, verify ManifestVerifyFunc) (RetractionVerdict, string, error) {
	if err := ctx.Err(); err != nil {
		return RetractionUnknown, "", err
	}
	if verify == nil {
		return RetractionUnknown, "", nil
	}
	v, ok := fetchVerifiedTip(ctx, fetcher, owner, repo, ref.TreeRepoPath(), verify)
	if !ok {
		return RetractionUnknown, "", nil
	}
	var pv *semver.Version
	if pinned.SignedVersion != "" {
		var err error
		pv, err = semver.StrictNewVersion(pinned.SignedVersion)
		if err != nil {
			return RetractionUnknown, "", fmt.Errorf("the lockfile records signed_version %q for %s, which is not strict semver: %w", pinned.SignedVersion, ref.String(), err)
		}
		if v.Release.Version.LessThan(pv) {
			return RetractionRollback, fmt.Sprintf("the newest signed release at %s/%s is %s, below the %s this project pinned", owner, repo, v.Release.Version, pv), nil
		}
	}
	if retracted, why := v.Release.Retracted(pv); retracted {
		return RetractionRetracted, why, nil
	}
	return RetractionClean, "", nil
}

// fetchVerifiedTip reads and verifies the tip manifest for the bundle rooted
// at root on the default branch. false — Unknown — when it cannot be read,
// does not verify to a publisher and version, or names another bundle.
func fetchVerifiedTip(ctx context.Context, fetcher Fetcher, owner, repo, root string, verify ManifestVerifyFunc) (Verified, bool) {
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return Verified{}, false
	}
	raw, err := fetcher.FetchFile(ctx, owner, repo, root+"/"+tipManifestName, branch)
	if err != nil {
		return Verified{}, false
	}
	v, err := verify(raw, tipManifestSignatures(ctx, fetcher, owner, repo, root, branch))
	if err != nil || v.Publisher == "" || v.Release.Version == nil {
		return Verified{}, false
	}
	if v.Release.Name != path.Base(root) {
		return Verified{}, false
	}
	return v, true
}

// tipManifestName is the bundle manifest's file name and its signatures' key
// in .sigs/ (content.ManifestPath and content.BundleSigKey — remote cannot
// import content, and the verifier re-derives which entries it trusts, so a
// mismatch here only fails closed).
const tipManifestName = "SHA256SUMS"

// tipManifestSignatures fetches the .sigs/ entries filed against the manifest.
// A missing or unreadable directory is no signatures: the verifier then
// reports the manifest unattested, which is Unknown, never Clean.
func tipManifestSignatures(ctx context.Context, fetcher Fetcher, owner, repo, root, branch string) map[string][]byte {
	sigDir := root + "/.sigs"
	entries, err := fetcher.ListDir(ctx, owner, repo, sigDir, branch)
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir || !strings.HasPrefix(e.Name, tipManifestName+".") {
			continue
		}
		data, err := fetcher.FetchFile(ctx, owner, repo, sigDir+"/"+e.Name, branch)
		if err != nil {
			continue
		}
		out[e.Name] = data
	}
	return out
}
