package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/spf13/afero"
)

// SessionStore is the shared JSONL-transcript parse loop, plus the afero
// filesystem it reads through (the test injection point). Path conventions
// and per-line entry conversion stay with the caller.
type SessionStore struct {
	// FS is the filesystem transcripts are read through (test injection
	// point). Nil falls back to the OS filesystem.
	FS afero.Fs
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
