package spool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// seedIn publishes one in/ message and returns its ref plus the exact bytes
// on disk.
func seedIn(t *testing.T, m PathMapper, body string) (Ref, []byte) {
	t.Helper()
	w, err := NewWriter(afero.NewOsFs(), m, testHarp, DirIn, "coord")
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

// seedOut publishes one out/ message, as a runner's agent_send does.
func seedOut(t *testing.T, m PathMapper, body string) (Ref, []byte) {
	t.Helper()
	w, err := NewWriter(afero.NewOsFs(), m, testHarp, DirOut, "runner")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: testHarp, To: "parent", Body: body})
	require.NoError(t, err)
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, raw, "empty-source guard: seeded message must have bytes")
	return ref, raw
}

// TestConsume_MovesToConsumedAndKeepsTheBytes is the routed-audit pin.
// Consuming an out/ message is a RENAME: the file must be gone from out/ AND
// present, byte-identical, in out/consumed/, which is how a routed message is
// told from a refused one.
func TestConsume_MovesToConsumedAndKeepsTheBytes(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, before := seedOut(t, m, "payload for the audit trail\n")

	moved, err := Consume(m, ref, time.Now())
	require.NoError(t, err)
	require.Equal(t, DirOutConsumed, moved.Dir)
	require.Equal(t, ref.Name, moved.Name, "consumption must not rename the file's identity")

	livePath, err := m.Resolve(ref)
	require.NoError(t, err)
	_, err = os.Stat(livePath)
	require.True(t, os.IsNotExist(err), "the message must be gone from out/, got %v", err)

	consumedPath, err := m.Resolve(moved)
	require.NoError(t, err)
	after, err := os.ReadFile(consumedPath)
	require.NoError(t, err, "consume must MOVE the file into consumed/, not delete it")
	require.NotEmpty(t, after, "empty-source guard: the consumed copy must have bytes")
	require.Equal(t, before, after, "the consumed copy must be byte-identical to what was routed")

	msg, err := Read(m, moved)
	require.NoError(t, err)
	require.Equal(t, "payload for the audit trail\n", msg.Body)

	res, err := Sweep(m, testHarp, DirOut)
	require.NoError(t, err)
	require.Empty(t, res.Entries)

	consumedRes, err := Sweep(m, testHarp, DirOutConsumed)
	require.NoError(t, err)
	require.Len(t, consumedRes.Entries, 1, "out/consumed/ must list the routed message")
}

// TestConsume_AnInboxMessageIsNotConsumable: an in/ message is delivered with
// Deliver, never moved into a consumed/ directory.
func TestConsume_AnInboxMessageIsNotConsumable(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "inbox\n")
	_, err := Consume(m, ref, time.Now())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAlreadyGone)
}

// TestConsume_SecondTakeIsAlreadyGone: exactly one consumer wins; the loser
// must get the typed sentinel so it can retry or sweep rather than alarm.
func TestConsume_SecondTakeIsAlreadyGone(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedOut(t, m, "once\n")

	_, err := Consume(m, ref, time.Now())
	require.NoError(t, err)

	_, err = Consume(m, ref, time.Now())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAlreadyGone, "a lost consume race must be ErrAlreadyGone, not a generic failure")
}

// consumedNames lists the routed copies in out/consumed/ by file name.
func consumedNames(t *testing.T, m PathMapper) []string {
	t.Helper()
	res, err := Sweep(m, testHarp, DirOutConsumed)
	require.NoError(t, err)
	var names []string
	for _, e := range res.Entries {
		names = append(names, string(e.Ref.Name))
	}
	return names
}

// Every Consume prunes out/consumed/ past DeliveredRetention, measured from
// when each copy was ROUTED: the clock is the caller's, so the window is
// forced here, never waited out.
func TestConsume_PrunesRoutedCopiesOlderThanTheRetentionWindow(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	now := time.Now()
	old, _ := seedOut(t, m, "routed long ago\n")
	young, _ := seedOut(t, m, "routed recently\n")
	fresh, _ := seedOut(t, m, "routed now\n")

	_, err := Consume(m, old, now.Add(-DeliveredRetention-time.Minute))
	require.NoError(t, err)
	_, err = Consume(m, young, now.Add(-DeliveredRetention+time.Minute))
	require.NoError(t, err)
	require.Len(t, consumedNames(t, m), 2, "nothing is past the window until a later Consume says so")

	_, err = Consume(m, fresh, now)
	require.NoError(t, err)
	got := consumedNames(t, m)
	require.NotContains(t, got, string(old.Name), "routed before the window: pruned")
	require.Contains(t, got, string(young.Name), "routed inside the window: kept")
	require.Contains(t, got, string(fresh.Name), "the copy just routed is kept")
}

// The window counts from the ROUTE, not from when the child wrote the file: a
// rename keeps the writer's mtime, and a message that sat in out/ for longer
// than the window (a coordinator down for a week) must not be pruned the
// moment it is routed.
func TestConsume_AFileWrittenLongAgoIsKeptFromItsRouteTime(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	now := time.Now()
	stale, _ := seedOut(t, m, "written before the window\n")
	path, err := m.Resolve(stale)
	require.NoError(t, err)
	written := now.Add(-2 * DeliveredRetention)
	require.NoError(t, os.Chtimes(path, written, written))

	_, err = Consume(m, stale, now)
	require.NoError(t, err)
	other, _ := seedOut(t, m, "a later route\n")
	_, err = Consume(m, other, now.Add(time.Minute))
	require.NoError(t, err)

	require.Contains(t, consumedNames(t, m), string(stale.Name), "routed just now: inside the window")
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

		err = Deliver(m, ref, "m-retracted", time.Now())
		require.ErrorIs(t, err, ErrAlreadyGone, "the reader must learn the message was retracted")
	})

	t.Run("reader wins", func(t *testing.T) {
		ref, _ := seedIn(t, m, "too late\n")
		require.NoError(t, Deliver(m, ref, "m-too-late", time.Now()))

		_, err := Withdraw(m, ref)
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
	w, err := NewWriter(afero.NewOsFs(), m, testHarp, DirIn, "coord")
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
