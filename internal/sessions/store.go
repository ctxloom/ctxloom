package sessions

import (
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// Store is the storage port for the harp-keyed session index (ADR 0026).
// *Manager is the filesystem adapter (a single index.yaml under a cooperative
// file lock); *MemStore is an in-memory adapter. The operations layer depends
// on this interface so the index's backing store can change without touching
// the session operations.
//
// Introducing the interface does NOT change the launch hot-path: *Manager's
// cooperative locking and fault-tolerant load/save are untouched — this only
// abstracts the dependency. The surface is exactly the methods operations
// invoke; index-only helpers (Path, SetSummary, …) stay on the concrete type.
type Store interface {
	Load() (*Index, error)
	Reconcile(isDead func(Entry) bool) ([]Entry, error)
	ListForProject(projectDir string) ([]Entry, error)
	ListAll() ([]Entry, error)
	Find(harpName string) (*Entry, error)
	AssignHarp(projectDir, backend string) (Entry, error)
	BindSession(harpName, sessionID, transcriptPath string) error
	RecordEngineVersion(harpName, version string) error
	MarkEnded(harpName string, at time.Time) error
	MarkPurged(harpName string, at time.Time) error
	Rename(oldName, newName string) error
	Forget(harpName string) error
	PendingUpgrade() *upgrade.Pending
	CommitUpgrade() error
}

// Compile-time proof that both adapters satisfy the port.
var (
	_ Store = (*Manager)(nil)
	_ Store = (*MemStore)(nil)
)
