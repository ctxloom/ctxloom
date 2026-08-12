package spool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedIn publishes one in/ message and returns its ref plus the exact bytes
// on disk.
func seedIn(t *testing.T, m PathMapper, body string) (Ref, []byte) {
	t.Helper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: body})
	require.NoError(t, err)
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, raw, "empty-source guard: seeded message must have bytes")
	return ref, raw
}

// TestConsume_MovesToConsumedAndKeepsTheBytes is the consumed-audit pin.
// Consumption is a RENAME: the file must be gone from in/ AND present,
// byte-identical, in in/consumed/. A delete would satisfy "gone from in/" and
// destroy the audit trail, the dedupe seed, and the delivery confirmation.
func TestConsume_MovesToConsumedAndKeepsTheBytes(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, before := seedIn(t, m, "payload for the audit trail\n")

	moved, err := Consume(m, ref)
	require.NoError(t, err)
	require.Equal(t, DirInConsumed, moved.Dir)
	require.Equal(t, ref.Name, moved.Name, "consumption must not rename the file's identity")

	livePath, err := m.Resolve(ref)
	require.NoError(t, err)
	_, err = os.Stat(livePath)
	require.True(t, os.IsNotExist(err), "the message must be gone from in/, got %v", err)

	consumedPath, err := m.Resolve(moved)
	require.NoError(t, err)
	after, err := os.ReadFile(consumedPath)
	require.NoError(t, err, "consume must MOVE the file into consumed/, not delete it")
	require.NotEmpty(t, after, "empty-source guard: the consumed copy must have bytes")
	require.Equal(t, before, after, "the consumed copy must be byte-identical to what was delivered")

	msg, err := Read(m, moved)
	require.NoError(t, err)
	require.Equal(t, "payload for the audit trail\n", msg.Body)

	// And in/ now sweeps empty, which is the whole cursor model.
	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err)
	require.Empty(t, res.Entries)

	consumedRes, err := Sweep(m, testHarp, DirInConsumed)
	require.NoError(t, err)
	require.Len(t, consumedRes.Entries, 1, "consumed/ is the audit trail and must list the message")
}

// TestConsume_SecondTakeIsAlreadyGone: exactly one consumer wins; the loser
// must get the typed sentinel so it can retry or sweep rather than alarm.
func TestConsume_SecondTakeIsAlreadyGone(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "once\n")

	_, err := Consume(m, ref)
	require.NoError(t, err)

	_, err = Consume(m, ref)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAlreadyGone, "a lost consume race must be ErrAlreadyGone, not a generic failure")
}

// TestWithdraw_RacesConsumeThroughTheFilesystem: rename-won means retracted,
// ErrAlreadyGone means the reader already took it ("pulled").
func TestWithdraw_RacesConsumeThroughTheFilesystem(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()

	t.Run("writer wins", func(t *testing.T) {
		ref, before := seedIn(t, m, "retract me\n")
		moved, err := Withdraw(m, ref)
		require.NoError(t, err)
		require.Equal(t, DirInWithdrawn, moved.Dir)

		path, err := m.Resolve(moved)
		require.NoError(t, err)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, before, after, "a withdrawn message must be preserved, not deleted")

		_, err = Consume(m, ref)
		require.ErrorIs(t, err, ErrAlreadyGone, "the reader must learn the message was retracted")
	})

	t.Run("reader wins", func(t *testing.T) {
		ref, _ := seedIn(t, m, "too late\n")
		_, err := Consume(m, ref)
		require.NoError(t, err)

		_, err = Withdraw(m, ref)
		require.ErrorIs(t, err, ErrAlreadyGone, "a withdrawal that lost the race must report pulled, not fail loudly")
	})
}

func TestWithdraw_RefusesUnwithdrawableDirections(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := Withdraw(m, Ref{Harp: testHarp, Dir: DirOut, Name: "00000000000000000001.00000001.agent.md"})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAlreadyGone, "a direction that has no withdrawn state is a caller bug, not a race")
}

// TestRead_MissingFileIsTyped: a doorbell naming a file that is not there
// (the sweep won, or a mount's attribute cache is stale) must be
// distinguishable from a real read failure so the receiver retries instead of
// erroring.
func TestRead_MissingFileIsTyped(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	require.NoError(t, EnsureDirs(m, testHarp))

	_, err := Read(m, Ref{Harp: testHarp, Dir: DirIn, Name: "00000000000000000001.00000001.coord.md"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAlreadyGone)
}

func TestRead_RefusesInvalidRef(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := Read(m, Ref{Harp: testHarp, Dir: DirIn, Name: ".."})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAlreadyGone, "a hostile ref must fail as invalid, never as a benign race")
}

// TestSweep_ReportsMalformedFilesLoudly is the silent-no-op pin. A reader
// that skips what it cannot parse reports success while the message never
// arrives — every cheap signal green. Every unreadable entry must come back
// NAMED, and the readable ones must still be delivered.
func TestSweep_ReportsMalformedFilesLoudly(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	good, _ := seedIn(t, m, "readable\n")

	inDir, err := DirPath(m, testHarp, DirIn)
	require.NoError(t, err)
	junk := map[string]string{
		"00000000000000000002.00000002.coord.md": "no frontmatter here\n",
		"00000000000000000003.00000003.coord.md": "---\nkind: [unclosed\n---\n",
		"00000000000000000004.00000004.coord.md": "---\ncreated: 2026-08-12T10:11:12Z\n---\nno kind\n",
		"not-a-message-name.md":                  "---\nkind: message\ncreated: 2026-08-12T10:11:12Z\n---\nx\n",
		"README.txt":                             "notes\n",
	}
	for name, body := range junk {
		require.NoError(t, os.WriteFile(filepath.Join(inDir, name), []byte(body), 0o600))
	}

	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err, "a malformed file must not abort the whole drain")

	require.Len(t, res.Entries, 1, "the readable message must still be delivered")
	require.Equal(t, good.Name, res.Entries[0].Ref.Name)
	require.Equal(t, "readable\n", res.Entries[0].Message.Body)

	require.Len(t, res.Problems, len(junk), "every unreadable entry must be reported, not skipped")
	reported := map[string]bool{}
	for _, p := range res.Problems {
		require.Error(t, p.Err)
		reported[filepath.Base(p.Path)] = true
	}
	for name := range junk {
		require.True(t, reported[name], "sweep silently dropped %q", name)
	}

	joined := res.ProblemErr()
	require.Error(t, joined, "ProblemErr must make a silent drop impossible to ignore")
	for name := range junk {
		require.Contains(t, joined.Error(), name)
	}
}

func TestSweep_OrdersByFilenameAndSkipsSubdirs(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)

	var refs []Ref
	for i := range 5 {
		ref, err := w.Write(&Message{Kind: "message", Body: string(rune('a'+i)) + "\n"})
		require.NoError(t, err)
		refs = append(refs, ref)
	}

	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr(), "consumed/ and withdrawn/ are structure, not junk")
	require.Len(t, res.Entries, len(refs))
	for i, entry := range res.Entries {
		require.Equal(t, refs[i].Name, entry.Ref.Name, "entry %d out of order", i)
		require.Equal(t, string(rune('a'+i))+"\n", entry.Message.Body)
	}
}

func TestSweep_MissingDirectoryIsAnError(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := Sweep(m, testHarp, DirIn)
	require.Error(t, err, "sweeping a spool that was never created must say so, not report an empty drain")
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestSweep_RefusesInvalidHarp(t *testing.T) {
	hostHome(t)
	_, err := Sweep(NewHomeMapper(), "../escape", DirIn)
	require.Error(t, err)
}
