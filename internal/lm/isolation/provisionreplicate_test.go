package isolation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// replicaFixture stands up a host credential and an instance home, and starts
// the replicator over them. Everything is a temp directory: no test here may
// ever reach a real engine home, which is live production data whose loss
// costs the user their login.
func replicaFixture(t *testing.T, material Material) (hostFile, instFile string, res Result) {
	t.Helper()
	// Isolate HOME so the cross-process lock's sidecar lands in a temp
	// ~/.ctxloom/locks rather than anywhere near the real one.
	testsupport.Isolate(t)

	hostDir, home := t.TempDir(), t.TempDir()
	hostFile = filepath.Join(hostDir, "creds.json")
	require.NoError(t, os.WriteFile(hostFile, []byte("token-1"), 0o600))
	material.Host = hostFile

	p := &replicationProvisioner{}
	res, err := p.Provision(home, []Material{material})
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Close() })
	return hostFile, filepath.Join(home, filepath.FromSlash(material.DestRel)), res
}

func eventuallyReads(t *testing.T, path, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		got, err := os.ReadFile(path)
		return err == nil && string(got) == want
	}, 5*time.Second, 25*time.Millisecond, "%s never reached %q", path, want)
}

// Bootstrap: the instance gets the host material immediately, so the engine
// finds a usable credential rather than an empty home.
func TestReplicationProvision_BootstrapsTheInstanceFromTheHost(t *testing.T) {
	_, instFile, res := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	assert.Equal(t, DeliveryReplicated, res.Delivery,
		"replication must report shared-by-REPLICATION; a consumer debugging a rejected refresh needs to see it got the delivery with a rotation window")
	assert.Equal(t, replicationMechanism, res.Mechanism)

	got, err := os.ReadFile(instFile)
	require.NoError(t, err)
	assert.Equal(t, "token-1", string(got))

	info, err := os.Stat(instFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a file holding live credential bytes must be owner-only whatever the source's mode was")
}

// THE WHOLE POINT. A refresh written INSIDE the instance must reach the host,
// or the instance is a copy with a fuse on it — it works until the access
// token expires and then that instance is stuck.
func TestReplicationProvision_AnInstanceRefreshReachesTheHost(t *testing.T) {
	hostFile, instFile, _ := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	require.NoError(t, os.WriteFile(instFile, []byte("token-2-refreshed-by-the-engine"), 0o600))
	eventuallyReads(t, hostFile, "token-2-refreshed-by-the-engine")
}

// And the other direction: a refresh performed by some OTHER instance, landing
// on the host, must reach this one — otherwise this instance goes on
// presenting a token that has already been consumed.
func TestReplicationProvision_AHostRefreshReachesTheInstance(t *testing.T) {
	hostFile, instFile, _ := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	require.NoError(t, os.WriteFile(hostFile, []byte("token-3-from-another-instance"), 0o600))
	eventuallyReads(t, instFile, "token-3-from-another-instance")
}

// Loop suppression. Each propagation writes the OTHER side, which fires that
// side's watcher, which would propagate back — forever — if the replicator
// could not recognise its own writes. Hashing both sides after every write is
// what stops it, and the observable consequence is that the content SETTLES.
func TestReplicationProvision_DoesNotBounceItsOwnWritesBackAndForth(t *testing.T) {
	hostFile, instFile, _ := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	require.NoError(t, os.WriteFile(instFile, []byte("settled"), 0o600))
	eventuallyReads(t, hostFile, "settled")

	// Long enough for several debounce windows: a bouncing replicator would
	// still be writing, and each write bumps the modification time.
	before, err := os.Stat(hostFile)
	require.NoError(t, err)
	time.Sleep(time.Second)
	after, err := os.Stat(hostFile)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(),
		"the host file kept being rewritten after the content had settled, which is the replication loop")
}

// Writes must not swap the inode. A rename would, and that is fatal twice
// over: the host file may itself be a bind-mount target for another run (where
// rename returns EBUSY), and a new inode every sync turns the steady state
// into a stream of replace events.
func TestReplicationProvision_WritesInPlaceRatherThanRenaming(t *testing.T) {
	hostFile, instFile, _ := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	before, err := os.Stat(hostFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(instFile, []byte("token-in-place"), 0o600))
	eventuallyReads(t, hostFile, "token-in-place")

	after, err := os.Stat(hostFile)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after),
		"the host file was replaced rather than written through; a rename orphans every lock and watch bound to it")
}

// Read-only material is consumed, never written back: an instance's edit is
// reverted from the host rather than propagated to it.
func TestReplicationProvision_ReadOnlyMaterialNeverWritesBack(t *testing.T) {
	hostFile, instFile, _ := replicaFixture(t,
		Material{DestRel: ".claude/settings.json", Sharing: SharingShared, ReadOnly: true})

	require.NoError(t, os.WriteFile(instFile, []byte("the agent edited this"), 0o600))
	eventuallyReads(t, instFile, "token-1")

	onHost, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, "token-1", string(onHost), "read-only material reached the host")
}

// Unlike both mount implementations, replication leaves goroutines and
// watchers running — so its Result must be able to stop them, and stopping
// twice must be safe.
func TestReplicationProvision_CloseStopsTheReplicatorAndIsIdempotent(t *testing.T) {
	hostFile, instFile, res := replicaFixture(t, Material{DestRel: ".claude/creds.json", Sharing: SharingShared})

	require.NoError(t, res.Close())
	require.NoError(t, res.Close(), "Close must be idempotent; a caller unwinding two paths will call it twice")

	require.NoError(t, os.WriteFile(instFile, []byte("after-close"), 0o600))
	time.Sleep(500 * time.Millisecond)
	onHost, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, "token-1", string(onHost), "the replicator kept propagating after it was closed")
}

// Replication delivers shared material and nothing else: a replicated file's
// writes reach the host by definition, so claiming to isolate would be a lie.
func TestReplicationProvisioner_CanOnlyDeliverShared(t *testing.T) {
	p := &replicationProvisioner{}
	assert.True(t, p.Can(SharingShared))
	assert.False(t, p.Can(SharingPrivate))
	assert.False(t, p.Can(SharingUnset))
	assert.Equal(t, DeliveryReplicated, p.Delivery())
}
