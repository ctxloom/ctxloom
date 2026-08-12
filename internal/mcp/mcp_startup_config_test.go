package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestLoadStartupConfig_ReportsAFailedLoadExactlyOnce pins the rule that a
// config that fails to load must be reported ONCE. The fallback fixture already
// carries the failure as a config.Warning — which printAndRecordConfigWarnings both
// prints AND records as a strictness finding — so an additional direct warn is
// not a second piece of information, it is the same sentence twice. Duplicated
// diagnostics train a reader to treat repetition as noise, which is exactly how
// the second, DIFFERENT warning on a bad startup gets skipped.
//
// The loader is injected because config.Load degrades essentially every real
// failure (unreadable file, unparseable YAML, schema violation) into a
// config.Warning and returns a nil error — so no on-disk fixture can drive the
// error branch this row is about.
func TestLoadStartupConfig_ReportsAFailedLoadExactlyOnce(t *testing.T) {
	boom := errors.New("disk on fire")
	var cfg *config.Config
	out := captureStderr(t, func() {
		cfg = loadStartupConfigWith(func(...config.LoadOption) (*config.Config, error) {
			return nil, boom
		})
	})
	require.NotNil(t, cfg, "a failed load must still yield a usable fallback config")

	n := strings.Count(out, boom.Error())
	require.Equal(t, 1, n, "the config-load failure must be reported exactly once; got %d copies:\n%s", n, out)
	require.Contains(t, out, "failed to load config", "the one report must still name what failed")
}

// TestFallbackConfigForLoadFailure_CarriesTheFailureAsItsOnlyWarning pins the
// other half: the fallback config is not merely non-nil, it is the CARRIER of
// the failure. Drop the warning and the single report above becomes zero
// reports — a config that silently loaded as empty, which is this project's
// characteristic failure mode.
func TestFallbackConfigForLoadFailure_CarriesTheFailureAsItsOnlyWarning(t *testing.T) {
	cfg := fallbackConfigForLoadFailure(errors.New("disk on fire"))
	require.NotNil(t, cfg)
	warnings := cfg.GetWarnings()
	require.Len(t, warnings, 1)
	require.Equal(t, config.WarnKindRead, warnings[0].Kind)
	require.Contains(t, warnings[0].Text, "disk on fire")
}
