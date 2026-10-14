package configload

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// The two container-limit keys pass the schema in every spelling the parser
// accepts — a size or count, as YAML string or integer, 0 and unlimited — and
// reach the loaded Config.
func TestLoad_IsolationLimits_AcceptedSpellings(t *testing.T) {
	for _, body := range []string{
		"isolation_memory_limit: 8g\nisolation_pids_limit: 2048\n",
		"isolation_memory_limit: 512 MiB\nisolation_pids_limit: \"2048\"\n",
		"isolation_memory_limit: 7340032\n",
		"isolation_memory_limit: 0\nisolation_pids_limit: 0\n",
		"isolation_memory_limit: unlimited\nisolation_pids_limit: Unlimited\n",
	} {
		cfg := loadYAML(t, "schema_version: 7\n"+body)
		assert.Empty(t, schemaViolations(t, cfg), body)
		assert.Empty(t, unknownKeyWarnings(cfg), body)
	}
	cfg := loadYAML(t, "schema_version: 7\nisolation_memory_limit: 8g\nisolation_pids_limit: 2048\n")
	assert.Equal(t, config.ContainerLimits{
		Memory: config.ContainerLimit{Kind: config.LimitFixed, Value: 8 << 30},
		PIDs:   config.ContainerLimit{Kind: config.LimitFixed, Value: 2048},
	}, cfg.IsolationLimits())
}

// A value the schema cannot read is a schema violation named by its key.
func TestLoad_IsolationLimits_RejectedSpellings(t *testing.T) {
	for body, key := range map[string]string{
		"isolation_memory_limit: lots\n":   "isolation_memory_limit",
		"isolation_memory_limit: -1\n":     "isolation_memory_limit",
		"isolation_memory_limit: [8g]\n":   "isolation_memory_limit",
		"isolation_pids_limit: 4k\n":       "isolation_pids_limit",
		"isolation_pids_limit: -5\n":       "isolation_pids_limit",
		"isolation_pids_limit: true\n":     "isolation_pids_limit",
		"isolation_memory_limit: 8 gigs\n": "isolation_memory_limit",
	} {
		cfg := loadYAML(t, "schema_version: 7\n"+body)
		got := schemaViolations(t, cfg)
		require.Len(t, got, 1, "%s: warnings %+v", body, cfg.GetWarnings())
		assert.Equal(t, key, got[0].KeyPath, body)
	}
}
