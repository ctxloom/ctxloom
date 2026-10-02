// Package sessions tracks per-session metadata under the user-global sessions
// root, ~/.ctxloom/sessions. A session IS a directory there (IsSessionDir):
// ~/.ctxloom/sessions/<harp>/ carries a small sidecar, session.yaml, with the
// facts the directory cannot recover from its own contents — which project
// launched it, which engine owns it, the session ids it has been bound to and
// rotated past, when it was purged. Everything else a reader wants is derived
// on read: the harp name is the directory's, summary and detail come from
// essence.md, the canonical transcript path and last-activity time from the
// files themselves.
//
// There is no global index. One record per session, living in the session,
// means there is nothing to reconcile: a directory that exists is a session,
// a purged one says so in its own sidecar, and a forgotten one has no sidecar
// and is not listed. The listing is an enumeration, never a judgement.
//
// Backend-native session transcripts are still produced by the backend
// (e.g. Claude Code); this package only adds a cross-cutting
// harp-keyed layer on top.
package sessions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// lockFileMode and lockDirMode are the modes a sidecar's advisory-lock file
// and its parent directory are created with, before umask — not group- or
// world-WRITABLE, matching every other lock site in this project (see
// internal/core/agent/rmw_lock.go's identically-reasoned pair).
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// sidecarFileMode and sessionDirMode keep a session private to its owner: the
// sidecar records the session's MCP endpoint INCLUDING its bearer credential
// (Entry.MCP), which authenticates as this session to the runner. A container
// run reaches these files as the launching uid (the isolation identity
// contract), so owner-only access costs it nothing.
const (
	sidecarFileMode = 0o600
	sessionDirMode  = 0o700
)

// Entry is one session as a reader sees it: the sidecar's persisted facts
// plus the fields derived on read. The YAML keys are the sidecar's on-disk
// contract; the json tags mirror them field-for-field so that any future
// direct JSON projection of an Entry is snake_case-identical to the file.
//
// No frontend reads this type as JSON today, and none should be assumed to:
// `ctxloom session list --format json` renders a separate rendering-time
// projection (internal/adapters/cli.SessionRow — harp/summary/start/end/essence_path),
// the list_sessions MCP tool renders its own sessionSummary, and the
// ctxloom://sessions/recent resource builds its own YAML row type. The json
// tags are therefore a shape guarantee, not a wire in use.
type Entry struct {
	// HarpName is the session directory's name. Derived, never persisted: a
	// stored copy would have to be kept in step with a rename, and the
	// directory already says what it is called.
	HarpName string `yaml:"-" json:"harp_name"`

	SessionID  string     `yaml:"session_id,omitempty" json:"session_id,omitempty"` // empty until backend binds on initialize
	Backend    string     `yaml:"backend,omitempty" json:"backend,omitempty"`
	ProjectDir string     `yaml:"project_dir" json:"project_dir"`
	StartedAt  time.Time  `yaml:"started_at" json:"started_at"`
	EndedAt    *time.Time `yaml:"ended_at,omitempty" json:"ended_at,omitempty"`
	// TranscriptPath is the vendor transcript this harp is currently bound
	// to. Persisted because nothing in the directory reliably names it: the
	// engine-transcript-* symlink is best-effort and is deliberately not
	// created for a transcript that already lives inside the session dir.
	TranscriptPath string `yaml:"transcript_path,omitempty" json:"transcript_path,omitempty"`
	// MCP is the session's MCP endpoint, minted once per harp by the launch
	// resolver and bound here so a resume of the same harp reuses it; the
	// credential rides with it because the runner that binds the address
	// needs both. Omitted until bound.
	MCP Endpoint `yaml:"mcp,omitempty" json:"mcp,omitempty"`

	// Summary is essence.md's frontmatter `summary:` line, read on demand
	// (fillFromEssence) for a fast one-line render. Never persisted here —
	// the essence is the record, and a second copy is a second thing to
	// disagree.
	Summary string `yaml:"-" json:"summary,omitempty"`
	// Detail holds the distilled Open Items, derived from essence.md's body
	// the same way. Kept separate from Summary so the single-line consumers
	// (session list table, MCP resource) stay one line while a multi-line
	// renderer can show more.
	Detail []string `yaml:"-" json:"detail,omitempty"`

	// SourceEntries is the transcript's ENTRY COUNT at the moment this session
	// was last distilled — the staleness fingerprint. `session list` counts the
	// live transcript's entries and flags the row "out of date" once more have
	// arrived.
	//
	// It counts PROGRESS, not bytes, and that distinction is load-bearing. The
	// previous fingerprint was byte size, justified by "append-only transcripts
	// only grow" — true of a CAPTURED transcript and false of a CONVERTED one,
	// because operations.RefreshVendorTranscript replaces the canonical file
	// wholesale. Under byte size, changing a vendor adapter's field set or
	// timestamp format re-sized every canonical transcript and falsely staled
	// every essence, each costing a real LLM re-distillation; and two different
	// contents of equal length compared as current. An entry count moves only
	// when the conversation actually advances and is invariant to
	// re-serialisation.
	//
	// The trade accepted: an edit WITHIN an existing entry is invisible here,
	// where a byte size would have caught it. For a derived artifact that is
	// the right way round — re-serialisation is common, in-place entry edits
	// are not.
	//
	// Zero when never distilled; omitempty keeps those sidecars clean.
	SourceEntries int `yaml:"source_entries,omitempty" json:"source_entries,omitempty"`

	// LastActivity is the last-worked time used to order `session list`:
	// most-recent-first by actual activity, not by session creation. Computed
	// on read from the one clock (ActivityTime — the newest mtime under the
	// session dir), falling back to StartedAt when the dir cannot be read.
	// Never persisted and never exposed over the JSON wire (`session list
	// --format json`) — the same computed-on-read, local-only posture as
	// CanonicalTranscriptPath — so this is purely an in-memory ordering aid.
	LastActivity time.Time `yaml:"-" json:"-"`

	// CanonicalTranscriptPath is the harp's OWN captured transcript
	// (paths.HarpCanonicalTranscriptPath — internal/adapters/transcript.Recorder's
	// output), computed on read — never persisted — by stat'ing the file (see
	// fillCanonicalTranscript). Empty means no canonical transcript has
	// landed for this harp yet: a pre-capture session, an interactive-pty-only
	// session (§2d/§4d — not tee'd), or a chat that produced zero ChatEvents.
	// This is the enumeration key internal/adapters/transcript.CanonicalHistory (S3)
	// uses to discover which of a project's sessions it can serve,
	// independent of the legacy per-engine TranscriptPath.
	CanonicalTranscriptPath string `yaml:"-" json:"canonical_transcript_path,omitempty"`

	// PurgedAt records when `ctxloom session purge` destroyed this session's
	// machine-written bulk (transcript.jsonl, persist/transcripts/…). A purge
	// removes FILES and never the directory, so what it leaves is a real
	// session directory missing its content — indistinguishable from damage
	// except by this stamp. It is why a purged session stays visible in
	// `session list`, marked, rather than being read as a live session that
	// lost its transcript.
	PurgedAt *time.Time `yaml:"purged_at,omitempty" json:"purged_at,omitempty"`

	// EngineVersion is what the engine's own CLI said it was — verbatim, as
	// `claude --version` printed it — at the moment this session STARTED. It
	// is the key that selects a vendor transcript reader validated against
	// that format (vendorreader.SelectAdapter).
	//
	// It is recorded, not probed on demand, because a probe reports what is
	// installed NOW and never what WROTE these bytes: a session run under
	// claude-code 2.1 and read after upgrading to 2.2 must still be read by
	// 2.1's adapter. Selecting on a live probe would confidently mis-parse
	// exactly the sessions that most need care.
	//
	// EMPTY MEANS UNKNOWN, AND UNKNOWN REFUSES. A session that predates this
	// field, or one whose engine could not be asked (binary gone, version
	// command failed), carries nothing here — and the read path says so and
	// stops rather than guessing at the newest adapter. That refusal is
	// recoverable; a silent mis-parse on the path that feeds a model is not.
	EngineVersion string `yaml:"engine_version,omitempty" json:"engine_version,omitempty"`

	// Origin is who the session was minted for (Seed.Origin), stamped by the
	// mint. EMPTY READS AS A HUMAN'S SESSION: a session that predates the
	// field, or whose stamp failed, is never purged undistilled.
	Origin Origin `yaml:"origin,omitempty" json:"origin,omitempty"`

	// SigCheckDisabled records that the run which minted this session waived
	// bundle signature verification (--disable-sig-check), so a session that
	// delivered unverified content can be told apart afterwards. Stamped by
	// the mint with Origin; absent means the check was enforced.
	SigCheckDisabled bool `yaml:"sig_check_disabled,omitempty" json:"sig_check_disabled,omitempty"`

	// Rotations records every binding this harp has DISPLACED, oldest first.
	// claude-code's /clear starts a fresh session UUID and transcript file
	// under the same live process, firing SessionStart again; without this, a
	// harp that had been /clear'd lost the vendor file naming everything said
	// before the clear, and the canonical-transcript rebuild
	// (RefreshVendorTranscript) had nothing left to rebuild it from. The
	// rotated-away ids are recorded here and nowhere else, which is why
	// FindBySessionID searches them.
	//
	// Appended to by BindSession exactly when a rebind DISPLACES the current
	// binding (a different, non-empty session ID arrives with a transcript
	// path) — see BindSession's doc comment for the full rebind rule this
	// piggybacks on. Never appended to for an idempotent same-id rebind or an
	// id-only bind, since neither displaces anything.
	Rotations []Rotation `yaml:"rotations,omitempty" json:"rotations,omitempty"`
}

// Rotation is one displaced binding in an Entry's lineage: the session ID and
// vendor transcript path a harp was bound to before a later rebind replaced
// it (see Entry.Rotations). TranscriptPath is the vendor file's location AT
// THE MOMENT OF DISPLACEMENT, tracked so a later canonical rebuild
// (operations.RefreshVendorTranscript) can still find it even though the
// entry itself now points at the current binding.
type Rotation struct {
	SessionID      string    `yaml:"session_id" json:"session_id"`
	TranscriptPath string    `yaml:"transcript_path,omitempty" json:"transcript_path,omitempty"`
	RotatedAt      time.Time `yaml:"rotated_at" json:"rotated_at"`
}

// Manager is the filesystem session store: the session directories under
// root and their sidecars. Constructed via Open; every mutation goes through
// its methods so the per-session file lock and the sidecar's contents stay
// consistent.
type Manager struct {
	root string
	mu   sync.Mutex
	// rep receives the findings a listing or a bind raises about one
	// session without failing the whole operation (a corrupt sidecar, a
	// transcript link that could not be made). The caller renders them.
	rep report.Reporter
}

// Open returns a Manager over the sessions root — ~/.ctxloom/sessions, the
// same root every harp-derived path (paths.HarpDir and its family) resolves
// through, so the directories this Manager enumerates and the files the
// fill helpers stat are one tree. There is no override: two roots would be
// two answers to "which sessions exist".
//
// A retired global index (index.yaml) at the root is not read: the session
// directories and their sidecars are the only source of sessions.
//
// sink receives the per-session findings the Manager raises without failing
// an operation; nil discards them.
func Open(sink report.Sink) (*Manager, error) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	if err := os.MkdirAll(root, lockDirMode); err != nil {
		return nil, fmt.Errorf("mkdir sessions dir: %w", err)
	}
	return &Manager{root: root, rep: report.To(sink)}, nil
}

// Root returns the sessions root this Manager enumerates.
func (m *Manager) Root() string { return m.root }

// readSidecar returns the sidecar-backed Entry for harpName, or nil when the
// directory carries no sidecar (not a session, or forgotten) — or when the
// name could not be a session at all (harp.Validate), which is just a
// stronger way of not existing. An unparseable sidecar is an error: the
// caller decides whether that is fatal (a one-harp lookup) or skippable (a
// listing, where one corrupt sibling must not hide every other session).
func (m *Manager) readSidecar(harpName string) (*Entry, error) {
	if harp.Validate(harpName) != nil {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(m.root, harpName, paths.SessionSidecarFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := yaml.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("parse %s for %s: %w", paths.SessionSidecarFileName, harpName, err)
	}
	e.HarpName = harpName
	return &e, nil
}

// writeSidecar atomically replaces harpName's sidecar with e. Durable: the
// sidecar is the only record of the session's rotation lineage, and a rename
// that silently reverts after a crash loses that lineage with no signal.
func (m *Manager) writeSidecar(harpName string, e *Entry) error {
	data, err := yaml.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", paths.SessionSidecarFileName, err)
	}
	fs := afero.NewOsFs()
	dir := filepath.Join(m.root, harpName)
	if err := fs.MkdirAll(dir, sessionDirMode); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and the session dir
	// normally exists before its first sidecar write (launch lays out
	// persist/ and ephemeral/ under it with the default mode).
	if err := fs.Chmod(dir, sessionDirMode); err != nil {
		return fmt.Errorf("restrict session dir: %w", err)
	}
	return safefs.WriteFile(fs, filepath.Join(dir, paths.SessionSidecarFileName), data, sidecarFileMode, safefs.Durable())
}

// lock takes harpName's exclusive sidecar lock (paths.HarpSidecarLockPath)
// and returns its release func, already wrapped for return. Every mutating
// method acquires through here, so the lock's identity and the error's shape
// are decided once rather than re-agreed at each call site.
func (m *Manager) lock(harpName string) (func(), error) {
	lockPath, err := paths.HarpSidecarLockPath(harpName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		return nil, fmt.Errorf("lock: prepare lock directory: %w", err)
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	lockErr := fl.Lock()
	stop()
	if lockErr != nil {
		return nil, fmt.Errorf("lock: %w", lockErr)
	}
	return func() { _ = fl.Unlock() }, nil
}

// update applies mutate to harpName's sidecar under its lock: read, mutate,
// write. mutate reports whether it changed anything; an unchanged entry is
// not rewritten. A harp with no sidecar is an error — every mutator names an
// existing session.
func (m *Manager) update(harpName string, mutate func(e *Entry) (changed bool, err error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock(harpName)
	if err != nil {
		return err
	}
	defer unlock()

	e, err := m.readSidecar(harpName)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("%w: %q", ErrNotFound, harpName)
	}
	changed, err := mutate(e)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return m.writeSidecar(harpName, e)
}

// AssignHarp mints a fresh session: a harp name no directory under the root
// already uses, its directory, and a sidecar carrying the project dir and
// backend. Returns the assigned Entry.
//
// Used by `ctxloom run` pre-launch: the entry is "pending" because
// SessionID is not known until the spawned LLM's MCP server calls
// initialize. Use BindSession to fill that in.
//
// Uniqueness is the directory's: os.Mkdir either creates the name or fails
// with EEXIST, so two concurrent assigners cannot both take it, and a name
// that exists as a forgotten (sidecar-less) directory is skipped too rather
// than adopted — its leftover files belong to whatever session that was.
func (m *Manager) AssignHarp(projectDir, backend string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	names, err := os.ReadDir(m.root)
	if err != nil {
		return Entry{}, fmt.Errorf("read sessions root: %w", err)
	}
	used := make(map[string]struct{}, len(names))
	for _, n := range names {
		used[n.Name()] = struct{}{}
	}
	name, err := generateUniqueHarp(used)
	if err != nil {
		return Entry{}, err
	}
	if err := os.Mkdir(filepath.Join(m.root, name), lockDirMode); err != nil {
		return Entry{}, fmt.Errorf("mint session dir %s: %w", name, err)
	}
	entry := Entry{
		HarpName:   name,
		Backend:    backend,
		ProjectDir: projectDir,
		StartedAt:  time.Now().UTC(),
	}
	if err := m.writeSidecar(name, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// BindMCP records the session's MCP endpoint. Called by the launch resolver
// once per harp; a resume reads it back through Find and reuses it unless
// it asks for a rebind, which calls this again with the fresh endpoint.
func (m *Manager) BindMCP(harpName string, ep Endpoint) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		if e.MCP == ep {
			return false, nil
		}
		e.MCP = ep
		return true, nil
	})
}

// BindEngine records the engine the launch resolver decided for the
// session. The mint precedes resolution and so cannot know it; a later
// launch of the same harp may change it only by resolving to another.
func (m *Manager) BindEngine(harpName, engine string) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		if e.Backend == engine {
			return false, nil
		}
		e.Backend = engine
		return true, nil
	})
}

// BindSession fills in the backend-native session ID and transcript path
// for an existing harp-named entry. Called from the MCP initialize
// handler once the backend has bootstrapped enough to know its session
// UUID.
//
// A repeat bind carrying the SAME session ID is a no-op (idempotent hook
// re-runs). A DIFFERENT session ID arriving together with a transcript path
// is a ROTATION and the binding follows it: an engine can start a fresh
// transcript file under a still-live process (claude-code's /clear does
// exactly this, firing SessionStart again with the new UUID), and a binding
// pinned to the first transcript a harp ever had goes on naming a file the
// session stopped growing hours or days ago. Every reader that trusts the
// binding — recover, resume, transcript watch, essence staleness — then reads
// that dead file and reports success, which is the worst available outcome.
//
// A different ID with NO transcript path still loses to an existing binding:
// that is the compactor's forward-bind backstop, which knows an ID but not a
// file, and it must not displace what the SessionStart hook established. An
// empty ID likewise never blanks a live binding.
//
// A displacement does not DISCARD the binding it replaces: the old
// session_id/transcript_path are appended to Entry.Rotations before being
// overwritten (see its doc comment): without them the canonical-transcript
// rebuild has only the new, empty-at-/clear vendor file to rebuild from, and
// /recover finds nothing even though the pre-clear conversation is still on
// disk under the old session ID.
func (m *Manager) BindSession(harpName, sessionID, transcriptPath string) error {
	// Both empty is a genuine no-op — the ordinary shape of a hook
	// payload that carried no session identifier at all (session_cmd.go's
	// bindSessionFromPayload calls in with exactly this when none of its
	// fallbacks found one). Short-circuit before acquiring the lock.
	if sessionID == "" && transcriptPath == "" {
		return nil
	}
	return m.update(harpName, func(e *Entry) (bool, error) {
		if cur := e.SessionID; cur != "" {
			if sessionID == cur {
				return false, nil
			}
			// Defense-in-depth for the SessionStart-vs-compact-vs-scan race
			// the caller-side checks already guard against: only a binder
			// that names a transcript file is allowed to re-point.
			if sessionID == "" || transcriptPath == "" {
				return false, nil
			}
			// A DISPLACEMENT: cur is about to be overwritten by sessionID.
			// Preserve it in Rotations (oldest first) before that happens —
			// see Entry.Rotations' doc comment for why.
			e.recordRotation(cur)
		}
		if sessionID != "" {
			e.SessionID = sessionID
		}
		if transcriptPath != "" {
			e.TranscriptPath = transcriptPath
			// Create THIS binding's own immutable engine-transcript symlink
			// (see linkEngineTranscript's doc) — never a retroactive one for
			// the entry a rotation just displaced (cur, above): that
			// binding's link was already created when IT was current, at its
			// own bind. Best-effort: a failure must not block the bind.
			for _, f := range linkEngineTranscript(harpName, e.Backend, sessionID, transcriptPath) {
				m.rep.Report(f)
			}
		}
		return true, nil
	})
}

// recordRotation appends the current binding (cur and its transcript) to
// the lineage, unless cur is already recorded: a duplicate lineage entry
// would double-convert that segment on rebuild.
func (e *Entry) recordRotation(cur string) {
	for _, r := range e.Rotations {
		if r.SessionID == cur {
			return
		}
	}
	e.Rotations = append(e.Rotations, Rotation{
		SessionID:      cur,
		TranscriptPath: e.TranscriptPath,
		RotatedAt:      time.Now().UTC(),
	})
}

// AppendRotations adds rotations to harpName's lineage OUTSIDE the live
// rebind path — for `ctxloom session adopt`, which discovers vendor
// transcripts a rotation the store never recorded (pre-lineage-fix /clears)
// left orphaned, and needs to seed them into Rotations without a rebind ever
// happening. BindSession's own Rotations append (see its doc comment) only
// fires as the side effect of a LIVE rebind; this is the same append shape
// offered directly, for a caller that already knows the exact Rotation
// records to add.
//
// Each incoming rotation is skipped, not erred on, when its SessionID is
// already the entry's current binding or already present in Rotations — the
// same "alreadyRecorded" idempotency BindSession's rebind uses, so a repeat
// adopt run over a harp it already touched is a safe no-op rather than a
// duplicate-lineage hazard.
//
// The surviving rotations are appended and the WHOLE slice is then
// RE-SORTED by RotatedAt ascending — not left in call order. Rotations is
// documented (and the harp-lifetime canonical rebuild, operations.
// convertVendorTranscript, depends on it) as oldest-first array order; a
// caller adopting a vendor file that predates every rotation already on
// record — the ordinary shape for a pre-fix orphan, which by construction
// is OLDER than everything the store has ever known about this harp — must
// land BEFORE them, not after. Sorting by RotatedAt (already the field that
// carries each rotation's chronological position, set at real rebind time
// for an existing entry and computed by the caller to mean the same thing
// for an adopted one) gets every caller — insert-before, insert-between,
// insert-after — right with one rule instead of the caller having to find
// its own splice index.
func (m *Manager) AppendRotations(harpName string, rotations []Rotation) error {
	if len(rotations) == 0 {
		return nil
	}
	return m.update(harpName, func(e *Entry) (bool, error) {
		existing := make(map[string]struct{}, len(e.Rotations)+1)
		if e.SessionID != "" {
			existing[e.SessionID] = struct{}{}
		}
		for _, r := range e.Rotations {
			existing[r.SessionID] = struct{}{}
		}
		changed := false
		for _, r := range rotations {
			if _, dup := existing[r.SessionID]; dup {
				continue
			}
			e.Rotations = append(e.Rotations, r)
			existing[r.SessionID] = struct{}{}
			changed = true
		}
		if !changed {
			return false, nil
		}
		sort.SliceStable(e.Rotations, func(a, b int) bool {
			return e.Rotations[a].RotatedAt.Before(e.Rotations[b].RotatedAt)
		})
		return true, nil
	})
}

// enrich fills an entry's derived fields: the transcript located by
// position, the canonical transcript, and essence.md's summary/detail. Every
// read path — one-harp lookups and listings alike — goes through it, so an
// entry whose CanonicalTranscriptPath is empty means no capture exists, not
// that this path skipped the stat; SourceStale reads that field to decide
// which file the staleness fingerprint is even about.
func enrich(e *Entry) {
	fillTranscriptByLocation(e)
	fillCanonicalTranscript(e)
	fillFromEssence(e)
}

// Find returns the entry with the given harp name, enriched, or nil if no
// session directory of that name carries a sidecar. It reads exactly one
// sidecar: a one-harp lookup never walks the root, so a sibling session's
// corrupt sidecar is not its concern.
func (m *Manager) Find(harpName string) (*Entry, error) {
	e, err := m.readSidecar(harpName)
	if err != nil || e == nil {
		return nil, err
	}
	enrich(e)
	return e, nil
}

// FindBySessionID returns the entry whose CURRENT SessionID equals
// sessionID, OR — the lineage lookup Find cannot do — whose Rotations carries
// sessionID as a session id it was PREVIOUSLY bound to before a /clear rotated
// it away. A backend-native id an old vendor transcript, a stale hook payload,
// or a caller's own memory of "the session before the clear" still names is
// otherwise unresolvable to any harp once BindSession has re-pointed the
// entry past it.
//
// This one necessarily walks every sidecar: a session id is recorded only
// in the sidecar of the harp that owns it.
func (m *Manager) FindBySessionID(sessionID string) (*Entry, error) {
	if sessionID == "" {
		return nil, nil
	}
	entries, err := m.enumerate()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		e := &entries[i]
		if e.SessionID == sessionID {
			enrich(e)
			return e, nil
		}
		for _, r := range e.Rotations {
			if r.SessionID == sessionID {
				enrich(e)
				return e, nil
			}
		}
	}
	return nil, nil
}

// enumerate reads every session directory's sidecar under the root, in
// directory order, without enrichment. A sidecar that cannot be parsed is
// warned about and skipped — one corrupt session must not hide every other —
// and a root that does not exist yet is simply empty (first run).
func (m *Manager) enumerate() ([]Entry, error) {
	dirents, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions root: %w", err)
	}
	var out []Entry
	for _, d := range dirents {
		if !IsSessionDir(m.root, d) {
			continue
		}
		e, err := m.readSidecar(d.Name())
		if err != nil {
			m.rep.Warnf("session %s skipped: %v", d.Name(), err)
			continue
		}
		if e == nil {
			continue // raced a Forget between the predicate and the read
		}
		out = append(out, *e)
	}
	return out, nil
}

// ListForProject returns entries whose ProjectDir == projectDir, sorted
// most-recent-first by last-worked time (ActivityTime), not by creation
// time. A session that keeps getting resumed and worked must stay above a
// newer-CREATED-but-untouched one.
func (m *Manager) ListForProject(projectDir string) ([]Entry, error) {
	all, err := m.enumerate()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.ProjectDir == projectDir {
			out = append(out, e)
		}
	}
	return enrichAndSortByActivity(out), nil
}

// ListAll returns every session — no project filter — enriched and ordered
// identically to ListForProject (most-recent-first by ActivityTime). Backs
// the all-projects listings (`session list --all`, the list_sessions MCP
// tool) so project-scoped and cross-project views sort the same way.
func (m *Manager) ListAll() ([]Entry, error) {
	all, err := m.enumerate()
	if err != nil {
		return nil, err
	}
	return enrichAndSortByActivity(all), nil
}

// enrichAndSortByActivity fills each entry's derived fields and LastActivity,
// then sorts most-recent-first by last-worked time (ActivityTime, the one
// clock), with StartedAt as a deterministic tiebreak. Shared by
// ListForProject and ListAll so scoped and all-projects listings order
// identically. Mutates and returns the given slice.
//
// The layout is the home one, resolved once per listing: it is the same
// tree every paths.Harp* helper the enrichment stats resolves through, so
// the clock and the derived paths read one session.
func enrichAndSortByActivity(entries []Entry) []Entry {
	l, lerr := HomeLayout()
	for i := range entries {
		enrich(&entries[i])
		// Computed once per entry here, not inside the sort comparator: a
		// walk per comparison does not scale to a large store.
		entries[i].LastActivity = lastActivity(l, lerr, entries[i])
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].LastActivity.Equal(entries[j].LastActivity) {
			return entries[i].LastActivity.After(entries[j].LastActivity)
		}
		// Deterministic tiebreak: same activity time (e.g. both fell back to
		// StartedAt, or members stamped at the same mtime granularity).
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	return entries
}

// lastActivity is ActivityTime with the listing's fallback: a session whose
// dir cannot be read (a record with no directory — a MemStore entry, a
// forgotten session) orders by StartedAt rather than sinking a zero time to
// the bottom.
func lastActivity(l Layout, lerr error, e Entry) time.Time {
	if lerr != nil {
		return e.StartedAt
	}
	at, err := ActivityTime(l, e.HarpName)
	if err != nil || at.IsZero() {
		return e.StartedAt
	}
	return at
}

// MarkEnded sets EndedAt on the named entry. Idempotent.
func (m *Manager) MarkEnded(harpName string, at time.Time) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		t := at.UTC()
		e.EndedAt = &t
		return true, nil
	})
}

// MarkPurged stamps PurgedAt on the named entry. Idempotent (re-marking an
// already-purged entry just advances the timestamp).
//
// Callers MUST call this BEFORE unlinking any files (`operations.PurgeSession`
// does): if the process dies mid-destroy, the sidecar already says the
// missing content was removed on purpose. Reversing the order — destroy then
// mark — leaves a window in which the directory looks like a live session
// that lost its transcript, which is exactly the damage-vs-decision confusion
// PurgedAt exists to close.
func (m *Manager) MarkPurged(harpName string, at time.Time) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		t := at.UTC()
		e.PurgedAt = &t
		return true, nil
	})
}

// RecordEngineVersion stamps EngineVersion on the named entry — what the
// engine's CLI reported at session start, which is later the ONLY thing that
// selects a vendor transcript reader for this session.
//
// Separate from AssignHarp rather than an extra parameter to it: probing an
// engine binary is an exec, and AssignHarp is on `ctxloom run`'s pre-launch
// path. Keeping the probe outside means a slow or hanging engine binary
// cannot stall harp assignment.
//
// An empty version is a no-op, not a write: a failed probe must leave the
// field UNSET so the read path can tell "never recorded" from "recorded as
// nothing". Writing "" would be indistinguishable from a pre-field session and
// would look, on inspection, like the probe had succeeded.
func (m *Manager) RecordEngineVersion(harpName, version string) error {
	if version == "" {
		return nil
	}
	return m.update(harpName, func(e *Entry) (bool, error) {
		e.EngineVersion = version
		return true, nil
	})
}

// StampMint records what the mint knew on the named entry.
func (m *Manager) StampMint(harpName string, s MintStamp) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		s.apply(e)
		return true, nil
	})
}

// SetSourceEntries stamps the staleness fingerprint: the transcript ENTRY
// COUNT the essence was just distilled from (see Entry.SourceEntries and
// TranscriptStale). The summary and detail the essence carries are read from
// essence.md itself; nothing about them is stored here.
func (m *Manager) SetSourceEntries(harpName string, sourceEntries int) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		e.SourceEntries = sourceEntries
		return true, nil
	})
}

// Rename moves the session directory from oldName to newName — the directory
// IS the record, so its essence, persisted files and sidecar all travel
// together. Errors if oldName is not a session, if anything already sits at
// newName, or if newName is not a usable harp identifier or runs past
// harp.MaxNameLen.
//
// The validation lives HERE, where the data is, not in
// operations.RenameSession: the new name becomes a path component under the
// sessions root, and `ctxloom session edit <old> --name ../..` was previously
// a pass-through all the way to MkdirAll/Symlink.
func (m *Manager) Rename(oldName, newName string) error {
	if err := harp.ValidateRename(newName); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock(oldName)
	if err != nil {
		return err
	}
	defer unlock()

	cur, err := m.readSidecar(oldName)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("harp not found: %q", oldName)
	}
	newDir := filepath.Join(m.root, newName)
	if _, err := os.Lstat(newDir); err == nil {
		return fmt.Errorf("name already in use: %q", newName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rename %s: inspect %s: %w", oldName, newName, err)
	}
	if err := os.Rename(filepath.Join(m.root, oldName), newDir); err != nil {
		return fmt.Errorf("rename session dir: %w", err)
	}
	return nil
}

// Forget removes the harp's sidecar, and with it the session from every
// listing. The directory and everything else in it — transcript, essence,
// authored files — are left untouched: only the record goes away.
func (m *Manager) Forget(harpName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := m.lock(harpName)
	if err != nil {
		return err
	}
	defer unlock()

	sidecar, err := paths.HarpSidecarPath(harpName)
	if err != nil {
		return err
	}
	if err := os.Remove(sidecar); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("harp not found: %q", harpName)
		}
		return err
	}
	return nil
}

// generateUniqueHarp picks a fresh harp name not in `used`, through the shared
// allocator that also keys the project-id registry and per-project task ids —
// so an unresolvable collision errors here too, never a name that may collide.
func generateUniqueHarp(used map[string]struct{}) (string, error) {
	return harp.UniqueFrom(used, harp.GenerateName)
}
