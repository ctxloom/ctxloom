package transcript

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// shortWriter accepts the first cut bytes of the write it is armed for, then
// reports a short write — the shape a filesystem produces when it runs out of
// space part-way through a line. Everything before and after passes through.
type shortWriter struct {
	w     io.WriteCloser
	armed bool
	cut   int
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if !s.armed {
		return s.w.Write(p)
	}
	s.armed = false
	n, err := s.w.Write(p[:s.cut])
	if err != nil {
		return n, err
	}
	return n, io.ErrShortWrite
}

func (s *shortWriter) Close() error { return s.w.Close() }

// entryEvent is one ordinary user entry, distinct per label.
func entryEvent(label string) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: label}}
}

// TestRecorder_ShortWriteBoundsTheDamageAndShowsTheGap pins both halves of
// a partial-write defect.
//
// A partial write left an unterminated JSON fragment on disk AND left seq
// unadvanced. The fragment had no trailing newline, so the NEXT record's bytes
// were appended to it and the two collapsed into one unparseable line — the
// reader's degrade-to-partial rule then dropped both, losing a record that had
// been written perfectly. And because seq had not moved, the replacement
// carried the same seq as the fragment, so the file showed no discontinuity
// and Record.Seq's stated promise ("a reader can detect truncation/corruption
// by a seq discontinuity") was unmeetable exactly when it mattered.
func TestRecorder_ShortWriteBoundsTheDamageAndShowsTheGap(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "shortwrite-harp"

	sw := &shortWriter{}
	rec, err := NewRecorder(harp, "codex", func(r *fileRecorder) {
		r.open = func(path string) (io.WriteCloser, error) {
			f, oerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if oerr != nil {
				return nil, oerr
			}
			sw.w = f
			return sw, nil
		}
	})
	require.NoError(t, err)

	require.NoError(t, rec.Record(entryEvent("first")))

	// Arm the fault for the NEXT write only.
	sw.armed = true
	sw.cut = 20
	shortErr := rec.Record(entryEvent("truncated"))
	require.Error(t, shortErr, "fixture is not hostile: the short write reported success")
	require.True(t, errors.Is(shortErr, io.ErrShortWrite) || strings.Contains(shortErr.Error(), "write"),
		"the short write must surface as a write failure: %v", shortErr)

	require.NoError(t, rec.Record(entryEvent("third")))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 3, "the fragment must occupy a line of its own, not swallow the next record")

	// The good records either side of the fault both decode.
	var first, third Record
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &third), "the record AFTER the fault must be intact")
	require.NotNil(t, first.Entry)
	require.NotNil(t, third.Entry)
	assert.Equal(t, "first", first.Entry.Content)
	assert.Equal(t, "third", third.Entry.Content)

	// The middle line is the truncated fragment and must not parse.
	assert.Error(t, json.Unmarshal([]byte(lines[1]), &Record{}),
		"the truncated fragment must remain unparseable, not masquerade as a record")

	// And the loss is visible as a seq discontinuity, which is what
	// Record.Seq's doc promises a reader can rely on.
	assert.Equal(t, 0, first.Seq)
	assert.Equal(t, 2, third.Seq,
		"seq must advance past the lost record so the gap is detectable; reusing it hides the loss")
}

// TestRecorder_TotallyFailedWriteLeavesNoGap is the other edge: when NOTHING
// was written there is nothing to detect, and seq must NOT advance — a gap
// with no loss behind it would make every reader's truncation check cry wolf.
func TestRecorder_TotallyFailedWriteLeavesNoGap(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "zerowrite-harp"

	fail := &failingWriter{}
	rec, err := NewRecorder(harp, "codex", func(r *fileRecorder) {
		r.open = func(path string) (io.WriteCloser, error) {
			f, oerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if oerr != nil {
				return nil, oerr
			}
			fail.w = f
			return fail, nil
		}
	})
	require.NoError(t, err)

	require.NoError(t, rec.Record(entryEvent("first")))
	fail.armed = true
	require.Error(t, rec.Record(entryEvent("lost")))
	require.NoError(t, rec.Record(entryEvent("second")))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 2)

	var a, b Record
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &a))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &b))
	assert.Equal(t, 0, a.Seq)
	assert.Equal(t, 1, b.Seq, "a write that delivered zero bytes leaves no fragment and so must leave no gap")
}

// failingWriter refuses the write it is armed for outright, delivering zero
// bytes.
type failingWriter struct {
	w     io.WriteCloser
	armed bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.armed {
		f.armed = false
		return 0, errors.New("no space left on device")
	}
	return f.w.Write(p)
}

func (f *failingWriter) Close() error { return f.w.Close() }
