package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/afero"
)

// SessionStore is the shared scaffold embedded by the per-agent session
// history readers (claude, codex, antigravity): the afero filesystem and
// home-directory injection points their tests use, plus the common
// JSONL-transcript parse loop. Path conventions, per-line entry conversion,
// and session-ID recovery stay per-agent.
type SessionStore struct {
	// FS is the filesystem transcripts are read through (test injection
	// point). Nil falls back to the OS filesystem.
	FS afero.Fs
	// HomeDir overrides the user home directory for testing. Empty falls
	// back to os.UserHomeDir.
	HomeDir string
}

// NewSessionStore returns a SessionStore reading through the OS filesystem.
func NewSessionStore() SessionStore {
	return SessionStore{FS: afero.NewOsFs()}
}

// ResolveHomeDir returns the HomeDir override when set, else os.UserHomeDir.
func (s *SessionStore) ResolveHomeDir() (string, error) {
	if s.HomeDir != "" {
		return s.HomeDir, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return homeDir, nil
}

// SortSessionsMostRecentFirst sorts session metadata by start time, most
// recent first — the order every agent's ListSessions returns.
func SortSessionsMostRecentFirst(sessions []SessionMeta) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})
}

// MostRecentSession is the GetCurrentSession skeleton: given a ListSessions
// result (already sorted most recent first), it loads the newest session via
// get, surfacing the listing error or "no sessions found" first.
func MostRecentSession(sessions []SessionMeta, err error, get func(SessionMeta) (*Session, error)) (*Session, error) {
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}
	return get(sessions[0])
}

// ParseSessionFile reads a JSONL transcript at path into the normalized
// Session contract. parseLine converts one non-empty line into zero or more
// entries; malformed/unrecognized lines should yield nil so a session
// degrades to a partial transcript rather than an error.
//
// The loop uses an unbounded bufio.Reader instead of a capped bufio.Scanner:
// agents embed whole file contents in single JSONL lines (e.g. agy's
// write_to_file CodeContent, Claude's large tool results), and a Scanner cap
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
