package coord

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Launch-retry tunables (lunar-boat item 1): maxLaunchAttempts,
// launchBackoffBase, launchBackoffMax stay CONSTS (the defaults) but become
// ENV-overrideable, so an operator can tune the budget without a rebuild if
// the deliberately-small default (4 attempts, ~1.4s span) turns out wrong for
// a real container daemon, without reviving the pre-fix unbounded-spin bug
// via an accidental zero.

// TestResolveLaunchTunables_UnsetEnvUsesDefaults pins red-test (a): with no
// env override present, the effective values are exactly the built-in
// consts.
func TestResolveLaunchTunables_UnsetEnvUsesDefaults(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "")
	t.Setenv(EnvLaunchBackoffBase, "")
	t.Setenv(EnvLaunchBackoffMax, "")

	maxAttempts, backoffBase, backoffMax := resolveLaunchTunables()

	assert.Equal(t, defaultMaxLaunchAttempts, maxAttempts)
	assert.Equal(t, defaultLaunchBackoffBase, backoffBase)
	assert.Equal(t, defaultLaunchBackoffMax, backoffMax)
}

// TestResolveLaunchTunables_ValidEnvOverrides pins red-test (b): a valid
// override changes the effective value away from the const default.
func TestResolveLaunchTunables_ValidEnvOverrides(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "9")
	t.Setenv(EnvLaunchBackoffBase, "500ms")
	t.Setenv(EnvLaunchBackoffMax, "1m")

	maxAttempts, backoffBase, backoffMax := resolveLaunchTunables()

	assert.Equal(t, 9, maxAttempts)
	assert.Equal(t, 500*time.Millisecond, backoffBase)
	assert.Equal(t, time.Minute, backoffMax)
	assert.NotEqual(t, defaultMaxLaunchAttempts, maxAttempts, "override must actually take effect")
	assert.NotEqual(t, defaultLaunchBackoffBase, backoffBase, "override must actually take effect")
	assert.NotEqual(t, defaultLaunchBackoffMax, backoffMax, "override must actually take effect")
}

// TestResolveLaunchTunables_GarbageIntFallsBackToDefaultWithWarning pins
// red-test (c) for the integer knob: an unparseable value must fall back to
// the default, LOUDLY, never silently to zero — a zero max-attempts would
// resurrect the unbounded-spin bug maxLaunchAttempts was added to kill.
func TestResolveLaunchTunables_GarbageIntFallsBackToDefaultWithWarning(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "not-a-number")
	t.Setenv(EnvLaunchBackoffBase, "")
	t.Setenv(EnvLaunchBackoffMax, "")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	maxAttempts, _, _ := resolveLaunchTunables()

	require.NotEqual(t, 0, maxAttempts, "an invalid override must never silently become zero")
	assert.Equal(t, defaultMaxLaunchAttempts, maxAttempts)
	assert.Contains(t, buf.String(), EnvLaunchMaxAttempts, "an invalid override must warn loudly, naming the variable")
	assert.Contains(t, buf.String(), "not-a-number")
}

// TestResolveLaunchTunables_ZeroIntFallsBackToDefaultWithWarning is the sharp
// edge of (c): a literal "0" is syntactically a valid integer but must still
// be rejected — it is exactly the value that would resurrect unbounded
// retry.
func TestResolveLaunchTunables_ZeroIntFallsBackToDefaultWithWarning(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "0")
	t.Setenv(EnvLaunchBackoffBase, "")
	t.Setenv(EnvLaunchBackoffMax, "")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	maxAttempts, _, _ := resolveLaunchTunables()

	assert.Equal(t, defaultMaxLaunchAttempts, maxAttempts, "a zero override must fall back to the default, not disable the ceiling")
	assert.Contains(t, buf.String(), EnvLaunchMaxAttempts)
}

// TestResolveLaunchTunables_GarbageDurationFallsBackToDefaultWithWarning is
// (c) for the duration knobs: an unparseable Go-duration string falls back
// to the default, loudly, never to a zero backoff.
func TestResolveLaunchTunables_GarbageDurationFallsBackToDefaultWithWarning(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "")
	t.Setenv(EnvLaunchBackoffBase, "not-a-duration")
	t.Setenv(EnvLaunchBackoffMax, "-5s")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	_, backoffBase, backoffMax := resolveLaunchTunables()

	require.NotZero(t, backoffBase, "an invalid override must never silently become a zero backoff")
	require.NotZero(t, backoffMax, "an invalid override must never silently become a zero backoff")
	assert.Equal(t, defaultLaunchBackoffBase, backoffBase)
	assert.Equal(t, defaultLaunchBackoffMax, backoffMax)
	assert.Contains(t, buf.String(), EnvLaunchBackoffBase)
	assert.Contains(t, buf.String(), EnvLaunchBackoffMax)
}

// TestNew_AppliesLaunchTunablesFromEnv is the integration check: a
// Coordinator built while an override is set actually carries the resolved
// values through to the fields the retry loop (launchgate.go) reads — not
// just that the pure resolver function computes them correctly in
// isolation.
func TestNew_AppliesLaunchTunablesFromEnv(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "7")
	t.Setenv(EnvLaunchBackoffBase, "")
	t.Setenv(EnvLaunchBackoffMax, "")

	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    newFakeSpawner(nil, nil),
	})
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, 7, c.maxLaunchAttempts)
	assert.Equal(t, defaultLaunchBackoffBase, c.launchBackoffBase)
	assert.Equal(t, defaultLaunchBackoffMax, c.launchBackoffMax)
}

// TestNew_InitialisesLaunchGateMap pins the caller invariant that
// launchGateLocked relies on: New always allocates c.launches, so the helper
// needs no lazy nil init of its own. If a future refactor drops the make in
// New, this fails loudly instead of the map assignment panicking at the first
// launch attempt.
func TestNew_InitialisesLaunchGateMap(t *testing.T) {
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    newFakeSpawner(nil, nil),
	})
	require.NoError(t, err)
	defer c.Close()

	require.NotNil(t, c.launches, "New must allocate the launch-gate map")

	c.mu.Lock()
	st := c.launchGateLocked("some-harp")
	c.mu.Unlock()
	assert.NotNil(t, st)
}
