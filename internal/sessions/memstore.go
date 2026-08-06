package sessions

import (
	"fmt"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// MemStore is an in-memory sessions Store adapter. It mirrors *Manager's
// observable behavior (harp minting, first-bind-wins, reconcile-by-predicate)
// so the operations layer's storage-agnosticism is demonstrable (ADR 0026) and
// tests get a filesystem-free INDEX.
//
// Filesystem-free names the index and the index only: no index.yaml, no file
// lock, no atomic rewrite, and none of the real store's write side effects —
// BindSession drops no per-harp transcript symlink and PendingUpgrade always
// reports "current".
//
// It does NOT mean disk-free, and callers must not assume it. Find,
// ListForProject and ListAll run the same computed-on-read enrichment as
// *Manager — fillCanonicalTranscript and ActivityTime — which stat the harp's
// real HOME-rooted transcript files. That sharing is deliberate: a fake that
// skipped it would report a different CanonicalTranscriptPath and a different
// activity ordering than the store it stands in for, which is exactly how a
// defect stays invisible (see Rename). A test that needs those stats contained
// must isolate HOME.
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
			out = append(out, e)
		}
	}
	return enrichAndSortByActivity(out), nil
}

// ListAll returns every entry, most-recent-first by last-worked time (see
// ActivityTime), matching *Manager.ListAll — the all-projects listing with no
// project filter.
func (m *MemStore) ListAll() ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return enrichAndSortByActivity(append([]Entry(nil), m.sessions...)), nil
}

// Find returns a copy of the entry for harpName, or nil if absent. Enriches
// the copy with CanonicalTranscriptPath, matching ListForProject and
// *Manager.Find (S4).
func (m *MemStore) Find(harpName string) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName == harpName {
			out := m.sessions[i]
			fillCanonicalTranscript(&out)
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
	name, err := generateUniqueHarp(used)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{
		HarpName:   name,
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

// MarkPurged stamps PurgedAt on the named entry. Idempotent. Mirrors
// Manager.MarkPurged's ordering contract (mark before the caller destroys any
// file) — this fake exists so operations-level tests can exercise that
// ordering without a real index.yaml.
func (m *MemStore) MarkPurged(harpName string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions {
		if m.sessions[i].HarpName != harpName {
			continue
		}
		t := at.UTC()
		m.sessions[i].PurgedAt = &t
		return nil
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// Rename changes a harp name, erroring if oldName is absent, newName is taken,
// or newName is not a usable harp identifier. The last check mirrors
// Manager.Rename exactly: this is the fake every other package's
// tests run against, and a fake that accepts a name the real store refuses is
// how a validation defect stays invisible.
func (m *MemStore) Rename(oldName, newName string) error {
	if err := harp.Validate(newName); err != nil {
		return err
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
