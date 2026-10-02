package safefs

import "github.com/spf13/afero"

// SyncDir fsyncs dir's own entry list: an fsync on a FILE makes its contents
// durable but says nothing about the directory entry naming it. NewDurableFs
// is the usual way to get this; SyncDir is for a writer whose durability
// point is not a rename or a create through a decorated fs — an append-only
// log flushing the entry for a file it just created, say. The directory is
// opened through fs, so a test double's directory "syncs" as a no-op.
func SyncDir(fs afero.Fs, dir string) error {
	return syncDir(fs, dir)
}

// syncDirFn is the directory sync every durable path calls, indirected so a
// test can observe that it fired, with which directory, and inject a failure,
// without staging a crash.
var syncDirFn = syncDir

// SetSyncDirForTesting overrides the directory sync durable writes perform,
// process-wide, until the returned restore func runs. It exists for a
// CONSUMER's site-level test: a durable write produces bytes identical to a
// non-durable one, so a mutation dropping Durable() from a call site passes
// every content assertion unless the test can see the sync fire.
func SetSyncDirForTesting(fn func(dir string) error) func() {
	prev := syncDirFn
	syncDirFn = func(_ afero.Fs, dir string) error { return fn(dir) }
	return func() { syncDirFn = prev }
}
