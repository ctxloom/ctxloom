package container

import (
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedAssets_CallerCannotCorruptThem is the isolation contract every
// accessor owes: each call hands back a private copy, so a caller that writes
// through the returned slice cannot change what the NEXT reader of that asset
// sees. The assets are the source of truth for every image build and probe in a
// run, and a corrupted one would surface as an unexplained build failure far
// from its cause.
func TestEmbeddedAssets_CallerCannotCorruptThem(t *testing.T) {
	for name, read := range map[string]func() []byte{
		"Base":         Base,
		"Entrypoint":   Entrypoint,
		"ProbeSeccomp": ProbeSeccomp,
	} {
		pristine := read()
		require.NotEmpty(t, pristine, "%s", name)

		mine := read()
		mine[0] = 'X'

		assert.Equal(t, pristine, read(), "%s: a caller's write must not reach the next reader", name)
	}
}

// TestAssetFrom_EmptyAssetPanics is the truncation guard. The first assertion
// pins WHY it is needed: a 0-byte file reads as SUCCESS with zero bytes, so
// without the guard an empty asset would be delivered as a legitimate payload —
// a zero-instruction build context, an empty entrypoint script, or an empty
// seccomp document, each failing (or silently weakening confinement) far from
// its cause.
func TestAssetFrom_EmptyAssetPanics(t *testing.T) {
	truncated := fstest.MapFS{"base/Containerfile": &fstest.MapFile{Data: nil}}

	b, err := fs.ReadFile(truncated, "base/Containerfile")
	require.NoError(t, err, "a truncated asset reads as success — this is the silent path")
	require.Empty(t, b)

	assert.PanicsWithValue(t,
		`container: embedded asset "base/Containerfile" is empty — the file in the container/ package is truncated`,
		func() { assetFrom(truncated, "base/Containerfile") },
		"an empty asset must fail at the asset, not downstream")

	assert.Panics(t, func() { assetFrom(truncated, "nope") }, "an unreadable asset must fail loudly too")
}

// TestEmbeddedAssets_PayloadsAreShapedRight is the payload assertion behind the
// guard: every asset must actually carry its content, not merely be present.
func TestEmbeddedAssets_PayloadsAreShapedRight(t *testing.T) {
	assert.Contains(t, string(Base()), "FROM ", "the base Containerfile must carry at least one build instruction")

	entrypoint := string(Entrypoint())
	assert.True(t, len(entrypoint) > 2 && entrypoint[:2] == "#!", "the entrypoint script must carry a shebang")

	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []any  `json:"syscalls"`
	}
	require.NoError(t, json.Unmarshal(ProbeSeccomp(), &profile), "the probe seccomp profile must be valid JSON")
	assert.Equal(t, "SCMP_ACT_ERRNO", profile.DefaultAction, "the probe profile stays a TIGHT default, never unconfined")
	assert.NotEmpty(t, profile.Syscalls, "an empty allowlist would confine the probe to nothing")
}
