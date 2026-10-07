package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// boundTranscriptPath is the path a binding records for transcriptPath: the
// file it names with every symlink resolved. An engine reports its
// transcript under its config home, and the session home's history dir is a
// link into native/ (engine.HomeSpec.TranscriptStoreRel); recording the
// resolved path is what keeps the binding true once the disposable home is
// deleted on Close. A path that does not resolve yet — the engine has not
// flushed the file — is recorded as given: a dangling binding is still the
// only name there is, and LocateTranscript finds the file by location later.
func boundTranscriptPath(transcriptPath string) string {
	if resolved, err := filepath.EvalSymlinks(transcriptPath); err == nil {
		return resolved
	}
	return transcriptPath
}

// LocateTranscript finds a harp's transcript BY LOCATION: the newest
// transcript-shaped file under ~/.ctxloom/sessions/<harp>/native, where every
// engine's native history lands through its session home's link, on the host
// and in a container alike. A containerized structured child never runs the
// SessionStart hook, so the normal BindSession path (which records
// TranscriptPath) never fires — but the transcript physically lives in the
// harp's session dir, so location IS the binding. Engines nest their stores
// (claude: <encoded-project>/<uuid>.jsonl), hence the recursive walk. The
// newest .jsonl wins; .json is considered only when no .jsonl exists.
func LocateTranscript(harpName string) (string, bool) {
	if harpName == "" {
		return "", false
	}
	root, err := paths.HarpNativeDir(harpName)
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

// TranscriptSessionFunc names the native session the transcript at path
// records, for the engine an entry's Backend names. It is the engine's
// own knowledge (engine.Engine.TranscriptSession), handed in by the
// composition root because this package sits below the engine port.
type TranscriptSessionFunc func(backend, transcript string) (string, error)

var (
	transcriptSessionsMu sync.RWMutex
	transcriptSessions   TranscriptSessionFunc
)

// UseTranscriptSessions installs f as how a transcript found BY LOCATION
// names its session, until restore is called. The composition root installs
// one over the engine registry; with none installed, a located transcript
// names no session.
func UseTranscriptSessions(f TranscriptSessionFunc) (restore func()) {
	transcriptSessionsMu.Lock()
	defer transcriptSessionsMu.Unlock()
	prev := transcriptSessions
	transcriptSessions = f
	return func() {
		transcriptSessionsMu.Lock()
		defer transcriptSessionsMu.Unlock()
		transcriptSessions = prev
	}
}

// fillBindingByLocation resolves a missing (or dangling) transcript binding
// BY LOCATION for a returned entry COPY, and names the session from the
// transcript it located. Computed on read and never persisted, so the on-disk
// record keeps only what a bind actually recorded. A live bound transcript —
// a SessionStart hook in the controller's own view recorded it — short-circuits
// on the stat and its session id is untouched.
//
// A located transcript always names the session. A containerized child
// cannot write the controller's record, so the only key its record can hold
// is one the coordinator learned over the wire, which never displaces a
// binding (Manager.BindSession) and so stays on the first transcript after a
// /clear; the newest transcript in the harp's native dir is where the
// session actually is. Reported ok=true when the transcript was located.
func fillBindingByLocation(e *Entry) bool {
	if e.TranscriptPath != "" {
		if _, err := os.Stat(e.TranscriptPath); err == nil {
			return false
		}
	}
	p, ok := LocateTranscript(e.HarpName)
	if !ok {
		return false
	}
	e.TranscriptPath = p
	if id := locatedSession(e.Backend, p); id != "" {
		e.SessionID = id
	}
	return true
}

// locatedSession is the session the installed TranscriptSessionFunc names
// for transcript, or "" when none is installed or the engine cannot name it
// (it keeps no store of its own, or the file is not one of its transcripts):
// the record's key then stands, never blanked.
func locatedSession(backend, transcript string) string {
	transcriptSessionsMu.RLock()
	f := transcriptSessions
	transcriptSessionsMu.RUnlock()
	if f == nil {
		return ""
	}
	id, err := f(backend, transcript)
	if err != nil {
		return ""
	}
	return id
}

// fillCanonicalTranscript stats a harp's canonical transcript
// (paths.HarpCanonicalTranscriptPath, the one name it is ever written under)
// and records its path on the entry COPY when present — computed on read like
// fillBindingByLocation, never persisted. This is
// how a session becomes discoverable by ctxloom's own captured transcript
// independent of whatever the legacy engine-file
// TranscriptPath does or doesn't resolve to.
func fillCanonicalTranscript(e *Entry) {
	if e == nil || e.HarpName == "" {
		return
	}
	p, err := paths.HarpCanonicalTranscriptPath(e.HarpName)
	if err != nil {
		return
	}
	if _, err := os.Stat(p); err == nil {
		e.CanonicalTranscriptPath = p
	}
}

// TranscriptStale compares a transcript's current ENTRY COUNT to the count
// stamped when an essence was compacted from it (Entry.SourceEntries). It
// reports whether the essence is out of date and whether that could be
// determined at all: known=false (stale=false) when there is no stamped count
// (never compacted), no transcript path, or the file cannot be read.
//
// See Entry.SourceEntries for WHY this counts entries rather than bytes.
// Read-only and best-effort per the fault-tolerance philosophy — an unreadable
// transcript degrades to "can't tell", never an error.
func TranscriptStale(transcriptPath string, stampedEntries int) (stale, known bool) {
	if stampedEntries == 0 || transcriptPath == "" {
		return false, false
	}
	live, ok := CountTranscriptEntries(transcriptPath)
	if !ok {
		return false, false
	}
	return live != stampedEntries, true
}

// CountTranscriptEntries counts the conversational ENTRY records in a
// canonical transcript (one JSON record per line, kind "entry"). It reports
// ok=false when the file cannot be read at all.
//
// It decodes only each line's kind — never the whole record — because this
// runs on the cache-HIT path, where the entire point is to avoid work. A
// malformed line is skipped rather than fatal, matching how every transcript
// reader in this project treats one.
//
// It lives here, not in internal/adapters/transcript, because internal/adapters/transcript
// imports this package: the counter must be self-contained or the two form a
// cycle. It needs nothing but stdlib, so that costs nothing.
func CountTranscriptEntries(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	var kindOnly struct {
		Kind string `json:"kind"`
	}
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		kindOnly.Kind = ""
		if err := json.Unmarshal(line, &kindOnly); err != nil {
			continue // malformed line: skip, never fatal
		}
		if kindOnly.Kind == transcriptEntryKind {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, false
	}
	return count, true
}

// transcriptEntryKind is the canonical transcript's conversational record
// kind. Named rather than inlined so the string appears once (see
// internal/adapters/transcript.KindEntry, which this must agree with; this package
// cannot import that one without a cycle).
const transcriptEntryKind = "entry"

// SourceStale reports whether this entry's compacted essence is out of date
// relative to its source transcript, and whether that could be determined (see
// TranscriptStale). `session list` uses it to badge stale rows.
//
// Prefers CanonicalTranscriptPath over TranscriptPath (S4): once a
// harp has a captured canonical transcript, that IS the file the compactor
// actually compacts from (memory stamps its entry count the same way), so
// staleness must compare against it — comparing the essence's stamped count
// to the legacy engine file's would compare two different sources and the
// badge would lie.
func (e Entry) SourceStale() (stale, known bool) {
	path := e.TranscriptPath
	if e.CanonicalTranscriptPath != "" {
		path = e.CanonicalTranscriptPath
	}
	return TranscriptStale(path, e.SourceEntries)
}
