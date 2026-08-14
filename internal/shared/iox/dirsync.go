package iox

import "github.com/spf13/afero"

// SyncDir fsyncs dir's own entry list to stable storage: an fsync on a FILE
// makes its CONTENTS durable but says nothing about the directory entry
// naming it. This is the raw primitive Durable() uses internally after a
// rename, exported for a caller whose write never goes through
// WriteFileAtomic/WriteFileAtomicFs/AtomicFile at all — an append-only log
// flushing only the directory entry for a file it just created (not an
// atomic replace, so Durable()'s Option shape does not fit), a hand-rolled
// rename outside this package — and needs the same durability primitive
// directly. Unix-only in effect: see dirsync_windows.go for the documented
// no-op on Windows, where there is no directory-fsync equivalent to call.
func SyncDir(dir string) error {
	return syncDir(dir)
}

// syncDirFn is the parent-directory fsync hook Durable() invokes after a
// successful rename. Indirected — rather than WriteFileAtomicFs/Commit
// calling syncDir directly — so a test can observe that the durability seam
// fired, with which directory, and can inject a failure, without staging an
// actual crash to prove it: the same idiom coord/artifactstore's
// syncArtifactDir var uses for the identical problem, generalized here so
// every Durable() caller shares one seam instead of each carrying its own.
var syncDirFn = syncDir

// isOSBackedFs reports whether fs is the real operating-system filesystem, as
// opposed to a test double (afero.MemMapFs, a ReadOnlyFs wrapping one, ...).
// A directory fsync needs a real file descriptor for the directory itself,
// which only an OS-backed filesystem can hand back — on any other backend
// Durable is a documented no-op (see its doc). Mirrors the identical idiom in
// admission.isOSBackedFs and agent.isOSBackedFs; not shared code with either
// because both are unexported in packages this one must not import.
func isOSBackedFs(fs afero.Fs) bool {
	_, ok := fs.(*afero.OsFs)
	return ok
}
