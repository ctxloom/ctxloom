package discover

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ownerLockOf is the owner lock of the root endpoint.json sits in.
func ownerLockOf(endpoint string) string { return filepath.Join(filepath.Dir(endpoint), "owner.lock") }

// heldLocks is each live fixture writer's owner lock, by endpoint path. The
// package's tests are not parallel.
var heldLocks = map[string]safefs.Lock{}

// holdOwnerLock is a LIVE writer: a coordinator holds its root's owner lock
// for as long as it runs, and the kernel drops it the moment it dies.
func holdOwnerLock(t *testing.T, endpoint string) {
	t.Helper()
	l, err := safefs.New().Locks.Lock(ownerLockOf(endpoint))
	require.NoError(t, err)
	heldLocks[endpoint] = l
	t.Cleanup(func() { releaseOwnerLock(t, endpoint) })
}

// releaseOwnerLock is the writer dying without cleanup: the kernel drops its
// lock and every file it wrote stays.
func releaseOwnerLock(t *testing.T, endpoint string) {
	t.Helper()
	if fl, ok := heldLocks[endpoint]; ok {
		require.NoError(t, fl.Unlock())
		delete(heldLocks, endpoint)
	}
}

// A coordinator that died without cleaning up leaves its endpoint.json —
// deliberately, so a relaunch re-binds the same ports — and its owner.lock
// file, but nobody holds the lock. Its port and consumer credential are dead
// (or, worse, the port is someone else's now), so List must not hand them
// back. Only a root whose owner lock is HELD is a live endpoint.
func TestList_EndpointWhoseWriterIsGone_IsNotListed(t *testing.T) {
	home := testsupport.Isolate(t)
	now := time.Now()

	live := endpointPath(home, "proj", "live")
	writeEndpointAt(t, live, `{"loopback_port":1001,"consumer_cred":"tok-live"}`, now)

	dead := endpointPath(home, "proj", "dead")
	writeEndpointAt(t, dead, `{"loopback_port":1002,"consumer_cred":"tok-dead"}`, now.Add(time.Second))
	releaseOwnerLock(t, dead)

	lockless := endpointPath(home, "proj", "lockless")
	writeEndpointAt(t, lockless, `{"loopback_port":1003,"consumer_cred":"tok-lockless"}`, now.Add(2*time.Second))
	releaseOwnerLock(t, lockless)
	require.NoError(t, os.Remove(ownerLockOf(lockless)))

	eps, skipped := List()
	assert.Empty(t, skipped, "a dead coordinator's leftover endpoint is the ordinary aftermath of every exit, not a fault")
	require.Len(t, eps, 1)
	assert.Equal(t, "tok-live", eps[0].Cred)
}
