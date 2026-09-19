package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/afero"
)

// SessionStore is the shared scaffold embedded by per-agent session-history
// readers: the afero filesystem their tests inject through, plus the common
// JSONL-transcript parse loop. Path conventions, per-line entry conversion,
// and session-ID recovery stay per-agent.
//
// The claude/codex/antigravity SessionHistory readers that
// used to embed this were deleted outright (proven broken, see each
// package's backend.go doc). opencode's native reader still embeds it.
type SessionStore struct {
	// FS is the filesystem transcripts are read through (test injection
	// point). Nil falls back to the OS filesystem.
	FS afero.Fs
}

// ErrNoSessions reports honest absence: this project has no recorded session
// history for the requested workDir. It is a distinct fact from "the history
// could not be read", which arrives as a wrapped list/load error, and callers
// that treat an empty history as a normal state must discriminate with
// errors.Is rather than matching on message text.
var ErrNoSessions = errors.New("no sessions found")

// GetCurrentSessionViaGetSession is the common GetCurrentSession shape shared
// by every per-agent SessionHistory whose per-session loader takes (workDir,
// id string) — opencode's GetSession does (the claude/codex/antigravity
// readers that used to share this shape were deleted): call
// list(workDir), sort most-recent-first by StartTime, then load the newest
// session via getSession(workDir, id).
//
// This used to be three functions —
// SortSessionsMostRecentFirst (a helper with ZERO callers: the ordering
// invariant it exists to enforce was assumed, in a doc comment, rather than
// actually applied), MostRecentSession (which trusted that assumed ordering
// and took sessions[0] unsorted), and GetCurrentSessionViaListSessions (a
// pure pass-through with no caller of its own — GetCurrentSessionViaGetSession
// was its only user). Collapsed into one function that actually sorts, so the
// "most recent first" precondition is enforced here instead of merely
// documented; previously the guarantee depended entirely on each engine's
// ListSessions independently getting the order right (opencode's does, via
// its own sort.SliceStable, but nothing enforced that generically).
func GetCurrentSessionViaGetSession(workDir string, list func(string) ([]SessionMeta, error), getSession func(workDir, id string) (*Session, error)) (*Session, error) {
	sessions, err := list(workDir)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, ErrNoSessions
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})
	return getSession(workDir, sessions[0].ID)
}

// ParseSessionFile reads a JSONL transcript at path into the normalized
// Session contract. parseLine converts one non-empty line into zero or more
// entries; malformed/unrecognized lines should yield nil so a session
// degrades to a partial transcript rather than an error.
//
// The loop uses an unbounded bufio.Reader instead of a capped bufio.Scanner:
// agents embed whole file contents in single JSONL lines (e.g. Claude's
// large tool results), and a Scanner cap
// would hard-fail the entire session on the first oversized line, breaking
// the degrade-to-partial contract.
func (s *SessionStore) ParseSessionFile(path, sessionID string, parseLine func(line []byte) []SessionEntry) (*Session, error) {
	file, err := GetFS(s.FS).Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = file.Close() }()

	session := &Session{
		ID:      sessionID,
		Entries: []SessionEntry{},
	}

	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				session.Entries = append(session.Entries, parseLine(line)...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to scan session file: %w", err)
		}
	}

	if len(session.Entries) > 0 {
		session.StartTime = session.Entries[0].Timestamp
		session.EndTime = session.Entries[len(session.Entries)-1].Timestamp
	}
	return session, nil
}
