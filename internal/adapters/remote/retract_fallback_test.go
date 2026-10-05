package remote

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestResolveRetraction_FailStale covers Puller.resolveRetraction, the
// caller-side half of the fail-stale fix: CheckRetracted's fetch-failure
// branches report RetractionUnknown rather than "clean", and
// resolveRetraction is what turns
// that into a decision — fall back to the last verdict this project itself
// recorded, honoring it even when it says RETRACTED, and warning when that
// verdict is stale.
func TestResolveRetraction_FailStale(t *testing.T) {
	const localName = "ctxloom+git://github.com/trent/company//bundles/incident-runbook"
	ref := &Reference{Path: "incident-runbook", ContentVersion: ""}

	newPuller := func(t *testing.T, now time.Time, seed *LockEntry) *Puller {
		t.Helper()
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/proj/.ctxloom", WithLockfileFS(fs))
		lf := &Lockfile{Version: 1, Bundles: make(map[trust.BundleKey]LockEntry)}
		if seed != nil {
			lf.AddEntry(ItemTypeBundle, localName, *seed)
		}
		require.NoError(t, lm.Save(lf))
		return &Puller{
			lockfileManager: lm,
			now:             func() time.Time { return now },
		}
	}

	t.Run("a fresh verdict is honoured", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, nil)

		fetcher := newMockFetcher()
		fetcher.defaultBranch = "main"
		fetcher.files[ref.TreeRepoPath()+"/SHA256SUMS"] = []byte("tip")
		p.manifestVerify = verifierFor(map[string]Verified{"tip": signedTip("incident-runbook", "2.0.0", "shipped an incorrect deploy step")})

		retracted, reason, checkedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, retracted, "a fresh manifest read reporting retracted must be honoured directly, no fallback involved")
		assert.Equal(t, "shipped an incorrect deploy step", reason)
		assert.True(t, checkedAt.Equal(now), "a fresh verdict is stamped with the current time, not carried forward")
	})

	t.Run("an unreachable remote falls back to the persisted verdict", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		checkedAt := now.Add(-2 * 24 * time.Hour) // 2 days old: well inside the 14-day window
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			Retracted: false, RetractionCheckedAt: checkedAt,
		})

		fetcher := unreadableTip() // the tip read fails -> RetractionUnknown

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.False(t, retracted, "the persisted (clean) verdict must be what's delivered, not a fresh guess")
		assert.True(t, gotCheckedAt.Equal(checkedAt), "the fallback reports the PERSISTED check time, not now")
		assert.Empty(t, out.String(), "a fallback well inside the freshness window must not warn")
	})

	// SECURITY-CRITICAL: an attacker who can partition a developer from the
	// remote (or simply an outage) must NOT be able to resurrect content the
	// publisher already retracted merely by making the retraction check fail.
	// This is the exact bug fail-open (today's pre-fix behavior) had: a fetch
	// failure silently reported "not retracted" regardless of what was known.
	t.Run("a RETRACTED persisted verdict is still honoured when the remote is unreachable", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		checkedAt := now.Add(-2 * 24 * time.Hour)
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			Retracted: true, RetractedReason: "shipped an incorrect deploy step",
			RetractionCheckedAt: checkedAt,
		})

		fetcher := unreadableTip() // the tip read fails -> RetractionUnknown

		retracted, reason, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, retracted, "an unreachable remote must NOT resurrect content the publisher already retracted")
		assert.Equal(t, "shipped an incorrect deploy step", reason)
		assert.True(t, gotCheckedAt.Equal(checkedAt))
	})

	t.Run("a verdict older than 14 days warns", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		checkedAt := now.Add(-15 * 24 * time.Hour) // just past RetractionStaleAfter
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			Retracted: true, RetractedReason: "shipped an incorrect deploy step",
			RetractionCheckedAt: checkedAt,
		})

		fetcher := unreadableTip()

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, _, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, retracted, "staleness warns but still honors the last known verdict — it never becomes fail-closed")
		assert.Contains(t, out.String(), "warning", "a verdict older than the 14-day threshold must warn")
	})

	// An entry written by an older ctxloom has NO RetractionCheckedAt at all
	// (the field didn't exist yet). That is UNKNOWN AGE — neither "just
	// checked" nor implicitly "safe" — so it warns unconditionally, same as an
	// explicitly stale entry, while still honoring whatever verdict WAS
	// recorded (here: retracted).
	t.Run("an entry with no timestamp reads as unknown-age and warns, but is still honoured", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			Retracted: true, RetractedReason: "shipped an incorrect deploy step",
			// RetractionCheckedAt deliberately left zero: pre-migration entry.
		})

		fetcher := unreadableTip()

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, retracted, "an untimed but recorded retraction must still be honoured, not treated as absent")
		assert.True(t, gotCheckedAt.IsZero(), "resolveRetraction must not fabricate a timestamp the entry never had")
		assert.Contains(t, out.String(), "warning", "an untimed persisted verdict must warn as unknown-age")
		assert.Contains(t, out.String(), "UNKNOWN AGE")
	})

	// The warning must not NAME a cause it cannot know. Asserting "could not
	// reach" sent users hunting a network fault that did not exist; the
	// production fetcher reads a LOCAL clone, so on that path the claimed
	// cause cannot apply at all. Both fallback branches are checked: the
	// stale-age one worded the cause identically and would otherwise regress
	// on its own.
	t.Run("the fallback warning does not assert the remote was unreachable", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		for _, tc := range []struct {
			name  string
			entry *LockEntry
		}{
			{"unknown age", &LockEntry{
				SHA: "abc123", URL: "https://github.com/trent/company",
				Retracted: true, RetractedReason: "shipped an incorrect deploy step",
			}},
			{"stale age", &LockEntry{
				SHA: "abc123", URL: "https://github.com/trent/company",
				Retracted:           true,
				RetractedReason:     "shipped an incorrect deploy step",
				RetractionCheckedAt: now.Add(-30 * 24 * time.Hour),
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				p := newPuller(t, now, tc.entry)

				var out bytes.Buffer
				restore := clidiag.SetSink(&out)
				defer restore()

				_, _, _, err := p.resolveRetraction(context.Background(), unreadableTip(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
				require.NoError(t, err)

				warning := out.String()
				require.Contains(t, warning, "warning",
					"this case must actually warn, or the assertions below pass over an empty buffer")
				assert.NotContains(t, warning, "could not reach",
					"the warning must not state unreachability as the cause: a failed tip read says nothing about the network")
			})
		}
	})

	// A first-ever check against a remote that publishes no manifest resolves
	// to not-retracted, silently, stamped with the time that check RAN: a zero
	// stamp would be persisted as indistinguishable from an entry written
	// before check times were tracked, and every later run would then warn
	// about a verdict of "unknown age" that is in fact a day old.
	t.Run("no persisted entry and no manifest resolves unretracted, stamped now, and the next run does not warn", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, nil)

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, reason, checkedAt, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
		assert.True(t, checkedAt.Equal(now), "the verdict is stamped with when the check ran, not left zero")
		assert.Empty(t, out.String(), "nothing to fall back to must not be reported as a stale warning")

		_, err = p.updateLockfile(localName, PullOptions{ItemType: ItemTypeBundle}, &Remote{URL: "https://github.com/trent/company"},
			"abc123", "", "", "", retracted, reason, checkedAt, Verified{})
		require.NoError(t, err)

		p.now = func() time.Time { return now.Add(24 * time.Hour) }
		_, _, again, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, again.Equal(now.Add(24*time.Hour)), "the second run checks again and is stamped with when it ran")
		assert.Empty(t, out.String(), "a day-old verdict from a no-manifest remote is not of unknown age")
	})

	// Attack (b), stripping: whoever controls the repository replaces the
	// signed tip that retracted this pin with an UNSIGNED one that does not.
	// An unsigned tip says nothing, so the recorded verdict stands.
	t.Run("a stripped retraction does not clear a recorded one", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company", SignedVersion: "1.0.0",
			Retracted: true, RetractedReason: "leaked token", RetractionCheckedAt: now.Add(-time.Hour),
		})
		fetcher := newMockFetcher()
		fetcher.files[ref.TreeRepoPath()+"/SHA256SUMS"] = []byte("unsigned")
		p.manifestVerify = verifierFor(map[string]Verified{"unsigned": {}})

		retracted, reason, _, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, pinnedAt("1.0.0"))
		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Equal(t, "leaked token", reason)
	})

	// Attack (b), rewinding: the tip is moved back to the release this project
	// pinned — genuinely signed, at the floor, and from before the retraction
	// existed. A signed retraction of an exact version is permanent, so no
	// later clean tip lifts it for the same pin; only moving the pin does.
	t.Run("an older signed manifest cannot hide a newer retraction", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company", SignedVersion: "1.0.0",
			Retracted: true, RetractedReason: "leaked token", RetractionCheckedAt: now.Add(-time.Hour),
		})
		fetcher := newMockFetcher()
		fetcher.files[ref.TreeRepoPath()+"/SHA256SUMS"] = []byte("old")
		p.manifestVerify = verifierFor(map[string]Verified{"old": signedTip("incident-runbook", "1.0.0", "")})

		retracted, reason, _, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, pinnedAt("1.0.0"))
		require.NoError(t, err)
		assert.True(t, retracted, "a clean tip must not lift a signed retraction already recorded for this pin")
		assert.Equal(t, "leaked token", reason)
	})

	t.Run("a tip below the floor is reported as a rollback and keeps the recorded verdict", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company", SignedVersion: "1.2.0",
			Retracted: true, RetractedReason: "leaked token", RetractionCheckedAt: now.Add(-time.Hour),
		})
		fetcher := newMockFetcher()
		fetcher.files[ref.TreeRepoPath()+"/SHA256SUMS"] = []byte("rewound")
		p.manifestVerify = verifierFor(map[string]Verified{"rewound": signedTip("incident-runbook", "1.0.0", "")})

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()
		retracted, _, _, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, pinnedAt("1.2.0"))
		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Contains(t, out.String(), "rolled back")
	})
}

// TestResolveRetraction_EveryRunChecks pins the owner's ruling on retraction
// checks: the check runs on every call; a remote that publishes no retraction
// manifest has answered and is never a warning, however long ago the verdict
// was first recorded; and a check that could not run is still reported.
func TestResolveRetraction_EveryRunChecks(t *testing.T) {
	const localName = "ctxloom+git://github.com/trent/company//bundles/incident-runbook"
	ref := &Reference{Path: "incident-runbook"}
	tip := ref.TreeRepoPath() + "/SHA256SUMS"
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	newPuller := func(t *testing.T, seed *LockEntry) *Puller {
		t.Helper()
		lm := NewLockfileManager("/proj/.ctxloom", WithLockfileFS(afero.NewMemMapFs()))
		lf := &Lockfile{Version: 1, Bundles: make(map[trust.BundleKey]LockEntry)}
		if seed != nil {
			lf.AddEntry(ItemTypeBundle, localName, *seed)
		}
		require.NoError(t, lm.Save(lf))
		return &Puller{
			lockfileManager: lm,
			now:             func() time.Time { return now },
			manifestVerify:  verifierFor(nil),
		}
	}
	capture := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var out bytes.Buffer
		t.Cleanup(clidiag.SetSink(&out))
		return &out
	}

	t.Run("a manifest-less remote never warns, however old its last check", func(t *testing.T) {
		p := newPuller(t, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			RetractionCheckedAt: now.Add(-10 * RetractionStaleAfter),
		})
		out := capture(t)

		retracted, _, checkedAt, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.False(t, retracted)
		assert.True(t, checkedAt.Equal(now), "a check that ran and found no manifest is stamped with when it ran")
		assert.Empty(t, out.String(), "a remote that publishes no retraction manifest is not a warning")
	})

	// Deleting the manifest is something whoever controls the repository can
	// do; it must not lift a retraction a trusted publisher already signed.
	t.Run("a manifest removed after a retraction keeps the recorded retraction", func(t *testing.T) {
		p := newPuller(t, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			Retracted: true, RetractedReason: "leaked token",
			RetractionCheckedAt: now.Add(-time.Hour),
		})

		retracted, reason, _, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, retracted, "an absent manifest says nothing that could clear a recorded retraction")
		assert.Equal(t, "leaked token", reason)
	})

	// A pin signed at a release proves the remote once published a manifest.
	// Its disappearance is reported on every pull — there is no state to make
	// it once — and the recorded verdict stands.
	t.Run("a signed pin whose remote stopped publishing a manifest warns every pull", func(t *testing.T) {
		p := newPuller(t, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company", SignedVersion: "1.2.0",
			Retracted: true, RetractedReason: "leaked token",
			RetractionCheckedAt: now.Add(-time.Hour),
		})
		pinned := LockEntry{SignedVersion: "1.2.0"}
		want := fmt.Sprintf(manifestWithdrawnWarning, localName, "trent", "company")
		for call := 1; call <= 2; call++ {
			out := capture(t)
			retracted, reason, _, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName, pinned)
			require.NoError(t, err)
			assert.True(t, retracted, "call %d: the recorded retraction still applies", call)
			assert.Equal(t, "leaked token", reason)
			assert.Contains(t, out.String(), want, "call %d warns", call)
		}
	})

	t.Run("the check runs on every call, not only the first", func(t *testing.T) {
		p := newPuller(t, nil)
		fetcher := newMockFetcher()

		_, _, first, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		_, err = p.updateLockfile(localName, PullOptions{ItemType: ItemTypeBundle}, &Remote{URL: "https://github.com/trent/company"},
			"abc123", "", "", "", false, "", first, Verified{})
		require.NoError(t, err)

		later := now.Add(3 * RetractionStaleAfter)
		p.now = func() time.Time { return later }
		_, _, second, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)

		assert.Equal(t, []string{tip, tip}, fetcher.fetched, "each call reads the tip manifest itself")
		assert.True(t, second.Equal(later), "the second check is stamped with when IT ran, so it never goes stale")
	})

	t.Run("a stale verdict whose re-check could not run is reported", func(t *testing.T) {
		p := newPuller(t, &LockEntry{
			SHA: "abc123", URL: "https://github.com/trent/company",
			RetractionCheckedAt: now.Add(-2 * RetractionStaleAfter),
		})
		out := capture(t)

		_, _, checkedAt, err := p.resolveRetraction(context.Background(), unreadableTip(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.True(t, checkedAt.Equal(now.Add(-2*RetractionStaleAfter)), "a check that did not run must not refresh the stamp")
		assert.NotEmpty(t, out.String(), "a check that could not run is reported")
	})

	t.Run("a first check that could not run is reported and left unstamped", func(t *testing.T) {
		p := newPuller(t, nil)
		out := capture(t)

		retracted, _, checkedAt, err := p.resolveRetraction(context.Background(), unreadableTip(), "trent", "company", ref, ItemTypeBundle, localName, LockEntry{})
		require.NoError(t, err)
		assert.False(t, retracted)
		assert.True(t, checkedAt.IsZero(), "no check established this verdict, so no check time is claimed for it")
		assert.NotEmpty(t, out.String(), "a check that could not run is reported even with nothing recorded")
	})
}
