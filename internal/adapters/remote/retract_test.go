package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/release"
)

// The retraction channel reads ONE thing: the SHA256SUMS at the tip of the
// default branch, and only what a trusted publisher signed there. These tests
// drive CheckRetracted with a stub verifier keyed by the manifest bytes, so
// each case states exactly what the verifier concluded; the real signature
// check is bundles.ManifestVerifier's, over attest.VerifyManifest.

const retractBundle = "security"

func retractRef() *Reference {
	return &Reference{URL: "https://github.com/owner/repo", Path: retractBundle}
}

func tipManifestPath() string { return retractRef().TreeRepoPath() + "/SHA256SUMS" }

// signedTip is what a verifier reports for a tip a trusted publisher signed.
func signedTip(name, version string, withdrawn string, retracts ...release.Retraction) Verified {
	return Verified{
		Release:   release.Release{Name: name, Version: semver.MustParse(version), Retracts: retracts, Withdrawn: withdrawn},
		Publisher: "pub@example.test",
	}
}

func retracts(version, reason string) release.Retraction {
	return release.Retraction{Version: semver.MustParse(version), Reason: reason}
}

// verifierFor answers each manifest's verdict by its bytes; bytes it was not
// told about are refused as tampered.
func verifierFor(byRaw map[string]Verified) ManifestVerifyFunc {
	return func(raw []byte, _ map[string][]byte) (Verified, error) {
		v, ok := byRaw[string(raw)]
		if !ok {
			return Verified{}, errors.New("tampered")
		}
		return v, nil
	}
}

func tipWith(raw string) *mockFetcher {
	mf := newMockFetcher()
	mf.files[tipManifestPath()] = []byte(raw)
	return mf
}

func pinnedAt(version string) LockEntry {
	return LockEntry{SHA: "abc", SignedVersion: version, Publisher: "pub@example.test"}
}

func TestCheckRetracted(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		fetcher *mockFetcher
		verify  ManifestVerifyFunc
		pinned  LockEntry
		want    RetractionVerdict
		reason  string
	}{
		{name: "no manifest at the tip is unknown", fetcher: newMockFetcher(),
			verify: verifierFor(nil), pinned: pinnedAt("1.0.0"), want: RetractionUnknown},
		{name: "no verifier wired is unknown, never clean", fetcher: tipWith("m"),
			verify: nil, pinned: pinnedAt("1.0.0"), want: RetractionUnknown},
		{name: "an unsigned or untrusted tip is unknown", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": {}}), pinned: pinnedAt("1.0.0"), want: RetractionUnknown},
		{name: "a tampered tip is unknown", fetcher: tipWith("garbage"),
			verify: verifierFor(nil), pinned: pinnedAt("1.0.0"), want: RetractionUnknown},
		{name: "a withdrawal retracts every version", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip(retractBundle, "2.0.0", "abandoned")}),
			pinned: pinnedAt("1.0.0"), want: RetractionRetracted, reason: "abandoned"},
		{name: "a withdrawal retracts even an unattested pin", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip(retractBundle, "2.0.0", "abandoned")}),
			pinned: LockEntry{SHA: "abc"}, want: RetractionRetracted, reason: "abandoned"},
		{name: "a retraction naming the pinned version retracts it", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip(retractBundle, "1.3.0", "", retracts("1.2.0", "broken hook"))}),
			pinned: pinnedAt("1.2.0"), want: RetractionRetracted, reason: "broken hook"},
		{name: "a retraction naming another version is clean", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip(retractBundle, "1.3.0", "", retracts("1.1.0", "x"))}),
			pinned: pinnedAt("1.2.0"), want: RetractionClean},
		// A trusted publisher's manifest for ANOTHER bundle, served at this
		// bundle's path, speaks for that bundle and not this one.
		{name: "a signed tip naming another bundle is unknown", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip("other", "9.0.0", "")}),
			pinned: pinnedAt("1.2.0"), want: RetractionUnknown},
		// The tip is older than what this project already pinned: the branch
		// was rewound. It cannot clear anything.
		{name: "a signed tip below the floor is a rollback finding", fetcher: tipWith("m"),
			verify: verifierFor(map[string]Verified{"m": signedTip(retractBundle, "1.0.0", "")}),
			pinned: pinnedAt("1.2.0"), want: RetractionRollback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, reason, err := CheckRetracted(ctx, tc.fetcher, "owner", "repo", retractRef(), tc.pinned, tc.verify)
			require.NoError(t, err)
			assert.Equal(t, tc.want, verdict)
			if tc.reason != "" {
				assert.Equal(t, tc.reason, reason)
			}
		})
	}
}

// Only the manifest's own signatures travel to the verifier: .sigs/ also holds
// per-content entries, and none of them is a signature over SHA256SUMS.
func TestCheckRetracted_HandsTheVerifierOnlyTheManifestsSignatures(t *testing.T) {
	mf := tipWith("m")
	sigDir := retractRef().TreeRepoPath() + "/.sigs"
	mf.files[sigDir+"/SHA256SUMS.publish.v1.ctxloom.dev.aa.sig"] = []byte("manifest-sig")
	mf.files[sigDir+"/deadbeef.publish.v1.ctxloom.dev.aa.sig"] = []byte("other")
	var got map[string][]byte
	verify := func(raw []byte, sigs map[string][]byte) (Verified, error) {
		got = sigs
		return Verified{}, nil
	}
	_, _, err := CheckRetracted(context.Background(), mf, "owner", "repo", retractRef(), pinnedAt("1.0.0"), verify)
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"SHA256SUMS.publish.v1.ctxloom.dev.aa.sig": []byte("manifest-sig")}, got)
}
