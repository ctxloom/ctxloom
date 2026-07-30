package profiles

// Source is the read port for profiles (ADR 0026): a place profiles load from.
// *Loader satisfies it; the in-memory *MemStore does too. A database- or
// service-backed adapter would as well.
//
// The port is deliberately the storage surface only — List/Load/Exists for
// reads, Save/Delete for writes (see Store). Parent-graph resolution
// (Loader.ResolveProfile) and schema-upgrade handling (Loader.PendingUpgrades/
// CommitUpgrade) stay on the concrete *Loader: they are loader logic layered
// over storage, not storage itself, so adapters never have to reimplement them.
//
// # Concurrency
//
// An adapter is NOT required to be safe for concurrent use, and *Loader is not:
// reading a profile accumulates the schema-upgrade ledger (pending,
// pendingPaths) with no synchronisation. This is sound because a loader is
// built per use — every factory in the tree (Config.GetProfileLoader,
// operations' profileLoader/profileLoaderFS, the CLI's create path) returns a
// fresh instance on every call, and none of them is memoised. Handing one
// loader to several goroutines is therefore outside the contract; *MemStore
// guards its map because a map is shared state by construction, not because
// the port promises safety.
type Source interface {
	List() ([]*Profile, error)
	Load(name string) (*Profile, error)
	Exists(name string) bool
}

// Store is the read+write port: a backing store profiles persist to. The
// filesystem adapter is *Loader; *MemStore is an in-memory adapter. The
// operations layer's profile-authoring requests depend on this interface, never
// on a concrete loader, so storage can change without touching core logic.
type Store interface {
	Source
	Save(profile *Profile) error
	Delete(name string) error
}

// Compile-time proof that both adapters satisfy the write port (and, by
// embedding, the read port).
var (
	_ Store = (*Loader)(nil)
	_ Store = (*MemStore)(nil)
)
