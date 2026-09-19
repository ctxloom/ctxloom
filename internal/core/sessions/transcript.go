package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// linkEngineTranscript creates
// ~/.ctxloom/sessions/<harp>/engine-transcript-<engine>-<sessionID>.jsonl
// (paths.HarpEngineTranscriptLinkPath) as a symlink to the backend's vendor
// transcript at transcriptPath. Best-effort throughout: any failure (home
// unresolved, no symlink privilege on Windows, etc.) is warned and swallowed
// so it never blocks a session bind — the same posture its predecessor,
// linkTranscriptIntoHarpDir, held.
//
// REPLACES the single mutable `<harp>/transcript.jsonl` symlink that name
// used to be created under: that name is RETIRED — nothing creates or
// repoints it anymore. A harp accumulates SEVERAL vendor transcripts over its
// life (one per /clear rotation, one per engine a harp is ever rebound to),
// and the old symlink could only ever name the single most recent one,
// silently orphaning every earlier vendor log's only harp-dir reference the
// moment a later bind repointed it. It also name-collided with the canonical
// transcript's OWN leaf (paths.CanonicalTranscriptFileName, a DIFFERENT file
// in a DIFFERENT format at persist/transcript.jsonl) — a human browsing the
// harp dir root could not tell, from the name alone, which format they were
// looking at.
//
// Existing pre-rename `transcript.jsonl` symlinks are LEFT ALONE: this is
// read-only legacy, not migrated or cleaned up (standing no-backward-compat-
// shims policy — a fresh bind is the upgrade path, same as every other
// harp-index schema change in this package).
//
// Each link, once created, is IMMUTABLE: a repeat call naming the SAME
// engine+sessionID+transcriptPath is a no-op (the ordinary shape of an
// idempotent repeat hook firing). A repeat call naming the same
// engine+sessionID but a DIFFERENT transcriptPath — a session-id reuse
// anomaly, never expected in production but not impossible to construct — is
// the one case this DOES repoint, and it does so atomically (see
// atomicSymlink) with a diagnostic naming the collision, since two different
// files under one name is a correctness hazard for whoever reads it next.
// Every other rotation gets its OWN new link (a new sessionID means a new
// path), so the harp dir's own listing becomes the vendor-log lineage.
func linkEngineTranscript(harpName, engine, sessionID, transcriptPath string) {
	if engine == "" || sessionID == "" {
		clidiag.Warn("ctxloom", "engine transcript link for harp %q: missing engine (%q) or session id (%q); nothing linked", harpName, engine, sessionID)
		return
	}
	link, err := paths.HarpEngineTranscriptLinkPath(harpName, engine, sessionID)
	if err != nil {
		clidiag.Warn("ctxloom", "engine transcript link: %v", err)
		return
	}
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "engine transcript link: %v", err)
		return
	}
	// A transcript that already lives INSIDE the session dir needs no
	// reference link: a containerized run bind-mounts the engine's store root
	// at persist/transcripts, so the physical file is harp-addressable by
	// location (LocateTranscript) and this link would only add a second name
	// for it inside the same dir.
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
		clidiag.Warn("ctxloom", "engine transcript link: bound transcript %s does not resolve (%v); linking it anyway, but reads through the session dir will fail until it appears", transcriptPath, serr)
	}

	existing, rlErr := os.Readlink(link)
	switch {
	case rlErr == nil && existing == transcriptPath:
		// Create-once: already correct, nothing to do. The ordinary shape of
		// a repeat hook firing for the same live binding.
		return
	case rlErr == nil:
		// Same name, a DIFFERENT target already there: a session id got
		// reused for a different transcript file. Replace atomically (the
		// name is never observably absent — see atomicSymlink) and say so
		// loudly; this is not the routine first-sighting path.
		if err := atomicSymlink(transcriptPath, link); err != nil {
			clidiag.Warn("ctxloom", "engine transcript link: %v", err)
			return
		}
		clidiag.Warn("ctxloom", "engine transcript link %s previously pointed at %s, now repointed to %s: session id %q was reused for a different %s transcript", link, existing, transcriptPath, sessionID, engine)
		return
	case !errors.Is(rlErr, os.ErrNotExist):
		// Something occupies the name and it is not even a symlink (or is
		// unreadable for some other reason). Name the real cause here rather
		// than letting the Symlink call below fail with an opaque EEXIST,
		// which describes the symptom and hides the cause.
		clidiag.Warn("ctxloom", "engine transcript link: could not inspect existing %s (%v)", link, rlErr)
		return
	}
	// Absent: the ordinary first-sighting case for this engine+sessionID.
	if err := os.Symlink(transcriptPath, link); err != nil {
		clidiag.Warn("ctxloom", "engine transcript link: %v", err)
	}
}

// atomicSymlink replaces link with a symlink to target such that link is
// never observably absent at any instant a concurrent reader can look: a
// fresh, unique name is reserved in link's directory, symlinked to target,
// then renamed over link. rename(2) atomically replaces its destination on
// every platform this project ships for, so link names either the OLD target
// or the NEW one at every point a reader can observe it — never neither. This
// is the "unique temp name + rename" idiom iox.WriteFileAtomic/
// WriteFileAtomicFs use for regular files, applied to a symlink, which those
// primitives do not cover (they write byte content; a symlink has none to
// write — os.Symlink IS the write).
//
// The atomicity is BY CONSTRUCTION (rename's platform guarantee), not
// independently reproven by a test that observes the window from a second
// goroutine — such a test would be racy by nature. What the test suite pins
// instead is the END STATE (the link resolves to the new target after a
// call) and that a Remove-then-Symlink mutant — which DOES have an
// observable absent window — is distinguishable: forcing the temp symlink
// step to fail leaves the ORIGINAL link fully intact only when nothing before
// the rename ever touched it, which is exactly this function's structure and
// exactly what the non-atomic mutant violates.
func atomicSymlink(target, link string) error {
	dir := filepath.Dir(link)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(link)+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomic symlink %s: reserve temp name: %w", link, err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	if err := os.Remove(tmpName); err != nil {
		return fmt.Errorf("atomic symlink %s: clear reserved temp name: %w", link, err)
	}
	if err := os.Symlink(target, tmpName); err != nil {
		return fmt.Errorf("atomic symlink %s: create temp symlink: %w", link, err)
	}
	if err := os.Rename(tmpName, link); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic symlink %s: rename into place: %w", link, err)
	}
	return nil
}

// LocateTranscript finds a harp's transcript BY LOCATION: the newest
// transcript-shaped file under ~/.ctxloom/sessions/<harp>/persist/transcripts,
// the dir a containerized run bind-mounts to the engine's native transcript
// store root. A containerized structured child never runs the SessionStart
// hook, so the normal BindSession path (which records TranscriptPath) never
// fires — but with the mount, the transcript physically lives in the harp's
// session dir, so location IS the binding. Engines nest their stores
// (claude: <encoded-project>/<uuid>.jsonl; antigravity, before it was
// removed in 0.7.0: <uuid>/.system_generated/logs/transcript_full.jsonl),
// hence the recursive walk. The newest .jsonl wins (claude/codex
// transcripts, and any older antigravity ones a pre-upgrade harp still
// carries); .json is the kiro-store fallback considered only when no .jsonl
// exists.
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

// ActivityTime returns e's last-worked time for `session list` ordering: the
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

// TranscriptStale compares a transcript's current ENTRY COUNT to the count
// stamped when an essence was distilled from it (Entry.SourceEntries). It
// reports whether the essence is out of date and whether that could be
// determined at all: known=false (stale=false) when there is no stamped count
// (never distilled), no transcript path, or the file cannot be read.
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

// SourceStale reports whether this entry's distilled essence is out of date
// relative to its source transcript, and whether that could be determined (see
// TranscriptStale). `session list` uses it to badge stale rows.
//
// Prefers CanonicalTranscriptPath over TranscriptPath (S4): once a
// harp has a captured canonical transcript, that IS the file the compactor
// actually distills from (memory stamps its entry count the same way), so
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
