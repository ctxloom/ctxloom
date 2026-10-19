package termoracle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The default focus-lock timings (the zero PresentPolicy): how long the
// human's input must have been quiet before an approval may take focus, and
// how long the approval stays inert once it has it.
const (
	QuietFor = 1500 * time.Millisecond
	ArmFor   = 750 * time.Millisecond
)

// Notice is what every contract case's summon asks an engaged surface to
// show: the asking harp, the one thing only the caller knows.
const Notice = "approval from wiry-otter"

// Outcome is what one Summon came to.
type Outcome int

const (
	// Shown: the approval holds focus.
	Shown Outcome = iota
	// Engaged: the human already had the surface in use; it was told the
	// notice and focus did not move.
	Engaged
	// Cancelled: the summon's context ended first.
	Cancelled
	// Unavailable: the surface is closed or broken; nothing was shown.
	Unavailable
)

// Summoner is one implementation of the root terminal surface, on a manual
// clock, as the summon contract drives it. Every method that delivers input
// returns once the surface has acted on it, so a case never waits on a
// guess.
type Summoner interface {
	// Type is one read of the human's keys, delivered wherever focus is.
	Type(t *testing.T, keys string)
	// Engine is every byte the engine has received from the human.
	Engine() string
	// Summon asks, on its own goroutine, for the approval view, with Notice;
	// the result arrives on the channel.
	Summon(ctx context.Context) <-chan error
	// Parked returns once the summon in flight has made its attempt and is
	// waiting on the clock.
	Parked(t *testing.T)
	// Advance moves the manual clock, firing what came due.
	Advance(t *testing.T, d time.Duration)
	// Outcome maps one Summon result onto the contract's vocabulary.
	Outcome(err error) Outcome
	// Shown reports whether the approval view holds focus.
	Shown() bool
	// Armed reports whether the approval view has armed and how many keys
	// its inert window discarded.
	Armed() (armed bool, discarded int)
	// Approval is every key the approval view received once armed.
	Approval() string
	// Engage has the human take the surface themself (the prefix key).
	Engage(t *testing.T)
	// Notices are the notices the surface was told while engaged.
	Notices() []string
	// Close ends the surface.
	Close()
}

// recv waits for a Summon result, failing only at the test binary's
// deadline: every case asks once the result is decided.
func recv(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-testsupport.Expiry(t):
		t.Fatal("Summon never returned")
		return nil
	}
}

func pending(t *testing.T, ch <-chan error, why string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s: Summon returned %v", why, err)
	default:
	}
}

// RunSummonContract runs the summon contract against fresh surfaces from
// newSurface. It is the approvals focus lock: an approval takes focus only
// at a pause in the human's typing, discards what is typed during its inert
// window (restarting the window on each key), never takes focus from a
// surface the human is using, and refuses once the surface is gone.
func RunSummonContract(t *testing.T, newSurface func(t *testing.T) Summoner) {
	t.Run("a typing human keeps focus; it moves QuietFor after the last key", func(t *testing.T) {
		s := newSurface(t)
		s.Type(t, "a")
		done := s.Summon(context.Background())
		s.Parked(t)
		for _, k := range []string{"b", "c", "d"} {
			s.Advance(t, time.Second)
			s.Type(t, k)
			s.Parked(t)
			pending(t, done, "focus moved while the human was typing")
			assert.False(t, s.Shown(), "the approval took focus while the human was typing")
		}
		s.Advance(t, QuietFor-100*time.Millisecond)
		s.Parked(t)
		pending(t, done, "focus moved before QuietFor of quiet")
		s.Advance(t, 100*time.Millisecond)
		require.Equal(t, Shown, s.Outcome(recv(t, done)))
		assert.True(t, s.Shown())
		assert.Equal(t, "abcd", s.Engine(), "every key typed before the approval reached the engine")
	})

	t.Run("keys in the inert window are discarded, restart it, and decide nothing", func(t *testing.T) {
		s := newSurface(t)
		s.Advance(t, time.Hour)
		require.Equal(t, Shown, s.Outcome(recv(t, s.Summon(context.Background()))))
		s.Type(t, "y1\r")
		armed, n := s.Armed()
		require.False(t, armed)
		require.Equal(t, 3, n, "three keys discarded")
		s.Advance(t, 500*time.Millisecond)
		s.Type(t, "n")
		s.Advance(t, ArmFor-500*time.Millisecond)
		armed, _ = s.Armed()
		require.False(t, armed, "armed while a key had restarted the window")
		s.Advance(t, 500*time.Millisecond)
		armed, n = s.Armed()
		require.True(t, armed, "ArmFor after the last key, the approval arms")
		assert.Equal(t, 4, n, "every key the window swallowed is counted")
		assert.Empty(t, s.Approval(), "no key typed while inert reached the approval")
		assert.Empty(t, s.Engine(), "nor the engine")
		s.Type(t, "j")
		assert.Equal(t, "j", s.Approval(), "armed, keys reach the approval")
	})

	t.Run("a surface in use keeps focus and is told who asked", func(t *testing.T) {
		s := newSurface(t)
		s.Engage(t)
		s.Advance(t, time.Hour)
		require.Equal(t, Engaged, s.Outcome(recv(t, s.Summon(context.Background()))))
		assert.Equal(t, []string{Notice}, s.Notices())
		assert.False(t, s.Shown())
	})

	t.Run("a cancelled summon ends with its context", func(t *testing.T) {
		s := newSurface(t)
		s.Type(t, "a")
		ctx, cancel := context.WithCancel(context.Background())
		done := s.Summon(ctx)
		s.Parked(t)
		cancel()
		require.Equal(t, Cancelled, s.Outcome(recv(t, done)))
		s.Advance(t, time.Hour)
		assert.False(t, s.Shown(), "a withdrawn summon never takes focus later")
	})

	t.Run("a closed surface refuses", func(t *testing.T) {
		s := newSurface(t)
		s.Close()
		require.Equal(t, Unavailable, s.Outcome(recv(t, s.Summon(context.Background()))))
	})
}
