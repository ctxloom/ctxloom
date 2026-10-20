// Package sessions tracks per-session metadata under the user-global sessions
// root, ~/.ctxloom/sessions. A session IS a directory there (IsSessionDir):
// ~/.ctxloom/sessions/<harp>/ carries a small sidecar, session.yaml, with the
// facts the directory cannot recover from its own contents — which project
// launched it, which engine owns it, the session ids it has been bound to and
// rotated past, when it was purged. Everything else a reader wants is derived
// on read: the harp name is the directory's, the canonical transcript path
// and last-activity time from the files themselves.
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
	"syscall"
	"time"

	sessionsidecarmig "github.com/ctxloom/ctxloom/internal/migrations/sessionsidecar"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// lockDirMode is the mode the sessions root and a session directory are
// created with, before umask: traversable, not group- or world-WRITABLE.
const lockDirMode = 0o755

// sidecarFileMode and sessionDirMode keep a session private to its owner: the
// directory holds the session's transcript and delivered engine config, and the sidecar names the native session key that resumes it. The
// engine endpoint's bearer is deliberately NOT here: it is minted per launch
// and lives only in that launch's memory and delivered config. A container
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
// projection (internal/adapters/cli.SessionRow — harp/summary/state/start/end)
// and `session show` the operations.SessionView read model. The json tags are
// therefore a shape guarantee, not a wire in use.
type Entry struct {
	// HarpName is the session directory's name. Derived, never persisted: a
	// stored copy would have to be kept in step with a rename, and the
	// directory already says what it is called.
	HarpName string `yaml:"-" json:"harp_name"`

	SessionID  string `yaml:"session_id,omitempty" json:"session_id,omitempty"` // empty until backend binds on initialize
	Backend    string `yaml:"backend,omitempty" json:"backend,omitempty"`
	ProjectDir string `yaml:"project_dir" json:"project_dir"`
	// OutputDir is the session's human root: where its readable outputs
	// (plans, published reports) go.
	// Recorded ABSOLUTE at mint (RecordOutputDir) because its default
	// derives from per-platform and per-user state and its base is
	// configurable: re-deriving it later could name a different folder than
	// the one the session's files are in.
	OutputDir string     `yaml:"output_dir,omitempty" json:"output_dir,omitempty"`
	StartedAt time.Time  `yaml:"started_at" json:"started_at"`
	EndedAt   *time.Time `yaml:"ended_at,omitempty" json:"ended_at,omitempty"`
	// TranscriptPath is the vendor transcript this harp is currently bound
	// to. Persisted because nothing in the session dir names it: claude's
	// native file is found under native/ by location (LocateTranscript), but
	// a host-home binding lives wherever the engine put it.
	TranscriptPath string `yaml:"transcript_path,omitempty" json:"transcript_path,omitempty"`

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
	// It is what a reader of a session's captured transcript
	// (internal/adapters/transcript.ParseTranscriptFile) is handed,
	// independent of the per-engine TranscriptPath.
	CanonicalTranscriptPath string `yaml:"-" json:"canonical_transcript_path,omitempty"`

	// PurgedAt records when `ctxloom session purge` destroyed this session's
	// machine-written bulk (transcripts/, native/). A purge
	// removes FILES and never the directory, so what it leaves is a real
	// session directory missing its content — indistinguishable from damage
	// except by this stamp. It is why a purged session stays visible in
	// `session list`, marked, rather than being read as a live session that
	// lost its transcript.
	PurgedAt *time.Time `yaml:"purged_at,omitempty" json:"purged_at,omitempty"`

	// Keep marks the session's transcript as never reclaimable, at any age
	// (Retention): set by `ctxloom session keep`, cleared by its --undo.
	// The transcript is the session's record, and this is how a human says
	// one is worth keeping past session_transcript_retention.
	Keep bool `yaml:"keep,omitempty" json:"keep,omitempty"`

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
	// field, or whose stamp failed, keeps its transcript until its retention
	// passes.
	Origin Origin `yaml:"origin,omitempty" json:"origin,omitempty"`

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

	// RunEnds records how this session's runs ENDED, oldest first: the
	// cause, its detail, and the runner's exit status when one is known.
	// EndedAt says only when the session ended; without this a session whose
	// runner was killed — a signal, an OOM kill — read afterwards exactly like
	// one that finished. One entry per run (a run's later end replaces its
	// earlier one), so a resume that fails to launch does not erase the end
	// of the run it was resuming. Bounded to the newest MaxRunEnds; the
	// coordinator's run journal keeps every end.
	RunEnds []RunEnd `yaml:"run_ends,omitempty" json:"run_ends,omitempty"`

	// EndReason records why a session ended WITHOUT its engine ever having
	// launched: the run was refused or failed after the mint (the resolver
	// prepares the session home) and before the engine started. EndedAt says
	// only when, and the terminal output that said why is gone with the
	// terminal; without this such a session is a directory and a timestamp.
	// Nil for a session whose engine launched — the engine's transcript and
	// status cover those — except a run whose engine refused its credential
	// after launch, which ends as a refusal.
	EndReason *EndReason `yaml:"end_reason,omitempty" json:"end_reason,omitempty"`
}

// The classes of an EndReason.
const (
	// EndClassRefused: ctxloom declined to run (exit 2), the fix being the
	// human's to apply.
	EndClassRefused = "refused"
	// EndClassFatalStartup: startup aborted over collected fatal findings
	// (exit 3).
	EndClassFatalStartup = "fatal-startup"
	// EndClassFailed: ctxloom's own error, with no engine status (exit 1).
	EndClassFailed = "failed"
	// EndClassCancelled: the run was ended by a signal or its context.
	EndClassCancelled = "cancelled"
)

// EndReason is why a session ended before its engine launched (Entry.EndReason).
// Exit is `ctxloom run`'s own exit status; Class names what that status means.
// Message is the error ctxloom reported, and Findings each fatal finding as it
// was printed (its text and its fix).
type EndReason struct {
	Exit     int      `yaml:"exit" json:"exit"`
	Class    string   `yaml:"class" json:"class"`
	Message  string   `yaml:"message,omitempty" json:"message,omitempty"`
	Findings []string `yaml:"findings,omitempty" json:"findings,omitempty"`
}

// MaxRunEnds bounds Entry.RunEnds: a one-shot session mints a run per turn,
// and the sidecar is rewritten whole on every change.
const MaxRunEnds = 16

// RunEnd is one run's end as the session index records it (Entry.RunEnds).
// ExitCode is the runner's or engine's exit status — its own code, or
// 128+signum when a signal ended it — and is nil when no exit was observed
// (a launch that failed, a runner still running when it was declared lost).
// Failed reports a run that did not finish (coord.CauseIsFailure, or a
// non-zero exit).
type RunEnd struct {
	RunID    string    `yaml:"run_id" json:"run_id"`
	At       time.Time `yaml:"at" json:"at"`
	Cause    string    `yaml:"cause" json:"cause"`
	Detail   string    `yaml:"detail,omitempty" json:"detail,omitempty"`
	ExitCode *int      `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
	Failed   bool      `yaml:"failed,omitempty" json:"failed,omitempty"`
}

// withRunEnd returns ends with end recorded: replacing the same run's earlier
// end in place, else appended, then trimmed to the newest MaxRunEnds. The one
// rule both stores apply.
func withRunEnd(ends []RunEnd, end RunEnd) []RunEnd {
	end.At = end.At.UTC()
	for i := range ends {
		if ends[i].RunID == end.RunID {
			ends[i] = end
			return ends
		}
	}
	ends = append(ends, end)
	if over := len(ends) - MaxRunEnds; over > 0 {
		ends = append(ends[:0:0], ends[over:]...)
	}
	return ends
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
	// session without failing the whole operation (a corrupt sidecar). The
	// caller renders them.
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
// Opening creates nothing: every reader arrives through Open, and a root that
// does not exist yet reads as no sessions. The root is laid out by the first
// mint (AssignHarp). A root that exists but cannot be a directory of sessions
// is refused here, so the caller reports that rather than a later symptom.
//
// sink receives the per-session findings the Manager raises without failing
// an operation; nil discards them.
func Open(sink report.Sink) (*Manager, error) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	if fi, err := os.Stat(root); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("open sessions dir: %w", &os.PathError{Op: "open", Path: root, Err: syscall.ENOTDIR})
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat sessions root: %w", err)
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
	e, err := decodeSidecar(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s for %s: %w", paths.SessionSidecarFileName, harpName, err)
	}
	e.HarpName = harpName
	return e, nil
}

// sidecarKind versions the session sidecar.
var sidecarKind = schemaver.Define("session sidecar", 3, sessionsidecarmig.Steps()...)

// sidecarDoc is the sidecar on disk: an Entry plus the format generation,
// which is a fact about the file rather than about the session, so Entry does
// not carry it.
type sidecarDoc struct {
	SchemaVersion int `yaml:"schema_version"`
	Entry         `yaml:",inline"`
}

// decodeSidecar reads a sidecar's bytes at the current generation. Reading
// never writes: an older sidecar is migrated here in memory and reaches disk
// with the next writeSidecar.
func decodeSidecar(data []byte) (*Entry, error) {
	r, err := sidecarKind.Upgrade(data)
	if err != nil {
		return nil, err
	}
	var d sidecarDoc
	if err := yaml.Unmarshal(r.Data, &d); err != nil {
		return nil, err
	}
	return &d.Entry, nil
}

// writeSidecar atomically replaces harpName's sidecar with e. Durable: the
// sidecar is the only record of the session's rotation lineage, and a rename
// that silently reverts after a crash loses that lineage with no signal.
func (m *Manager) writeSidecar(harpName string, e *Entry) error {
	data, err := yamlx.Marshal(sidecarDoc{SchemaVersion: sidecarKind.Current(), Entry: *e})
	if err != nil {
		return fmt.Errorf("marshal %s: %w", paths.SessionSidecarFileName, err)
	}
	fs := afero.NewOsFs()
	dir := filepath.Join(m.root, harpName)
	if err := fs.MkdirAll(dir, sessionDirMode); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and the session dir
	// normally exists before its first sidecar write (launch lays out its
	// members under it with the default mode).
	if err := fs.Chmod(dir, sessionDirMode); err != nil {
		return fmt.Errorf("restrict session dir: %w", err)
	}
	return safefs.WriteFile(fs, filepath.Join(dir, paths.SessionSidecarFileName), data, sidecarFileMode, safefs.Durable())
}

// lock takes harpName's exclusive sidecar lock (paths.HarpSidecarLockPath)
// and returns its release func, already wrapped for return. Every mutating
// method acquires through here, so the lock's identity and the error's shape
// are decided once rather than re-agreed at each call site. The Manager works
// on the controller's own filesystem (it reaches it through os), so its lock
// is the controller's own too: safefs.New's.
func (m *Manager) lock(harpName string) (func(), error) {
	lockPath, err := paths.HarpSidecarLockPath(harpName)
	if err != nil {
		return nil, err
	}
	l, err := safefs.New().Locks.Lock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() { _ = l.Unlock() }, nil
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

	if err := os.MkdirAll(m.root, lockDirMode); err != nil {
		return Entry{}, fmt.Errorf("mkdir sessions dir: %w", err)
	}
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

// RecordOutputDir records harp's output dir: <base>/<project>/<harp>, the
// project being the plain name of the entry's own ProjectDir
// (paths.OutputDir). Recorded once, at mint, absolute; returns it.
func (m *Manager) RecordOutputDir(harpName, base string) (string, error) {
	var dir string
	err := m.update(harpName, func(e *Entry) (bool, error) {
		d, err := paths.OutputDir(base, e.ProjectDir, harpName)
		if err != nil {
			return false, err
		}
		dir = d
		e.OutputDir = d
		return true, nil
	})
	return dir, err
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
// binding — resume, transcript watch, the exit-time capture — then reads
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
			e.TranscriptPath = boundTranscriptPath(transcriptPath)
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
// RefreshVendorTranscript, depends on it) as oldest-first array order; a
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
// position and the session it records, and the canonical transcript. Every
// read path — one-harp lookups and listings alike — goes through it, so an
// entry whose CanonicalTranscriptPath is empty means no capture exists, not
// that this path skipped the stat.
func enrich(e *Entry) {
	fillBindingByLocation(e)
	fillCanonicalTranscript(e)
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
	// No record holds it: a session bound only by location (a containerized
	// child's) is named by its transcript, so look there — only on a miss, as
	// it walks each harp's native dir.
	for i := range entries {
		e := &entries[i]
		if fillBindingByLocation(e) && e.SessionID == sessionID {
			enrich(e)
			return e, nil
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
// the all-projects listing (`session list --all`) so project-scoped and
// cross-project views sort the same way.
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

// RecordRunEnd records how one of the session's runs ended (Entry.RunEnds).
func (m *Manager) RecordRunEnd(harpName string, end RunEnd) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		e.RunEnds = withRunEnd(e.RunEnds, end)
		return true, nil
	})
}

// RecordEndReason records why the session ended before its engine launched
// (Entry.EndReason).
func (m *Manager) RecordEndReason(harpName string, reason EndReason) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		e.EndReason = &reason
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

// ClearPurged removes the named entry's PurgedAt: a purged session whose
// canonical transcript was rebuilt from the engine log that outlived the purge
// is an ordinary session again. Idempotent: an unmarked entry is not
// rewritten.
func (m *Manager) ClearPurged(harpName string) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		if e.PurgedAt == nil {
			return false, nil
		}
		e.PurgedAt = nil
		return true, nil
	})
}

// SetKeep marks (keep true) or unmarks the named session's transcript as
// kept (Entry.Keep). Idempotent: an unchanged mark is not rewritten.
func (m *Manager) SetKeep(harpName string, keep bool) error {
	return m.update(harpName, func(e *Entry) (bool, error) {
		if e.Keep == keep {
			return false, nil
		}
		e.Keep = keep
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

// ErrOutputDirExists refuses a rename whose session's output folder cannot
// move to the new name because something is already there.
var ErrOutputDirExists = errors.New("the output folder for the new name already exists")

// ErrOutputDirMove refuses a rename whose session's output folder could not
// be moved, or whose record of the move could not be written.
var ErrOutputDirMove = errors.New("the session's output folder could not be moved")

// Rename moves the session directory from oldName to newName — the directory
// IS the record, so its transcript, persisted files and sidecar all travel
// together — and moves the session's output folder beside itself to the new
// name, recording where it went. Errors if oldName is not a session, if
// anything already sits at newName, or if newName is not a usable harp
// identifier or runs past harp.MaxNameLen; and refuses with
// ErrOutputDirExists or ErrOutputDirMove when the output folder cannot move.
// A refusal leaves everything where it was: each step is undone if a later
// one fails.
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

	cur, newDir, err := m.renameSource(oldName, newName)
	if err != nil {
		return err
	}
	oldOut, newOut, moving, err := renamedOutputDir(cur.OutputDir, newName)
	if err != nil {
		return err
	}
	// A move, never copy-then-delete: a rename leaves the folder at exactly
	// one of its two names.
	if moving {
		if err := safefs.Rename(afero.NewOsFs(), oldOut, newOut); err != nil {
			return fmt.Errorf("%w: %s -> %s: %w", ErrOutputDirMove, oldOut, newOut, err)
		}
	}
	unmove := func() {
		if moving {
			_ = safefs.Rename(afero.NewOsFs(), newOut, oldOut)
		}
	}
	unrecord, err := m.recordOutputDir(oldName, cur, newOut)
	if err != nil {
		unmove()
		return err
	}
	if err := safefs.Rename(afero.NewOsFs(), filepath.Join(m.root, oldName), newDir); err != nil {
		unrecord()
		unmove()
		return fmt.Errorf("rename session dir: %w", err)
	}
	return nil
}

// recordOutputDir records newOut as the output folder in oldName's sidecar,
// returning the undo. A session recording no folder (newOut "") records
// nothing.
func (m *Manager) recordOutputDir(oldName string, cur *Entry, newOut string) (undo func(), err error) {
	if newOut == "" {
		return func() {}, nil
	}
	recorded := *cur
	recorded.OutputDir = newOut
	if err := m.writeSidecar(oldName, &recorded); err != nil {
		return nil, fmt.Errorf("%w: record %s: %w", ErrOutputDirMove, newOut, err)
	}
	return func() { _ = m.writeSidecar(oldName, cur) }, nil
}

// renameSource is oldName's record and the session dir newName would take,
// refusing a rename of a harp that is not a session, or onto a name a session
// dir already holds. The caller holds oldName's lock.
func (m *Manager) renameSource(oldName, newName string) (cur *Entry, newDir string, err error) {
	cur, err = m.readSidecar(oldName)
	if err != nil {
		return nil, "", err
	}
	if cur == nil {
		return nil, "", fmt.Errorf("session not found: %q", oldName)
	}
	newDir = filepath.Join(m.root, newName)
	if _, err := os.Lstat(newDir); err == nil {
		return nil, "", fmt.Errorf("name already in use: %q", newName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("rename %s: inspect %s: %w", oldName, newName, err)
	}
	return cur, newDir, nil
}

// renamedOutputDir is where a session's recorded output folder goes under
// newName — beside itself — and whether there is a folder to move. A session
// recording none has nothing to move (oldOut ""); a recorded folder never
// written moves nothing but is still recorded under the new name. Something
// already at the target is ErrOutputDirExists.
func renamedOutputDir(recorded, newName string) (oldOut, newOut string, moving bool, err error) {
	if recorded == "" {
		return "", "", false, nil
	}
	newOut = filepath.Join(filepath.Dir(recorded), newName)
	if _, err := os.Lstat(newOut); err == nil {
		return "", "", false, fmt.Errorf("%w: %s", ErrOutputDirExists, newOut)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", false, fmt.Errorf("%w: inspect %s: %w", ErrOutputDirMove, newOut, err)
	}
	if _, err := os.Lstat(recorded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recorded, newOut, false, nil
		}
		return "", "", false, fmt.Errorf("%w: inspect %s: %w", ErrOutputDirMove, recorded, err)
	}
	return recorded, newOut, true, nil
}

// Forget removes the harp's sidecar, and with it the session from every
// listing. The directory and everything else in it — transcript, authored
// files — are left untouched: only the record goes away.
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
			return fmt.Errorf("session not found: %q", harpName)
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
