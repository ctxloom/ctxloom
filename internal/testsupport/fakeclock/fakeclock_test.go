package fakeclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClock_AdvanceFiresDueTimersInOrderAndNothingElse(t *testing.T) {
	c := New()
	var fired []string
	c.AfterFunc(2*time.Second, func() { fired = append(fired, "b") })
	c.AfterFunc(time.Second, func() {
		fired = append(fired, "a")
		c.AfterFunc(500*time.Millisecond, func() { fired = append(fired, "a+") })
	})
	c.AfterFunc(2*time.Second, func() { fired = append(fired, "b2") })
	stop := c.AfterFunc(time.Second, func() { fired = append(fired, "stopped") })
	assert.True(t, stop(), "stopped before it fired")
	c.AfterFunc(3*time.Second, func() { fired = append(fired, "late") })

	c.Advance(2 * time.Second)
	assert.Equal(t, []string{"a", "a+", "b", "b2"}, fired)
	assert.Equal(t, Epoch.Add(2*time.Second), c.Now())
	assert.Equal(t, 1, c.Pending())
}
