// Version-pinned profile-ref tests verify that a profile's "@<commit>" suffix
// (on bundle_items cherry-picks, on whole bundles, and on fragment refs) is
// threaded through assembly to the loader's version-aware resolution — so the
// pinned historical version is what assembles, gated by ITS OWN content hash —
// while an unversioned ref keeps resolving to the lockfile-pinned default.
package operations

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// cqVersionRef is the canonical bundle ref of the acme "cq" bundle used by these
// tests; its repo (trustRepo) is registered UNtrusted so only explicit grants
// expose content — exactly the surface a pinned version is gated against.
const cqVersionRef = acmeBundle + "cq" // https://github.com/acme/repo@bundles/cq

// versionPinnedLoader builds an exposure-style loader over the acme/cq bundle:
// a seeded lockfile-default bundle, a fake version resolver serving per-commit
// bundles (an absent commit errors, simulating a fetch failure), and a real
// trust contentGate resolving against store + an untrusted acme remote. It is
// the operations-level analogue of bundles.versionedLoader.
func versionPinnedLoader(t *testing.T, store *trust.Store, def *bundles.Bundle, versions map[string]*bundles.Bundle) (*bundles.Loader, *config.Config) {
	t.Helper()
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow

	resolver := func(_canonical, commit string) (*bundles.Bundle, error) {
		b, ok := versions[commit]
		if !ok {
			return nil, fmt.Errorf("fake resolver: no commit %q", commit)
		}
		clone := *b // copy so the loader's Name stamp doesn't clobber the fixture
		return &clone, nil
	}

	l := bundles.NewLoader(nil, true,
		bundles.WithSeededBundles(map[string]*bundles.Bundle{cqVersionRef: def}),
		bundles.WithVersionResolver(resolver),
		bundles.WithTrustGate(gate))
	return l, cfg
}

// profileCfg returns cfg with a single inline profile of the given fields wired
// as the loader's cfg (so AssembleContext resolves the profile and the gate
// share one config).
func profileCfg(cfg *config.Config, name string, p config.Profile) *config.Config {
	cfg.Profiles = config.ProfilesConfig{Definitions: map[string]config.Profile{name: p}}
	return cfg
}

// TestVersionPinned_BundleItem_ResolvesHistoricalVersion proves a profile
// bundle_items cherry-pick of "<ref>@<commit>:fragments/x" assembles that
// commit's content (granted), distinct from the lockfile default.
func TestVersionPinned_BundleItem_ResolvesHistoricalVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V1-BODY"}}},
	}
	store := newTrustStore(t)
	// Trust the PINNED version's content (its own hash) — `ctxloom trust` on it.
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/solid", fragHash("V1-BODY"), ""))
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "pinned", config.Profile{
		BundleItems: []string{cqVersionRef + "@c1:fragments/solid"},
	})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "pinned", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "V1-BODY", "pinned cherry-pick assembles the historical version")
	assert.NotContains(t, res.Context, "DEFAULT-BODY", "the lockfile default must NOT be used when pinned")
}

// TestVersionPinned_FragmentRef_ResolvesHistoricalVersion proves the third
// version-honoring field: a profile `fragments:` ref carrying "@<commit>" on its
// bundle part assembles that commit's content (granted), not the default.
func TestVersionPinned_FragmentRef_ResolvesHistoricalVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V1-BODY"}}},
	}
	store := newTrustStore(t)
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/solid", fragHash("V1-BODY"), ""))
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "fragpin", config.Profile{
		Fragments: []config.FragmentRef{{Name: cqVersionRef + "@c1#fragments/solid"}},
	})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "fragpin", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "V1-BODY", "a pinned fragments: ref assembles the historical version")
	assert.NotContains(t, res.Context, "DEFAULT-BODY")
}

// TestVersionPinned_UnversionedRefUnchanged proves an unversioned cherry-pick
// keeps resolving to the lockfile-pinned default (today's behavior, untouched).
func TestVersionPinned_UnversionedRefUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V1-BODY"}}},
	}
	store := newTrustStore(t)
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/solid", fragHash("DEFAULT-BODY"), ""))
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "plain", config.Profile{
		BundleItems: []string{cqVersionRef + ":fragments/solid"},
	})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "plain", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "DEFAULT-BODY", "an unversioned ref resolves the lockfile default")
	assert.NotContains(t, res.Context, "V1-BODY")
}

// TestVersionPinned_WholeBundle_PinsAllItems proves "bundles: [<ref>@<commit>]"
// pins EVERY item resolved from that bundle to the commit (the pinned version's
// fragment set, distinct from the default).
func TestVersionPinned_WholeBundle_PinsAllItems(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{
			"alpha": {Content: "ALPHA-V1"},
			"beta":  {Content: "BETA-V1"},
		}},
	}
	store := newTrustStore(t)
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/alpha", fragHash("ALPHA-V1"), ""))
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/beta", fragHash("BETA-V1"), ""))
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "wholepin", config.Profile{
		Bundles: []string{cqVersionRef + "@c1"},
	})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "wholepin", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "ALPHA-V1", "whole-bundle pin enumerates and resolves the commit's items")
	assert.Contains(t, res.Context, "BETA-V1")
	assert.NotContains(t, res.Context, "DEFAULT-BODY", "the default version's items must not leak in")
}

// TestVersionPinned_GateEvaluatesPinnedHash proves the trust gate keys on the
// PINNED version's content hash: an un-granted pinned version is withheld
// (surfaced content-free under the version-less ref), and a `ctxloom trust` grant
// of that version's hash then exposes it.
func TestVersionPinned_GateEvaluatesPinnedHash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"solid": {Content: "DEFAULT-BODY"}}}
	versions := map[string]*bundles.Bundle{
		"c2": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "V2-BODY"}}},
	}
	store := newTrustStore(t) // no grant yet for V2-BODY
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "pinned2", config.Profile{
		BundleItems: []string{cqVersionRef + "@c2:fragments/solid"},
	})

	// Un-granted pinned version → withheld, content-free under the version-less ref.
	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "pinned2", Loader: loader})
	require.NoError(t, err)
	assert.NotContains(t, res.Context, "V2-BODY", "an un-granted pinned version must be withheld")
	assert.Equal(t, []string{cqVersionRef + "#fragments/solid"}, loader.Withheld(),
		"the withheld pinned version is tallied under its version-less ref")

	// `ctxloom trust` of the pinned version's own hash exposes it (fresh loader so
	// the version cache + withheld set don't carry the prior decision).
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/solid", fragHash("V2-BODY"), ""))
	loader2, _ := versionPinnedLoader(t, store, def, versions)
	res2, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "pinned2", Loader: loader2})
	require.NoError(t, err)
	assert.Contains(t, res2.Context, "V2-BODY", "granting the pinned version's hash exposes it")
}

// TestVersionPinned_FetchFailureWithholdsOnlyThatItem proves a per-version fetch
// failure drops ONLY the unresolvable item (fail-closed) while a sibling pinned
// to a resolvable commit still assembles.
func TestVersionPinned_FetchFailureWithholdsOnlyThatItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Disable always-on companion builtins so FragmentsLoaded is exactly the
	// profile's items regardless of what's on the test host's PATH.
	defer config.SetLookPathForTesting(func(string) (string, error) {
		return "", fmt.Errorf("not installed")
	})()
	def := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{
		"good": {Content: "DEFAULT-GOOD"},
		"bad":  {Content: "DEFAULT-BAD"},
	}}
	versions := map[string]*bundles.Bundle{
		"c1": {Fragments: map[string]bundles.BundleFragment{"good": {Content: "GOOD-V1"}}},
		// "broken" is intentionally absent ⇒ the fake resolver errors.
	}
	store := newTrustStore(t)
	require.NoError(t, store.SetAccepted(trustRepo, "cq#fragments/good", fragHash("GOOD-V1"), ""))
	loader, cfg := versionPinnedLoader(t, store, def, versions)
	cfg = profileCfg(cfg, "mixed", config.Profile{
		BundleItems: []string{
			cqVersionRef + "@c1:fragments/good",
			cqVersionRef + "@broken:fragments/bad",
		},
	})

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "mixed", Loader: loader})
	require.NoError(t, err, "a per-version fetch failure must never abort assembly")
	assert.Contains(t, res.Context, "GOOD-V1", "the resolvable pinned item still assembles")
	assert.NotContains(t, res.Context, "DEFAULT-BAD", "the unresolvable item is withheld, not silently defaulted")
	assert.Equal(t, []string{cqVersionRef + "#fragments/good"}, res.FragmentsLoaded)
}
