package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// An engine's NoLegacyHistoryReason and its constructed backend's History()
// are two statements of one fact — the descriptor's is the one every reader
// consults, the backend's is what a legacy leg would actually call — and
// they must agree for every registered engine: a reason with a live History
// hides a working leg, a nil History with no reason is the silent miss.
func TestNoLegacyHistoryReason_AgreesWithEveryBackendsHistory(t *testing.T) {
	names := List()
	require.NotEmpty(t, names)
	retired := 0
	for _, name := range names {
		reason := NoLegacyHistoryReason(name)
		history := Get(name).History()
		if reason != "" {
			retired++
			assert.Nil(t, history, "%s declares its legacy scraper retired (%q) but still constructs a History", name, reason)
		} else {
			assert.NotNil(t, history, "%s constructs no History yet declares no reason — the retirement is undeclared", name)
		}
	}
	assert.GreaterOrEqual(t, retired, 1, "at least one shipped engine's scraper was retired")
	assert.ElementsMatch(t, retiredNames(t), RetiredScraperBackendNames())
	assert.Empty(t, NoLegacyHistoryReason("never-registered"), "an unregistered name keeps its legacy leg by default")
}

func retiredNames(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range List() {
		if NoLegacyHistoryReason(name) != "" {
			out = append(out, name)
		}
	}
	return out
}

// IsTestOnly is a thin read over the Distribution enum: a double is hidden
// from every user-facing enumeration by declaring DistributionTestOnly, and
// nothing else reads as test-only — not an OptIn engine, not an unknown name.
func TestIsTestOnly_ReadsTheDistributionEnum(t *testing.T) {
	for _, name := range List() {
		assert.Equal(t, DistributionFor(name) == agent.DistributionTestOnly, IsTestOnly(name), name)
	}
	assert.False(t, IsTestOnly("never-registered"))
	assert.Equal(t, agent.DistributionUnset, DistributionFor("never-registered"))
}
