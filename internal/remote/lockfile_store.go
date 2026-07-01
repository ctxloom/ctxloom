package remote

// LockfileStore is the storage port for a lockfile (ADR 0026): the active
// (lock.yaml) or pending (lock.pending.yaml) record a manager is scoped to.
// *LockfileManager is the filesystem adapter; its existing afero.Fs seam
// (WithLockfileFS) already supplies an in-memory backend for tests, so no
// separate MemStore is needed. The operations layer depends on this interface
// for its injectable lock managers, never on the concrete type.
type LockfileStore interface {
	Load() (*Lockfile, error)
	Save(lockfile *Lockfile) error
	Delete() error
	Path() string
	IsPending() bool
}

// Compile-time proof the filesystem manager satisfies the port.
var _ LockfileStore = (*LockfileManager)(nil)
