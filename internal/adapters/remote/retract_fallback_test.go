package remote

import (
	"bytes"
	"context"
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
	const localName = "https://github.com/trent/company@bundles/incident-runbook"
	ref := &Reference{Path: "incident-runbook", ContentVersion: ""}

	newPuller := func(t *testing.T, now time.Time, seed *LockEntry) *Puller {
		t.Helper()
		fs := afero.NewMemMapFs()
		lm := NewLockfileManager("/proj/.ctxloom", WithLockfileFS(fs))
		lf := &Lockfile{Version: 1, Bundles: make(map[string]LockEntry)}
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
		fetcher.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: incident-runbook
    reason: "shipped an incorrect deploy step"
`)

		retracted, reason, checkedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
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

		fetcher := newMockFetcher() // no manifest reachable at all -> RetractionUnknown

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
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

		fetcher := newMockFetcher() // unreachable manifest -> RetractionUnknown

		retracted, reason, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
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

		fetcher := newMockFetcher()

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, _, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
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

		fetcher := newMockFetcher()

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, _, gotCheckedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
		require.NoError(t, err)
		assert.True(t, retracted, "an untimed but recorded retraction must still be honoured, not treated as absent")
		assert.True(t, gotCheckedAt.IsZero(), "resolveRetraction must not fabricate a timestamp the entry never had")
		assert.Contains(t, out.String(), "warning", "an untimed persisted verdict must warn as unknown-age")
		assert.Contains(t, out.String(), "UNKNOWN AGE")
	})

	// The warning must not NAME a cause it cannot know. An Unknown verdict is
	// ambiguous by construction (CheckRetracted's doc): "this remote publishes
	// no manifest" — the ordinary case — is indistinguishable from an outage.
	// Asserting "could not reach" sent users hunting a network fault that did
	// not exist; the production fetcher reads a LOCAL clone, so on that path
	// the claimed cause cannot apply at all. Both fallback branches are
	// checked: the stale-age one worded the cause identically and would
	// otherwise regress on its own.
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

				_, _, _, err := p.resolveRetraction(context.Background(), newMockFetcher(), "trent", "company", ref, ItemTypeBundle, localName)
				require.NoError(t, err)

				warning := out.String()
				require.Contains(t, warning, "warning",
					"this case must actually warn, or the assertions below pass over an empty buffer")
				assert.NotContains(t, warning, "could not reach",
					"the warning must not state unreachability as the cause: an Unknown verdict cannot tell an outage from a remote that simply publishes no manifest")
				assert.Contains(t, warning, "no retraction manifest",
					"the warning must offer the ordinary cause (no manifest published) alongside the unreadable one")
			})
		}
	})

	// With NOTHING recorded at all (first-ever check for this ref), there is
	// no verdict to fall back to and nothing whose age could be reported. This
	// is overwhelmingly the ordinary "this remote publishes no manifest" case
	// (see CheckRetracted's doc), not evidence of an outage — so it resolves
	// to Clean, silently, matching the long-standing default.
	t.Run("no persisted entry at all falls back to clean, unwarned", func(t *testing.T) {
		now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		p := newPuller(t, now, nil)

		fetcher := newMockFetcher()

		var out bytes.Buffer
		restore := clidiag.SetSink(&out)
		defer restore()

		retracted, reason, checkedAt, err := p.resolveRetraction(context.Background(), fetcher, "trent", "company", ref, ItemTypeBundle, localName)
		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
		assert.True(t, checkedAt.IsZero())
		assert.Empty(t, out.String(), "nothing to fall back to must not be reported as a stale warning")
	})
}
