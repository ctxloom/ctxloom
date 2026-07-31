package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedLegacyIndex writes an index whose started_at is parseable but NOT in
// canonical RFC3339Nano form, so loadLocked's upgrade pipeline has real work
// and stages a pending upgrade.
func seedLegacyIndex(t *testing.T, harpName string) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.yaml")
	body := "sessions:\n" +
		"  - harp_name: " + harpName + "\n" +
		"    project_dir: /proj\n" +
		"    started_at: 2026-05-28 16:57:20.781317+00:00\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	return m, path
}

// TestManager_PendingUpgradeSurvivesInterveningReads measures the concurrency
// clause of U099-F16, whose text is truncated in the findings index at "so a
// concurrent `Fin…`". Reconstructed against the code, the concern is that
// loadLocked unconditionally nils pendingUpgrade, so a read landing between a
// caller's Load and its CommitUpgrade would discard the staged upgrade and the
// user's consent would commit nothing.
//
// It does not fire, and this pins why. loadLocked nils and then RE-STAGES from
// the file's current bytes, and every touch of pendingUpgrade -- the nil, the
// re-stage, PendingUpgrade's read -- happens under the same m.mu, so no caller
// can observe the intermediate. An intervening read leaves the pending upgrade
// exactly where it was.
func TestManager_PendingUpgradeSurvivesInterveningReads(t *testing.T) {
	const harpName = "swift-amber-falcon"
	m, path := seedLegacyIndex(t, harpName)

	_, err := m.Load()
	require.NoError(t, err)
	// The fixture must be hostile: without a genuinely staged upgrade there is
	// nothing for an intervening read to lose.
	require.NotNil(t, m.PendingUpgrade(), "the seeded index must stage an upgrade")

	// The read named in the truncated clause.
	got, err := m.Find(harpName)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NotNil(t, m.PendingUpgrade(),
		"an intervening read must not discard the staged upgrade: loadLocked re-stages from the file's current bytes")

	require.NoError(t, m.CommitUpgrade())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "16:57:20.781317+00:00",
		"the consented upgrade must actually have been written")
	require.Nil(t, m.PendingUpgrade(), "and nothing stays pending afterwards")
}

// TestManager_MutationCanonicalizesRatherThanLosingTheUpgrade is the other
// half. A mutation between Load and CommitUpgrade DOES clear pendingUpgrade --
// but only because saveLocked has already written canonical form, so there is
// genuinely nothing left to consent to. Nothing is lost; the write self-heals.
func TestManager_MutationCanonicalizesRatherThanLosingTheUpgrade(t *testing.T) {
	const harpName = "swift-amber-falcon"
	m, path := seedLegacyIndex(t, harpName)

	_, err := m.Load()
	require.NoError(t, err)
	require.NotNil(t, m.PendingUpgrade(), "the seeded index must stage an upgrade")

	require.NoError(t, m.BindSession(harpName, "sess-1", ""))

	require.Nil(t, m.PendingUpgrade(), "the write cleared the pending upgrade")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "16:57:20.781317+00:00",
		"because it canonicalized the file itself, rather than dropping the upgrade")
}
