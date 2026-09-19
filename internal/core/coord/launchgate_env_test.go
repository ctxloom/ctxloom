package coord

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The launch-retry budget's env overrides all obey ONE rule: unset or empty
// falls back SILENTLY (the ordinary unconfigured case), anything set but not
// positive falls back LOUDLY, and a positive value wins. These tests drive the
// integer reader and the duration reader over the same input classes so the
// two can never answer the same class differently — a silent fallback to zero
// is the unbounded-retry bug launchgate.go's header describes, reopening.

// envCase is one input class and what every reader must do with it. Every
// class here resolves to the default; they differ only in loudness.
type envCase struct {
	name  string
	raw   string
	set   bool // false: the variable is absent entirely
	warns bool
}

var envFallbackCases = []envCase{
	{name: "unset", set: false, warns: false},
	{name: "empty", raw: "", set: true, warns: false},
	{name: "unparseable", raw: "not-a-number", set: true, warns: true},
	{name: "zero", raw: "0", set: true, warns: true},
	{name: "negative", raw: "-1", set: true, warns: true},
}

const envTunableName = "CTXLOOM_TEST_LAUNCH_TUNABLE"

func TestEnvLaunchReaders_AgreeOnEveryFallbackClass(t *testing.T) {
	const defInt = 4
	const defDur = 200 * time.Millisecond

	for _, c := range envFallbackCases {
		t.Run(c.name, func(t *testing.T) {
			gotInt := withEnvTunable(t, c.raw, c.set, func() int { return envLaunchInt(envTunableName, defInt) })
			gotDur := withEnvTunable(t, c.raw, c.set, func() time.Duration { return envLaunchDuration(envTunableName, defDur) })

			assert.Equal(t, defInt, gotInt.value, "integer reader must fall back on a %s value", c.name)
			assert.Equal(t, defDur, gotDur.value, "duration reader must fall back on a %s value", c.name)
			assert.Equal(t, c.warns, gotInt.warned, "integer reader: a %s value must be %s", c.name, loudness(c.warns))
			assert.Equal(t, c.warns, gotDur.warned, "duration reader: a %s value must be %s", c.name, loudness(c.warns))
		})
	}
}

func TestEnvLaunchReaders_PositiveValueWinsSilently(t *testing.T) {
	gotInt := withEnvTunable(t, "9", true, func() int { return envLaunchInt(envTunableName, 4) })
	assert.Equal(t, 9, gotInt.value)
	assert.False(t, gotInt.warned, "an accepted value is silent")

	gotDur := withEnvTunable(t, "1500ms", true, func() time.Duration {
		return envLaunchDuration(envTunableName, 200*time.Millisecond)
	})
	assert.Equal(t, 1500*time.Millisecond, gotDur.value)
	assert.False(t, gotDur.warned, "an accepted value is silent")
}

// TestEnvLaunchReaders_WarningNamesVariableValueAndDefault: the warning is the
// whole point of the loud fallback — it must say which variable was rejected,
// what it said, and what is being used instead.
func TestEnvLaunchReaders_WarningNamesVariableValueAndDefault(t *testing.T) {
	gotInt := withEnvTunable(t, "nope", true, func() int { return envLaunchInt(envTunableName, 4) })
	assert.Contains(t, gotInt.warning, envTunableName)
	assert.Contains(t, gotInt.warning, `"nope"`)
	assert.Contains(t, gotInt.warning, "4")

	gotDur := withEnvTunable(t, "nope", true, func() time.Duration {
		return envLaunchDuration(envTunableName, 200*time.Millisecond)
	})
	assert.Contains(t, gotDur.warning, envTunableName)
	assert.Contains(t, gotDur.warning, `"nope"`)
	assert.Contains(t, gotDur.warning, "200ms", "a duration default is shown in duration syntax, not nanoseconds")
}

// TestResolveLaunchTunables_ReadsAllThreeOverrides pins the one production
// caller: each override lands on its own tunable, none bleeds into another.
func TestResolveLaunchTunables_ReadsAllThreeOverrides(t *testing.T) {
	t.Setenv(EnvLaunchMaxAttempts, "7")
	t.Setenv(EnvLaunchBackoffBase, "1s")
	t.Setenv(EnvLaunchBackoffMax, "2m")

	attempts, base, ceiling := resolveLaunchTunables()
	assert.Equal(t, 7, attempts)
	assert.Equal(t, time.Second, base)
	assert.Equal(t, 2*time.Minute, ceiling)
}

// TestResolveLaunchTunables_UnconfiguredIsTheDocumentedDefaultSet: no env, no
// warning, the built-in budget.
func TestResolveLaunchTunables_UnconfiguredIsTheDocumentedDefaultSet(t *testing.T) {
	for _, name := range []string{EnvLaunchMaxAttempts, EnvLaunchBackoffBase, EnvLaunchBackoffMax} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	attempts, base, ceiling := resolveLaunchTunables()
	assert.Equal(t, defaultMaxLaunchAttempts, attempts)
	assert.Equal(t, defaultLaunchBackoffBase, base)
	assert.Equal(t, defaultLaunchBackoffMax, ceiling)
	assert.Empty(t, buf.String(), "the unconfigured case must not warn")
}

// --- harness ---------------------------------------------------------------

type envResult[T any] struct {
	value   T
	warned  bool
	warning string
}

// withEnvTunable runs read with envTunableName set to raw (or absent when set
// is false) and the diagnostic sink captured, so a test can assert BOTH the
// resolved value and whether the fallback was loud.
func withEnvTunable[T any](t *testing.T, raw string, set bool, read func() T) envResult[T] {
	t.Helper()
	t.Setenv(envTunableName, raw) // registers the restore either way
	if !set {
		require.NoError(t, os.Unsetenv(envTunableName))
	}
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	return envResult[T]{value: read(), warned: buf.Len() > 0, warning: buf.String()}
}

func loudness(warns bool) string {
	if warns {
		return "loud"
	}
	return "silent"
}
