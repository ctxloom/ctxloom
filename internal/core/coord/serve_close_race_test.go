package coord

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// newUnservedCoordinator builds a coordinator WITHOUT standing its listeners
// up — newTestCoordinator calls Serve for you, which is exactly the call these
// tests need to make themselves.
func newUnservedCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    newFakeSpawner(nil, nil),
		OwnerHarp:  ownerIdentity().Harp,
	})
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	t.Cleanup(c.Close)
	return c
}

// TestCoordinator_ServeIsRaceFreeAgainstURLReaders pins that the listener set
// is published safely. Serve binds the transport; ReachURL reads it from the spawn
// path (children.go builds a child's env from it) and Close reads it during
// teardown — all on other goroutines. Unsynchronised, that is a data race on a
// pointer field, and the reader could observe a half-initialised coordServing.
//
// Run under -race (test-pkg always does), the reader loop below reports it.
func TestCoordinator_ServeIsRaceFreeAgainstURLReaders(t *testing.T) {
	c := newUnservedCoordinator(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.LoopbackURL()
				_, _ = c.ReachURL("host")
			}
		}
	}()

	serveErr := c.Serve()
	close(stop)
	wg.Wait()

	assert.NoError(t, serveErr)
	assert.NotEmpty(t, c.LoopbackURL(), "readers must see the listeners once Serve returns")
}

// TestCoordinator_CloseIsRaceFreeAgainstServe pins the same field across the
// teardown pairing that matters most: a Close concurrent with Serve.
func TestCoordinator_CloseIsRaceFreeAgainstServe(t *testing.T) {
	c := newUnservedCoordinator(t)

	var wg sync.WaitGroup
	wg.Add(1)
	var serveErr error
	go func() {
		defer wg.Done()
		serveErr = c.Serve()
	}()
	c.Close()
	wg.Wait()

	// Whichever order won, no listener survives: a Serve that published
	// behind the Close unwinds its own and says so (ErrClosed), and a Serve
	// that published ahead of it was taken down by the Close.
	if serveErr != nil {
		assert.ErrorIs(t, serveErr, ErrClosed)
	}
	assert.False(t, c.Serving(), "no listener set may survive a Close, whichever order the two ran in")
	c.Close() // idempotent
}
