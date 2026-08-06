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
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

// Entry is one row in index.yaml. The YAML keys are the on-disk contract; the
// json tags mirror them field-for-field so that any future direct JSON
// projection of an Entry is snake_case-identical to the file.
//
// No frontend reads this type as JSON today, and none should be assumed to:
// `ctxloom session list --format json` renders a separate rendering-time
// projection (internal/cli.SessionRow — harp/summary/start/end/essence_path),
// the list_sessions MCP tool renders its own sessionSummary, and the
// ctxloom://sessions/recent resource builds its own YAML row type. The json
// tags are therefore a shape guarantee, not a wire in use.
//
// The one deliberate asymmetry is CanonicalTranscriptPath: computed on read, so
// yaml:"-", but json-visible for a projection that wants capture presence.
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

	// LastActivity is the last-worked time used to order the resume picker:
	// most-recent-first by actual activity, not by session creation. Computed
	// on read (see ActivityTime) from the transcript's mtime when available,
	// falling back to StartedAt for a session that has no transcript yet or
	// whose transcript can't be stat'd. Never persisted and never exposed over
	// the JSON wire (`session list --format json`) — the same computed-on-read,
	// local-only posture as Distilled/EssencePath — so this is purely an
	// in-memory ordering aid.
	LastActivity time.Time `yaml:"-" json:"-"`

	// CanonicalTranscriptPath is the harp's OWN captured transcript
	// (paths.HarpCanonicalTranscriptPath — internal/transcript.Recorder's
	// output), computed on read like Distilled/
	// EssencePath — never persisted — by stat'ing the file (see
	// fillCanonicalTranscript, called from ListForProject). Empty means no
	// canonical transcript has landed for this harp yet: a pre-capture
	// session, an interactive-pty-only session (§2d/§4d — not tee'd), or a
	// chat that produced zero ChatEvents. This is the enumeration key
	// internal/transcript.CanonicalHistory (S3) uses to discover which of a
	// project's sessions it can serve, independent of the legacy per-engine
	// TranscriptPath; it does not change which reader any existing consumer
	// selects (that is S4's job).
	CanonicalTranscriptPath string `yaml:"-" json:"canonical_transcript_path,omitempty"`

	// PurgedAt records when `ctxloom session purge` destroyed this session's
	// machine-written bulk (transcript.jsonl, persist/transcripts/…). It is
	// why the row survives its own transcript: isUnrecoverable
	// (internal/operations/sessions.go) treats a non-nil PurgedAt as
	// recoverable-by-fiat, ahead of its transcript-existence check, so a
	// purged session stays visible in `session list` — marked, not gone —
	// rather than being silently reconciled away the next time anything
	// lists sessions. Additive + omitempty: an older binary reading this
	// index simply never sees it, and no index schema upgrade is needed.
	PurgedAt *time.Time `yaml:"purged_at,omitempty" json:"purged_at,omitempty"`
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

	unlock, err := m.lock()
	if err != nil {
		return err
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

	// Stages fired, so a rewrite is about to land over a non-empty index.
	// Nothing to write is not a successful write: an empty payload would
	// truncate the session index while this reports the upgrade as committed.
	if len(upgraded) == 0 {
		return fmt.Errorf("pending upgrade for %s carries no content; refusing to truncate it", m.path)
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

	unlock, err := m.lock()
	if err != nil {
		return Entry{}, err
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
	// Both empty is a genuine no-op — the ordinary shape of a hook
	// payload that carried no session identifier at all (session_cmd.go's
	// bindSessionFromPayload calls in with exactly this when none of its
	// fallbacks found one). Short-circuit before acquiring the file lock or
	// loading the index: without this, the call below would still assign
	// SessionID = "" (no actual change, since the entry was already unbound)
	// and perform a full index rewrite under lock for a call that changes
	// nothing.
	if sessionID == "" && transcriptPath == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return err
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
	// A transcript that already lives INSIDE the session dir needs no
	// reference link: a containerized run bind-mounts the engine's store root
	// at persist/transcripts, so the physical file is harp-addressable by
	// location (LocateTranscript) and a transcript.jsonl symlink would only
	// add a second name for it inside the same dir.
	if rel, err := filepath.Rel(dir, transcriptPath); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	// A binding whose target is not on disk still gets its link: BindSession
	// runs on MCP initialize and an engine may not have flushed its transcript
	// yet, so refusing would discard a link that becomes valid moments later.
	// But a dangling link is indistinguishable from a missing one at every
	// later read, so the stale binding is named here, where the cause is known.
	if _, serr := os.Stat(transcriptPath); serr != nil {
		clidiag.Warn("ctxloom", "transcript link: bound transcript %s does not resolve (%v); linking it anyway, but reads through the session dir will fail until it appears", transcriptPath, serr)
	}
	link := filepath.Join(dir, paths.CanonicalTranscriptFileName)
	// Only ABSENCE licenses moving on. Any other removal failure is reported
	// where it happened: the Symlink below then fails with EEXIST, which names
	// the symptom and hides the cause.
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		clidiag.Warn("ctxloom", "transcript link: could not replace the existing %s (%v)", link, err)
	}
	if err := os.Symlink(transcriptPath, link); err != nil {
		clidiag.Warn("ctxloom", "transcript link: %v", err)
	}
}

// LocateTranscript finds a harp's transcript BY LOCATION: the newest
// transcript-shaped file under ~/.ctxloom/sessions/<harp>/persist/transcripts,
// the dir a containerized run bind-mounts to the engine's native transcript
// store root. A containerized structured child never runs the SessionStart
// hook, so the normal BindSession path (which records TranscriptPath) never
// fires — but with the mount, the transcript physically lives in the harp's
// session dir, so location IS the binding. Engines nest their stores
// (claude: <encoded-project>/<uuid>.jsonl; antigravity:
// <uuid>/.system_generated/logs/transcript_full.jsonl), hence the recursive
// walk. The newest .jsonl wins (claude/codex/antigravity transcripts); .json
// is the kiro-store fallback considered only when no .jsonl exists.
func LocateTranscript(harpName string) (string, bool) {
	if harpName == "" {
		return "", false
	}
	root, err := paths.HarpTranscriptStoreDir(harpName)
	if err != nil {
		return "", false
	}
	var bestJSONL, bestJSON string
	var tJSONL, tJSON time.Time
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an absent/unreadable subtree degrades to "not found"
		}
		if d.IsDir() {
			// claude-code records each in-harness subagent's interior under
			// <session>/subagents/agent-<id>.jsonl. Those files are often the
			// newest in the store while a subagent runs, but they are not the
			// session's transcript — skip the subtree so "newest wins" cannot
			// resolve a harp to a subagent interior.
			if d.Name() == "subagents" {
				return fs.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		switch filepath.Ext(p) {
		case ".jsonl":
			keepNewest(&bestJSONL, &tJSONL, p, info.ModTime())
		case ".json":
			keepNewest(&bestJSON, &tJSON, p, info.ModTime())
		}
		return nil
	})
	if bestJSONL != "" {
		return bestJSONL, true
	}
	if bestJSON != "" {
		return bestJSON, true
	}
	return "", false
}

// keepNewest records path/modTime as the running winner when nothing has been
// chosen yet or this candidate is newer. The two extension arms of
// LocateTranscript's walk ran identical bookkeeping over different variables;
// naming it once means "newest wins" is defined in one place rather than
// re-agreed per file type.
func keepNewest(best *string, bestTime *time.Time, path string, modTime time.Time) {
	if *best == "" || modTime.After(*bestTime) {
		*best, *bestTime = path, modTime
	}
}

// fillTranscriptByLocation resolves a missing (or dangling) transcript binding
// by location for a returned entry COPY. Computed on read and never persisted
// — the same posture as the Distilled/EssencePath computed fields — so the
// on-disk index keeps only what a bind actually recorded. A live host-bound
// entry short-circuits on the stat and is untouched.
func fillTranscriptByLocation(e *Entry) {
	if e == nil || e.HarpName == "" {
		return
	}
	if e.TranscriptPath != "" {
		if _, err := os.Stat(e.TranscriptPath); err == nil {
			return
		}
	}
	if p, ok := LocateTranscript(e.HarpName); ok {
		e.TranscriptPath = p
	}
}

// fillCanonicalTranscript stats a harp's canonical transcript.jsonl
// (paths.ResolveHarpCanonicalTranscriptPath — falls back to the pre-rename
// transcript.acp.jsonl when only that one exists, so a session captured
// before the rename stays discoverable) and records its path on the entry
// COPY when present — the same computed-on-read posture as
// fillTranscriptByLocation/Distilled/EssencePath, never persisted. This is
// how a session becomes discoverable by ctxloom's own captured transcript
// independent of whatever the legacy engine-file
// TranscriptPath does or doesn't resolve to.
func fillCanonicalTranscript(e *Entry) {
	if e == nil || e.HarpName == "" {
		return
	}
	p, err := paths.ResolveHarpCanonicalTranscriptPath(e.HarpName)
	if err != nil {
		return
	}
	if _, err := os.Stat(p); err == nil {
		e.CanonicalTranscriptPath = p
	}
}

// Find returns a copy of the entry with the given harp name, or nil if
// absent. Enriches the copy with CanonicalTranscriptPath the same way
// ListForProject does (fillCanonicalTranscript) — S4: every
// single-harp lookup (compactor, sessionfeed, mcp memory tools) needs to see
// canonical-transcript presence consistently with the listing path, not just
// callers that go through ListForProject.
//
// internal/shared/tasks/projectid/registry.go's ResolveByPath/ResolveByID
// share this function's generic Load+IndexFunc+copy shape (a find-in-slice
// idiom that recurs across this codebase's several independent index/registry
// types) but are a DIFFERENT domain — a project-id registry, not the harp
// session index — with no canonical-transcript concept to mirror. This is a
// deliberate, reviewed divergence, not a missed sibling update.
// reprise:accept-drift
func (m *Manager) Find(harpName string) (*Entry, error) {
	idx, err := m.Load()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(idx.Sessions, func(e Entry) bool { return e.HarpName == harpName })
	if i < 0 {
		return nil, nil
	}
	out := idx.Sessions[i]
	fillTranscriptByLocation(&out)
	fillCanonicalTranscript(&out)
	return &out, nil
}

// ActivityTime returns e's last-worked time for resume-picker ordering: the
// transcript's mtime (canonical transcript preferred over the legacy
// TranscriptPath — see the body) when set and stat succeeds, falling back to
// StartedAt for a never-worked session (no transcript bound/located yet) or
// one whose transcript can no longer be stat'd. Exported so a caller
// merging in rows that never went through ListForProject (e.g. run.go's
// raw/not-yet-adopted backend transcript rows) can compute the same signal
// without duplicating the fallback logic.
//
// Callers should compute this ONCE per entry — e.g. stash it in
// Entry.LastActivity as ListForProject does below — rather than calling it
// from inside a sort comparator: a stat per comparison does not scale to a
// large index (100+ sessions means O(n log n) stats instead of O(n)).
func ActivityTime(e Entry) time.Time {
	// Prefer the canonical transcript (paths.HarpCanonicalTranscriptPath) over
	// the legacy TranscriptPath, mirroring SourceStale: an ACP/coordinator
	// session records ONLY a canonical transcript and never binds a legacy
	// TranscriptPath, so statting TranscriptPath alone pinned it to StartedAt
	// and mis-ranked it. Canonical is also the file the
	// compactor distills and SourceSize fingerprints — ordering and staleness
	// must agree on which file is the source of truth.
	if e.CanonicalTranscriptPath != "" {
		if info, err := os.Stat(e.CanonicalTranscriptPath); err == nil {
			return info.ModTime()
		}
	}
	if e.TranscriptPath != "" {
		if info, err := os.Stat(e.TranscriptPath); err == nil {
			return info.ModTime()
		}
	}
	return e.StartedAt
}

// ListForProject returns entries whose ProjectDir == projectDir, sorted
// most-recent-first by last-worked time (transcript mtime, falling back to
// StartedAt — see ActivityTime), not by creation time. A session that keeps
// getting resumed and worked must stay above a newer-CREATED-but-untouched
// one. Used by the picker.
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
	return enrichAndSortByActivity(out), nil
}

// ListAll returns every entry in the index — no project filter — enriched and
// ordered identically to ListForProject (most-recent-first by ActivityTime).
// Backs the all-projects listings (`session list --all`, the list_sessions MCP
// tool) so project-scoped and cross-project views sort the same way.
func (m *Manager) ListAll() ([]Entry, error) {
	idx, err := m.Load()
	if err != nil {
		return nil, err
	}
	return enrichAndSortByActivity(append([]Entry(nil), idx.Sessions...)), nil
}

// enrichAndSortByActivity fills each entry's computed transcript/canonical
// paths and LastActivity, then sorts most-recent-first by last-worked time
// (ActivityTime), with StartedAt as a deterministic tiebreak. Shared by
// ListForProject and ListAll so scoped and all-projects listings order
// identically. Mutates and returns the given slice.
func enrichAndSortByActivity(entries []Entry) []Entry {
	for i := range entries {
		fillTranscriptByLocation(&entries[i])
		fillCanonicalTranscript(&entries[i])
		// Computed once per entry here, not inside the sort comparator.
		entries[i].LastActivity = ActivityTime(entries[i])
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].LastActivity.Equal(entries[j].LastActivity) {
			return entries[i].LastActivity.After(entries[j].LastActivity)
		}
		// Deterministic tiebreak: same activity time (e.g. both fell back to
		// StartedAt, or transcripts stat'd to the same mtime granularity).
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	return entries
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
//
// Prefers CanonicalTranscriptPath over TranscriptPath (S4): once a
// harp has a captured canonical transcript, that IS the file the compactor
// actually distills from (memory.transcriptSize stamps its size the same way),
// so staleness must compare against it — comparing the essence's stamped size
// to the legacy engine file's size would compare two different sources and
// the badge would lie.
func (e Entry) SourceStale() (stale, known bool) {
	path := e.TranscriptPath
	if e.CanonicalTranscriptPath != "" {
		path = e.CanonicalTranscriptPath
	}
	return TranscriptStale(path, e.SourceSize)
}

// MarkEnded sets EndedAt on the named entry. Idempotent.
func (m *Manager) MarkEnded(harpName string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return err
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

// MarkPurged stamps PurgedAt on the named entry. Idempotent (re-marking an
// already-purged entry just advances the timestamp).
//
// Callers MUST call this BEFORE unlinking any files (`operations.PurgeSession`
// does): if the index write lands first and the process then dies mid-destroy,
// the row is left with PurgedAt set and a dangling TranscriptPath, which
// isUnrecoverable now reads as "purged on purpose" rather than "gone,
// forget it". Reversing the order — destroy then mark — reopens exactly the
// silent-deletion window PurgedAt exists to close: a transcript already gone,
// no PurgedAt yet written, and the next `session list` reconciles the row away
// for good.
func (m *Manager) MarkPurged(harpName string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return err
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
		idx.Sessions[i].PurgedAt = &t
		return m.saveLocked(idx)
	}
	return fmt.Errorf("harp not found: %q", harpName)
}

// Rename changes the harp name of an existing entry to newName, leaving
// SessionID and other fields intact. Errors if oldName doesn't exist, if
// newName is already in use, or if newName is not a usable harp identifier.
//
// The validation lives HERE, where the data is (the same posture as
// SetSummary's empty-summary refusal), not in operations.RenameSession: the
// new name becomes an index key that paths.HarpDir turns into a filesystem
// path, and `ctxloom session rename <old> ../..` was previously a
// pass-through all the way to MkdirAll/Symlink.
func (m *Manager) Rename(oldName, newName string) error {
	if err := harp.Validate(newName); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return err
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

	unlock, err := m.lock()
	if err != nil {
		return err
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
//
// CONTRACT ON isDead: it is called while this Manager holds BOTH its mutex and
// the index file lock, so it must not reach back into the session index by any
// route. Calling a method on this Manager self-deadlocks on the non-reentrant
// mutex; opening a second Manager over the same file and calling a locking
// method of ITS own blocks forever on the flock, which is exclusive and
// blocking even within one process. Neither fails — the process hangs with no
// error and no output. Judge only the Entry passed in, plus the filesystem
// (the production predicate, operations.isUnrecoverable, stats transcript
// files and nothing else).
//
// The predicate must stay inside the lock: the whole point is that the load,
// the judgement and the pruning save are one atomic sequence, so an entry
// cannot be judged against an index another writer has since changed.
func (m *Manager) Reconcile(isDead func(Entry) bool) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	idx, err := m.loadLocked()
	if err != nil {
		return nil, err
	}
	survivors := make([]Entry, 0, len(idx.Sessions))
	for _, e := range idx.Sessions {
		// Judge a COPY enriched with the computed-on-read canonical
		// transcript path. Judging the raw entry made the
		// recoverability test structurally blind: CanonicalTranscriptPath is
		// yaml:"-", so it is ALWAYS empty on a freshly loaded entry, and a
		// session whose vendor transcript had been pruned but whose
		// ctxloom-captured transcript.jsonl survived was reaped — by a
		// saveLocked, i.e. irreversibly.
		judged := e
		fillCanonicalTranscript(&judged)
		if !isDead(judged) {
			survivors = append(survivors, e)
			continue
		}
		// A silent irreversible delete deserves one line of output.
		clidiag.Warn("ctxloom", "session %s dropped from the index: its transcript is gone and it has no distilled essence", e.HarpName)
	}
	if len(survivors) != len(idx.Sessions) {
		idx.Sessions = survivors
		if err := m.saveLocked(idx); err != nil {
			return nil, err
		}
	}
	// Fill AFTER the save decision so the computed paths are computed-on-read
	// only, never persisted. (The canonical-transcript fill above is on a
	// throwaway copy for exactly the same reason.) The RETURNED entries get
	// both fills, so Reconcile's entries mean the same thing as Find's and
	// ListForProject's: an entry whose CanonicalTranscriptPath is empty must
	// mean no capture exists, not that this method skipped the stat —
	// SourceStale reads that field to decide which file the staleness
	// fingerprint is even about.
	for i := range survivors {
		fillTranscriptByLocation(&survivors[i])
		fillCanonicalTranscript(&survivors[i])
	}
	return survivors, nil
}

// SetSummary updates the cached summary, detail lines, and source-size
// fingerprint on the index entry. summary mirrors the `summary:` line from the
// compacted essence.md frontmatter; detail holds the extra picker lines (Open
// Items); sourceSize is the transcript byte size the essence was distilled from,
// used for staleness detection (see TranscriptStale). Passing nil detail clears
// it; a zero sourceSize leaves the fingerprint unset (no staleness badge).
//
// An EMPTY summary is refused. Accepting it made this the
// destructive variant of the house empty-input bug: SetSummary(harp, "", nil,
// 0) reported success while erasing a good summary, its detail lines AND its
// staleness fingerprint. A failed distill produces exactly that argument set,
// so the only thing standing between a distill hiccup and permanent loss of a
// session's summary was a caller remembering to check first (memory.
// Compactor.updateSessionIndex does; nothing made it). The refusal lives here,
// where the data is.
func (m *Manager) SetSummary(harpName, summary string, detail []string, sourceSize int64) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("refusing to set an empty summary on %q: that would erase the existing summary, detail lines and staleness fingerprint", harpName)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock()
	if err != nil {
		return err
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

// lockPath is the single home of the index's cooperative lock-file name. The
// ".lock" suffix is connascent across every mutating method: they must all name
// the same file or they silently stop excluding one another — no error, no
// warning, just two writers in one index.
func (m *Manager) lockPath() string { return filelock.PathFor(m.path) }

// lock takes the index's exclusive file lock and returns its release func,
// already wrapped for return. Every mutating method acquires through here, so
// the lock's identity and the error's shape are decided once rather than
// re-agreed at each call site.
func (m *Manager) lock() (func(), error) {
	unlock, err := filelock.Lock(m.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	return unlock, nil
}

// saveLocked atomically replaces the index file with idx.
//
// A nil idx is refused: yaml.Marshal(nil) is the literal `null`,
// and writing it would atomically overwrite every recorded session with a
// file that loads back as an empty index — a total, silent wipe with a nil
// error. No caller passes nil today; this is a guard on the destroy step, not
// a reaction to a live bug.
func (m *Manager) saveLocked(idx *Index) error {
	if idx == nil {
		return fmt.Errorf("refusing to write a nil session index (that would overwrite %s with `null`)", m.path)
	}
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

// generateUniqueHarp picks a fresh harp name not in `used`, through the shared
// allocator that also keys the project-id registry and per-project task ids —
// so an unresolvable collision errors here too, never a name that may collide.
func generateUniqueHarp(used map[string]struct{}) (string, error) {
	return harp.UniqueFrom(used, harp.GenerateName)
}
