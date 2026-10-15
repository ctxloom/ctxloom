package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseLimits(t *testing.T, body string) (ContainerLimits, error) {
	t.Helper()
	cfg, err := ParseConfig([]byte("schema_version: 7\n" + body))
	if err != nil {
		return ContainerLimits{}, err
	}
	return cfg.IsolationLimits(), nil
}

// Unset keys leave both limits to the isolation adapter's host-derived
// default: config carries no number of its own for them.
func TestIsolationLimits_UnsetIsDefault(t *testing.T) {
	got, err := parseLimits(t, "")
	require.NoError(t, err)
	assert.Equal(t, ContainerLimits{}, got)
	assert.Equal(t, LimitDefault, got.Memory.Kind)
	assert.Equal(t, LimitDefault, got.PIDs.Kind)
}

// A size reads with docker's binary units (k/m/g/t, 1024-based, optional
// b/ib suffix, any case), a bare number is bytes, and a decimal is allowed.
func TestIsolationLimits_MemoryAcceptsSizes(t *testing.T) {
	for raw, want := range map[string]int64{
		"8g":           8 << 30,
		"8G":           8 << 30,
		"8GiB":         8 << 30,
		"8gb":          8 << 30,
		"512m":         512 << 20,
		"512 MiB":      512 << 20,
		"1.5g":         3 << 29,
		"1t":           1 << 40,
		"7340032":      7 << 20,
		"\"7340032b\"": 7 << 20,
		"6144k":        6 << 20,
	} {
		got, err := parseLimits(t, "isolation_memory_limit: "+raw+"\n")
		require.NoError(t, err, raw)
		assert.Equal(t, ContainerLimit{Kind: LimitFixed, Value: want}, got.Memory, raw)
	}
}

// 0 and unlimited (any case) switch the cap off EXPLICITLY — distinct from
// unset, which takes the default.
func TestIsolationLimits_ZeroAndUnlimitedDisable(t *testing.T) {
	for _, raw := range []string{"0", "\"0\"", "unlimited", "Unlimited", "0g"} {
		got, err := parseLimits(t, "isolation_memory_limit: "+raw+"\nisolation_pids_limit: "+raw+"\n")
		if raw == "0g" {
			// 0g is a size of zero bytes: the memory cap is off; it is not a
			// pids count.
			require.ErrorIs(t, err, ErrInvalidContainerLimit, raw)
			continue
		}
		require.NoError(t, err, raw)
		assert.Equal(t, ContainerLimit{Kind: LimitUnlimited}, got.Memory, raw)
		assert.Equal(t, ContainerLimit{Kind: LimitUnlimited}, got.PIDs, raw)
	}
	got, err := parseLimits(t, "isolation_memory_limit: 0g\n")
	require.NoError(t, err)
	assert.Equal(t, ContainerLimit{Kind: LimitUnlimited}, got.Memory)
}

func TestIsolationLimits_PidsAcceptsACount(t *testing.T) {
	got, err := parseLimits(t, "isolation_pids_limit: 2048\n")
	require.NoError(t, err)
	assert.Equal(t, ContainerLimit{Kind: LimitFixed, Value: 2048}, got.PIDs)

	got, err = parseLimits(t, "isolation_pids_limit: 1\n")
	require.NoError(t, err)
	assert.Equal(t, ContainerLimit{Kind: LimitFixed, Value: 1}, got.PIDs, "the smallest count is a cap")
}

// Any spelling of zero switches the pids cap off, as any zero size does the
// memory cap: never a "fixed" cap of nothing.
func TestIsolationLimits_PidsAnyZeroIsOff(t *testing.T) {
	for _, raw := range []string{`"00"`, `"+0"`, `"-0"`} {
		got, err := parseLimits(t, "isolation_pids_limit: "+raw+"\n")
		require.NoError(t, err, raw)
		assert.Equal(t, ContainerLimit{Kind: LimitUnlimited}, got.PIDs, raw)
	}
}

// A malformed or unusable value is refused at load, never defaulted: a typo
// that silently became "no cap" (or the default) would run agents under a
// limit nobody configured.
func TestIsolationLimits_RefusesBadValues(t *testing.T) {
	for _, body := range []string{
		"isolation_memory_limit: lots\n",
		"isolation_memory_limit: 8x\n",
		"isolation_memory_limit: -1g\n",
		"isolation_memory_limit: 1m\n", // below the runtimes' minimum
		"isolation_memory_limit: 5242879\n",
		"isolation_memory_limit: 99999999999t\n", // overflows int64 bytes
		"isolation_memory_limit: 8388608t\n",     // exactly 2^63 bytes: one past int64
		"isolation_pids_limit: -1\n",
		"isolation_pids_limit: 1.5\n",
		"isolation_pids_limit: many\n",
		"isolation_pids_limit: 4k\n",
	} {
		_, err := parseLimits(t, body)
		require.ErrorIs(t, err, ErrInvalidContainerLimit, body)
	}
}

// The smallest memory cap docker accepts loads; one byte less is refused.
func TestIsolationLimits_MemoryFloorIsTheRuntimeMinimum(t *testing.T) {
	got, err := parseLimits(t, "isolation_memory_limit: 6m\n")
	require.NoError(t, err)
	assert.Equal(t, int64(MinContainerMemoryBytes), got.Memory.Value)

	_, err = parseLimits(t, "isolation_memory_limit: 6291455\n")
	require.ErrorIs(t, err, ErrInvalidContainerLimit)
}

// The limits round-trip through the persisted document unchanged.
func TestIsolationLimits_RoundTripThroughFixture(t *testing.T) {
	cfg := NewFixture(Fixture{IsolationMemoryLimit: "4g", IsolationPidsLimit: "unlimited"})
	back := NewFixture(cfg.ToFixture())
	assert.Equal(t, ContainerLimits{
		Memory: ContainerLimit{Kind: LimitFixed, Value: 4 << 30},
		PIDs:   ContainerLimit{Kind: LimitUnlimited},
	}, back.IsolationLimits())
}
