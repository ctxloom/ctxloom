package sessions

import (
	"time"
)

// Store is the storage port for the harp-keyed session store (ADR 0026).
// *Manager is the filesystem adapter (session directories and their sidecars
// under the sessions root, each mutated under its own cooperative lock);
// *MemStore is an in-memory adapter. The operations layer depends on this
// interface so the backing store can change without touching the session
// operations. The surface is exactly the methods operations invoke;
// store-only helpers (SetSourceEntries, …) stay on the concrete type.
type Store interface {
	ListForProject(projectDir string) ([]Entry, error)
	ListAll() ([]Entry, error)
	Find(harpName string) (*Entry, error)
	FindBySessionID(sessionID string) (*Entry, error)
	AssignHarp(projectDir, backend string) (Entry, error)
	BindSession(harpName, sessionID, transcriptPath string) error
	AppendRotations(harpName string, rotations []Rotation) error
	RecordEngineVersion(harpName, version string) error
	MarkEnded(harpName string, at time.Time) error
	MarkPurged(harpName string, at time.Time) error
	Rename(oldName, newName string) error
	Forget(harpName string) error
}

// Compile-time proof that both adapters satisfy the port.
var (
	_ Store = (*Manager)(nil)
	_ Store = (*MemStore)(nil)
)
