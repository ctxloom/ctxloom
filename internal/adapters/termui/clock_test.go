package termui

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeClock is a manual Clock: time moves only on Advance, which runs every
// timer that has come due, in deadline order, on the caller's goroutine.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Time
	seq  int
	f    func()
	done bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &fakeTimer{at: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		stopped := !t.done
		t.done = true
		return stopped
	}
}

// Pending reports how many timers are armed and not yet fired or stopped.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// Advance moves time forward by d, firing due timers one at a time (a timer
// a fired callback schedules inside the window fires too).
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		next := c.nextDueLocked(end)
		if next == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

func (c *fakeClock) nextDueLocked(end time.Time) *fakeTimer {
	live := c.timers[:0]
	for _, t := range c.timers {
		if !t.done {
			live = append(live, t)
		}
	}
	c.timers = live
	sort.Slice(live, func(i, j int) bool {
		if live[i].at.Equal(live[j].at) {
			return live[i].seq < live[j].seq
		}
		return live[i].at.Before(live[j].at)
	})
	if len(live) == 0 || live[0].at.After(end) {
		return nil
	}
	return live[0]
}

func TestPresentPolicy_DefaultsAndFloors(t *testing.T) {
	assert.Equal(t, PresentPolicy{QuietFor: 1500 * time.Millisecond, ArmFor: 750 * time.Millisecond},
		PresentPolicy{}.normalized(), "zero takes the ruled defaults")
	assert.Equal(t, PresentPolicy{QuietFor: 500 * time.Millisecond, ArmFor: 300 * time.Millisecond},
		PresentPolicy{QuietFor: time.Millisecond, ArmFor: time.Millisecond}.normalized(),
		"a duration under the floor is raised to it, never honoured")
	assert.Equal(t, PresentPolicy{QuietFor: 2 * time.Second, ArmFor: time.Second},
		PresentPolicy{QuietFor: 2 * time.Second, ArmFor: time.Second}.normalized())
}
