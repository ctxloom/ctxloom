package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// launchBackoff's exponential loop only consults launchBackoffMax
// INSIDE the doubling loop, and the loop body never runs for the first retry
// (fails == 1 → `for range 0`). So the very first retry returned
// launchBackoffBase UNCAPPED: an operator who lowers CTXLOOM_LAUNCH_BACKOFF_MAX
// below ..._BASE (a legitimate way to say "never wait more than X") gets a
// first retry that ignores the ceiling they set, and every subsequent attempt
// obeys it — the one inconsistency an operator tuning a runaway launch loop
// would least expect.
//
// The ceiling is a ceiling for every attempt, first included.
func TestLaunchBackoff_FirstRetryHonoursTheCeiling(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	c.launchBackoffBase = 5 * time.Second
	c.launchBackoffMax = 250 * time.Millisecond

	assert.Equal(t, c.launchBackoffMax, c.launchBackoff(1),
		"the FIRST retry must be capped at launchBackoffMax too — a base above the ceiling must not "+
			"slip through the doubling loop's cap check")

	// The rest of the curve is unchanged: still capped, still monotone.
	assert.Equal(t, c.launchBackoffMax, c.launchBackoff(2))
	assert.Equal(t, c.launchBackoffMax, c.launchBackoff(9))
	assert.Zero(t, c.launchBackoff(0), "no failures, no wait")
}

// TestLaunchBackoff_NormalCurveUnchanged pins the ordinary base<max case so the
// cap fix cannot quietly flatten the exponential curve it is meant to bound.
func TestLaunchBackoff_NormalCurveUnchanged(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	c.launchBackoffBase = 100 * time.Millisecond
	c.launchBackoffMax = 1 * time.Second

	assert.Equal(t, 100*time.Millisecond, c.launchBackoff(1))
	assert.Equal(t, 200*time.Millisecond, c.launchBackoff(2))
	assert.Equal(t, 400*time.Millisecond, c.launchBackoff(3))
	assert.Equal(t, 800*time.Millisecond, c.launchBackoff(4))
	assert.Equal(t, 1*time.Second, c.launchBackoff(5), "capped from here on")
	assert.Equal(t, 1*time.Second, c.launchBackoff(6))
}
