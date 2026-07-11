package sessions

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// MemStore is an in-memory sessions Store adapter. It mirrors *Manager's
// observable behavior (harp minting, first-bind-wins, reconcile-by-predicate)
// without touching disk, so the operations layer's storage-agnosticism is
// demonstrable (ADR 0026) and tests get a filesystem-free index. Filesystem
// side effects of the real index — the per-harp transcript symlink, schema
// upgrades — are intentionally absent; PendingUpgrade always reports "current".
type MemStore struct {
	mu       sync.Mutex
	sessions []Entry
}

// NewMemStore returns an empty in-memory session index.
func NewMemStore() *MemStore {
	return &MemStore{}
}

// Load returns a snapshot copy of the index.
func (m *MemStore) Load() (*Index, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &Index{Sessions: append([]Entry(nil), m.sessions...)}, nil
}

// Reconcile drops entries that isDead reports unrecoverable and returns the
// survivors, matching *Manager.Reconcile.
func (m *MemStore) Reconcile(isDead func(Entry) bool) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	survivors := make([]Entry, 0, len(m.sessions))
	for _, e := range m.sessions {
		if !isDead(e) {
			survivors = append(survivors, e)
		}
	}
	m.sessions = survivors
	return append([]Entry(nil), survivors...), nil
}

// ListForProject returns entries for projectDir, most-recent-first by
// last-worked time (see ActivityTime), matching *Manager.ListForProject.
func (m *MemStore) ListForProject(projectDir string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.sessions {
		if e.ProjectDir == projectDir {
			e.LastActivity = ActivityTime(e)
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivity.Equal(out[j].LastActivity) {
			return out[i].LastActivity.After(out[j].LastActivity)
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// Find returns a copy of the entry for harpName, or nil if absent.
func (m *MemStore) Find(harpName string) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName == harpName {
			out := m.sessions[i]
			return &out, nil
		}
	}
	return nil, nil
}

// AssignHarp mints a fresh unique harp for a new run and appends a pending
// entry (SessionID unbound), matching *Manager.AssignHarp.
func (m *MemStore) AssignHarp(projectDir, backend string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	used := make(map[string]struct{}, len(m.sessions))
	for _, s := range m.sessions {
		used[s.HarpName] = struct{}{}
	}
	entry := Entry{
		HarpName:   generateUniqueHarp(used),
		Backend:    backend,
		ProjectDir: projectDir,
		StartedAt:  time.Now().UTC(),
	}
	m.sessions = append(m.sessions, entry)
	return entry, nil
}

// BindSession records the backend session id / transcript for harpName,
// first-bind-wins (a bound entry is a no-op), matching *Manager.BindSession.
func (m *MemStore) BindSession(harpName, sessionID, transcriptPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName != harpName {
			continue
		}
		if m.sessions[i].SessionID != "" {
			return nil
		}
		m.sessions[i].SessionID = sessionID
		if transcriptPath != "" {
			m.sessions[i].TranscriptPath = transcriptPath
		}
		return nil
	}
	return fmt.Errorf("harp not found in index: %q", harpName)
}

// MarkEnded stamps EndedAt on the named entry. Idempotent.
func (m *MemStore) MarkEnded(harpName string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName != harpName {
			continue
		}
		t := at.UTC()
		m.sessions[i].EndedAt = &t
		return nil
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// Rename changes a harp name, erroring if oldName is absent or newName is taken.
func (m *MemStore) Rename(oldName, newName string) error {
	if newName == "" {
		return fmt.Errorf("newName required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	targetIdx := -1
	for i := range m.sessions {
		if m.sessions[i].HarpName == newName {
			return fmt.Errorf("name already in use: %q", newName)
		}
		if m.sessions[i].HarpName == oldName {
			targetIdx = i
		}
	}
	if targetIdx < 0 {
		return fmt.Errorf("harp not found: %q", oldName)
	}
	m.sessions[targetIdx].HarpName = newName
	return nil
}

// Forget removes the harp entry from the index.
func (m *MemStore) Forget(harpName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName != harpName {
			continue
		}
		m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
		return nil
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// PendingUpgrade always reports "current" — an in-memory index has no on-disk
// schema to migrate.
func (m *MemStore) PendingUpgrade() *upgrade.Pending { return nil }

// CommitUpgrade is a no-op for the in-memory store.
func (m *MemStore) CommitUpgrade() error { return nil }
