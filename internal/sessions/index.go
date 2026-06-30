// Package sessions tracks per-session metadata under user-global
// ~/.ctxloom/sessions/index.yaml. Each entry binds a harp name (assigned
// by `ctxloom run` pre-launch) to the backend's session ID (bound on the
// first MCP initialize call from the spawned LLM) and to the project
// directory the session ran in.
//
// The index is the source of truth for the pre-launch resume picker and
// the load_session-by-harp-name path. Backend-native session transcripts
// are still produced by the backend (Claude Code, Codex, etc.); this
// package only adds a cross-cutting harp-keyed layer on top.
package sessions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// Entry is one row in index.yaml. The json tags mirror the yaml keys so
// `ctxloom session list --format json` and any frontend reading it (the VSCode
// companion) share the same snake_case contract as the on-disk index.
type Entry struct {
	HarpName       string     `yaml:"harp_name" json:"harp_name"`
	SessionID      string     `yaml:"session_id,omitempty" json:"session_id,omitempty"` // empty until backend binds on initialize
	Backend        string     `yaml:"backend,omitempty" json:"backend,omitempty"`
	ProjectDir     string     `yaml:"project_dir" json:"project_dir"`
	StartedAt      time.Time  `yaml:"started_at" json:"started_at"`
	EndedAt        *time.Time `yaml:"ended_at,omitempty" json:"ended_at,omitempty"`
	TranscriptPath string     `yaml:"transcript_path,omitempty" json:"transcript_path,omitempty"`
	Summary        string     `yaml:"summary,omitempty" json:"summary,omitempty"` // mirror of essence.md frontmatter, for fast picker render
	// Detail holds extra picker lines (the distilled Open Items) shown under the
	// summary. Kept separate from Summary so the single-line consumers (session
	// list table, MCP resource) stay one line while the picker can render more.
	Detail []string `yaml:"detail,omitempty" json:"detail,omitempty"`
	// SourceSize is the byte size of the backend transcript at the moment this
	// session was last distilled — the staleness fingerprint. `session list` and
	// the resume picker stat the live transcript (TranscriptPath) and flag the
	// row "out of date" once the size has moved past this. Append-only
	// transcripts only grow, so any change means the essence covers an earlier
	// slice. Zero when never distilled (or distilled before staleness tracking);
	// omitempty keeps those rows clean.
	SourceSize int64 `yaml:"source_size,omitempty" json:"source_size,omitempty"`

	// Distilled and EssencePath are COMPUTED at list/show time from the essence
	// file's presence on disk — never persisted (yaml:"-"), so the index stays the
	// source of truth for stored fields only. They let a client tell whether a
	// session has an essence and open it, without reconstructing the backend's
	// ~/.ctxloom/sessions/<harp>/essence.md layout. EssencePath is "" (and omitted)
	// when the session isn't distilled.
	Distilled   bool   `yaml:"-" json:"distilled"`
	EssencePath string `yaml:"-" json:"essence_path,omitempty"`
}

// Index is the on-disk form of the session index.
type Index struct {
	Sessions []Entry `yaml:"sessions"`
}

// Manager owns load/save of a single index file with a cooperative lock.
// Constructed via Open; all mutations go through its methods so the file
// lock and in-memory state stay consistent.
type Manager struct {
	path string
	mu   sync.Mutex

	// pendingUpgrade records an in-memory schema upgrade applied by the most
	// recent load (e.g. legacy timestamps normalized). Nil when the on-disk
	// index was already current. Never persisted automatically — an interactive
	// caller prompts and calls CommitUpgrade.
	pendingUpgrade *upgrade.Pending
}

// Open returns a Manager for the user-global index at
// ~/.ctxloom/sessions/index.yaml unless override is non-empty.
func Open(override string) (*Manager, error) {
	path := override
	if path == "" {
		p, err := paths.SessionIndexPath()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir sessions dir: %w", err)
	}
	return &Manager{path: path}, nil
}

// Path returns the absolute path to the index file.
func (m *Manager) Path() string { return m.path }

// Load reads the index from disk. Returns an empty Index if the file
// doesn't exist (first-run case).
func (m *Manager) Load() (*Index, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() (*Index, error) {
	m.pendingUpgrade = nil
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if len(data) == 0 {
		return &idx, nil
	}
	// Upgrade older on-disk encodings (e.g. legacy timestamp formats) to the
	// current one *in memory* before parsing. We don't rewrite the file here —
	// an interactive caller may prompt and persist via CommitUpgrade — so a
	// read-only command never silently rewrites the index. Normal mutations
	// (saveLocked) already write canonical form, so writes self-heal anyway.
	data, applied := indexUpgrades.Run(data)
	if len(applied) > 0 {
		m.pendingUpgrade = &upgrade.Pending{Path: m.path, Data: data, Applied: applied}
	}
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	return &idx, nil
}

// PendingUpgrade returns the in-memory schema upgrade staged by the most recent
// load, or nil if the on-disk index was already current. Safe for concurrent use.
func (m *Manager) PendingUpgrade() *upgrade.Pending {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingUpgrade
}

// CommitUpgrade persists a pending index upgrade to disk (atomic tmp+rename).
// No-op when nothing is pending; clears the pending state on success. Callers
// prompt the user before invoking this.
//
// The upgrade is RE-STAGED from the file's current bytes under the lock, not
// written from the staged snapshot: between Load (which staged it) and the
// user's consent, a concurrent writer — e.g. the spawned backend's MCP
// BindSession — may have rewritten the index, and persisting the stale
// snapshot would silently drop that write. Concurrent writers emit canonical
// form, so re-running the pipeline on fresh bytes then finds nothing to do.
func (m *Manager) CommitUpgrade() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingUpgrade == nil {
		return nil
	}

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(data) == 0) {
		m.pendingUpgrade = nil // the file is gone or empty; nothing to upgrade
		return nil
	}
	if err != nil {
		return err
	}
	upgraded, applied := indexUpgrades.Run(data)
	if len(applied) == 0 {
		m.pendingUpgrade = nil // a concurrent write already canonicalized it
		return nil
	}

	if err := iox.WriteFileAtomic(m.path, upgraded, 0o644); err != nil {
		return err
	}
	m.pendingUpgrade = nil
	return nil
}

// AssignHarp generates a fresh harp name (collision-checked against the
// existing index), appends a pending Entry with the given project dir
// and backend, and persists. Returns the assigned Entry.
//
// Used by `ctxloom run` pre-launch: the entry is "pending" because
// SessionID is not known until the spawned LLM's MCP server calls
// initialize. Use BindSession to fill that in.
func (m *Manager) AssignHarp(projectDir, backend string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return Entry{}, fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return Entry{}, err
	}

	used := make(map[string]struct{}, len(idx.Sessions))
	for _, s := range idx.Sessions {
		used[s.HarpName] = struct{}{}
	}

	name := generateUniqueHarp(used)
	entry := Entry{
		HarpName:   name,
		Backend:    backend,
		ProjectDir: projectDir,
		StartedAt:  time.Now().UTC(),
	}
	idx.Sessions = append(idx.Sessions, entry)
	if err := m.saveLocked(idx); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// BindSession fills in the backend-native session ID and transcript path
// for an existing harp-named entry. Called from the MCP initialize
// handler once the backend has bootstrapped enough to know its session
// UUID. First bind wins: once SessionID is set, subsequent calls with a
// different ID are silently dropped so a stale binder cannot clobber a
// fresh one through a TOCTOU race between Find and BindSession.
func (m *Manager) BindSession(harpName, sessionID, transcriptPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName != harpName {
			continue
		}
		// First bind wins. Same ID is a no-op (idempotent re-runs);
		// a different ID over an already-bound entry is also a no-op
		// — defense-in-depth for the SessionStart-vs-compact-vs-scan
		// race the caller-side `entry.SessionID != ""` checks already
		// guard against.
		if idx.Sessions[i].SessionID != "" {
			return nil
		}
		idx.Sessions[i].SessionID = sessionID
		if transcriptPath != "" {
			idx.Sessions[i].TranscriptPath = transcriptPath
			// Drop a convenience symlink in the harp dir pointing at the
			// live transcript so the session's tasks.md, essence.md, and
			// transcript.jsonl all live in one place. Best-effort: a
			// failure must not block the bind.
			linkTranscriptIntoHarpDir(harpName, transcriptPath)
		}
		return m.saveLocked(idx)
	}
	return fmt.Errorf("harp not found in index: %q", harpName)
}

// linkTranscriptIntoHarpDir creates ~/.ctxloom/sessions/<harp>/transcript.jsonl
// as a symlink to the backend's live transcript. Best-effort: any failure
// (home unresolved, no symlink privilege on Windows, etc.) is warned and
// swallowed so it never blocks a session bind. Idempotent — an existing link
// is replaced.
func linkTranscriptIntoHarpDir(harpName, transcriptPath string) {
	dir, err := paths.HarpDir(harpName)
	if err != nil {
		clidiag.Warn("ctxloom", "transcript link: %v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "transcript link: %v", err)
		return
	}
	link := filepath.Join(dir, "transcript.jsonl")
	_ = os.Remove(link) // replace any stale link; ignore absence
	if err := os.Symlink(transcriptPath, link); err != nil {
		clidiag.Warn("ctxloom", "transcript link: %v", err)
	}
}

// Find returns a copy of the entry with the given harp name, or nil if
// absent.
func (m *Manager) Find(harpName string) (*Entry, error) {
	idx, err := m.Load()
	if err != nil {
		return nil, err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName == harpName {
			out := idx.Sessions[i]
			return &out, nil
		}
	}
	return nil, nil
}

// ListForProject returns entries whose ProjectDir == projectDir, sorted
// most-recent-first. Used by the picker.
func (m *Manager) ListForProject(projectDir string) ([]Entry, error) {
	idx, err := m.Load()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range idx.Sessions {
		if e.ProjectDir == projectDir {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// TranscriptStale compares a transcript file's current byte size to the size
// stamped when an essence was distilled from it (Entry.SourceSize). It reports
// whether the essence is out of date and whether that could be determined at
// all: known=false (stale=false) when there is no stamped size (never distilled,
// or distilled before staleness tracking), no transcript path, or the stat
// fails. Append-only transcripts only grow, so any size difference means the
// essence covers an earlier slice. Read-only and best-effort per the
// fault-tolerance philosophy — an unreadable transcript degrades to "can't
// tell", never an error.
func TranscriptStale(transcriptPath string, stampedSize int64) (stale, known bool) {
	if stampedSize == 0 || transcriptPath == "" {
		return false, false
	}
	info, err := os.Stat(transcriptPath)
	if err != nil {
		return false, false
	}
	return info.Size() != stampedSize, true
}

// SourceStale reports whether this entry's distilled essence is out of date
// relative to its source transcript, and whether that could be determined (see
// TranscriptStale). The picker and `session list` use it to badge stale rows.
func (e Entry) SourceStale() (stale, known bool) {
	return TranscriptStale(e.TranscriptPath, e.SourceSize)
}

// MarkEnded sets EndedAt on the named entry. Idempotent.
func (m *Manager) MarkEnded(harpName string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName != harpName {
			continue
		}
		t := at.UTC()
		idx.Sessions[i].EndedAt = &t
		return m.saveLocked(idx)
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// Rename changes the harp name of an existing entry to newName, leaving
// SessionID and other fields intact. Errors if oldName doesn't exist or
// newName is already in use.
func (m *Manager) Rename(oldName, newName string) error {
	if newName == "" {
		return fmt.Errorf("newName required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return err
	}
	targetIdx := -1
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName == newName {
			return fmt.Errorf("name already in use: %q", newName)
		}
		if idx.Sessions[i].HarpName == oldName {
			targetIdx = i
		}
	}
	if targetIdx < 0 {
		return fmt.Errorf("harp not found: %q", oldName)
	}
	idx.Sessions[targetIdx].HarpName = newName
	return m.saveLocked(idx)
}

// Forget removes the harp entry from the index. The backend transcript
// and any cached distilled essence are left untouched on disk — only the
// harp-keyed pointer goes away.
func (m *Manager) Forget(harpName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName != harpName {
			continue
		}
		idx.Sessions = append(idx.Sessions[:i], idx.Sessions[i+1:]...)
		return m.saveLocked(idx)
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// Reconcile drops the entries that isDead reports as unrecoverable, persisting
// the pruned index, and returns the survivors. The list path runs this so a dead
// pointer — e.g. an entry whose bound transcript file has since been deleted,
// with no distilled essence to fall back on — never reaches a frontend; the
// removal is silent because such an entry is already unactionable. One atomic
// load+save under the lock, and a no-op write when nothing is dead.
func (m *Manager) Reconcile(isDead func(Entry) bool) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return nil, err
	}
	survivors := make([]Entry, 0, len(idx.Sessions))
	for _, e := range idx.Sessions {
		if !isDead(e) {
			survivors = append(survivors, e)
		}
	}
	if len(survivors) != len(idx.Sessions) {
		idx.Sessions = survivors
		if err := m.saveLocked(idx); err != nil {
			return nil, err
		}
	}
	return survivors, nil
}

// SetSummary updates the cached summary, detail lines, and source-size
// fingerprint on the index entry. summary mirrors the `summary:` line from the
// compacted essence.md frontmatter; detail holds the extra picker lines (Open
// Items); sourceSize is the transcript byte size the essence was distilled from,
// used for staleness detection (see TranscriptStale). Passing nil detail clears
// it; a zero sourceSize leaves the fingerprint unset (no staleness badge).
func (m *Manager) SetSummary(harpName, summary string, detail []string, sourceSize int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].HarpName != harpName {
			continue
		}
		idx.Sessions[i].Summary = summary
		idx.Sessions[i].Detail = detail
		idx.Sessions[i].SourceSize = sourceSize
		return m.saveLocked(idx)
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

func (m *Manager) saveLocked(idx *Index) error {
	data, err := yaml.Marshal(idx)
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	if err := iox.WriteFileAtomic(m.path, data, 0o644); err != nil {
		return err
	}
	// The file is now canonical, so any upgrade staged by the load that preceded
	// this save is no longer pending.
	m.pendingUpgrade = nil
	return nil
}

// generateUniqueHarp picks a fresh harp name that isn't in `used`.
// Generates up to 100 candidates; collision probability across 5.6M
// names is astronomically low even with thousands of sessions.
func generateUniqueHarp(used map[string]struct{}) string {
	for range 100 {
		name := harp.GenerateName()
		if _, dup := used[name]; !dup {
			return name
		}
	}
	return harp.GenerateName()
}
