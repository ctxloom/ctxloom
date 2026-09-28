package spool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// The owner's reader is a SUBPROCESS PER TURN (the turn-start hook), so its
// reservation cannot live in any process's memory: what it has taken and not
// yet acknowledged is a DIRECTORY, in/claimed/, and every test here reads
// that directory back rather than trusting a return value.

// bodiesOf lists the bodies of entries in order, so an assertion names the
// messages it means.
func bodiesOf(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Message.Body)
	}
	return out
}

// filesIn lists the plain files (not sub-directories) of one spool directory,
// straight off readdir, so a test's view of the disk cannot be the package's.
func filesIn(t *testing.T, m PathMapper, dir Dir) []string {
	t.Helper()
	path, err := DirPath(m, testHarp, dir)
	require.NoError(t, err)
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestClaim_TakesEveryUnclaimedMessageIntoClaimedAndReturnsItInOrder(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	first, _ := seedIn(t, m, "first\n")
	second, _ := seedIn(t, m, "second\n")

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Empty(t, res.Problems)
	assert.Equal(t, []string{"first\n", "second\n"}, bodiesOf(res.Entries), "chronological order is the filename order")
	for i, e := range res.Entries {
		assert.Equal(t, ClaimedDirName, e.Ref.Dir, "a claimed entry is addressed where it now lives")
		assert.NotNil(t, e.Message)
		assert.Equal(t, []Ref{first, second}[i].Name, e.Ref.Name, "claiming must not rename the file's identity")
	}

	assert.Empty(t, filesIn(t, m, DirIn), "a claimed message has LEFT in/")
	assert.ElementsMatch(t, []string{first.Name, second.Name}, filesIn(t, m, ClaimedDirName), "…and sits in in/claimed/ until acknowledged")
	assert.Empty(t, filesIn(t, m, DirInConsumed), "claiming is not consuming")
}

// TestClaim_RedeliversWhatWasClaimedButNeverAcknowledged is the crash between
// claim and ack: a reader took the message and died before acknowledging it
// (a hook killed mid-write, an engine that never started the turn). The next
// Claim must hand it out AGAIN — at-least-once is the substrate's floor, and a
// reservation that survived the crash but delivered nothing would be a
// message permanently invisible.
func TestClaim_RedeliversWhatWasClaimedButNeverAcknowledged(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "taken then lost\n")

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Equal(t, []string{"taken then lost\n"}, bodiesOf(res.Entries))
	// No Ack: the reader crashed here.

	again, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"taken then lost\n"}, bodiesOf(again.Entries), "a claimed-but-unacknowledged message is delivered again")
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, ClaimedDirName), "…from in/claimed/, where it still sits")
}

// TestClaim_InterleavesLeftoversWithNewerMailChronologically pins that a
// re-delivered leftover keeps its place in time: the filename order is the
// send order across BOTH directories, so the reader sees the older message
// first even though it was claimed earlier.
func TestClaim_InterleavesLeftoversWithNewerMailChronologically(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedIn(t, m, "older\n")
	_, err := Claim(m, testHarp)
	require.NoError(t, err)
	seedIn(t, m, "newer\n")

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"older\n", "newer\n"}, bodiesOf(res.Entries))
}

func TestAck_MovesAClaimedMessageIntoConsumedAndClaimNeverReturnsItAgain(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, before := seedIn(t, m, "delivered for good\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)

	require.NoError(t, Ack(m, testHarp, ref.Name))

	assert.Empty(t, filesIn(t, m, ClaimedDirName), "an acknowledged message has left in/claimed/")
	consumed := filesIn(t, m, DirInConsumed)
	require.Equal(t, []string{ref.Name}, consumed, "the ack is the rename into in/consumed/ — the audit trail, never a delete")
	path, err := DirPath(m, testHarp, DirInConsumed)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(path, ref.Name))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the consumed copy is byte-identical to what was delivered")

	again, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, again.Entries, "a consumed message is never delivered again")
}

// TestAck_OfAMessageNotInClaimedIsAlreadyGone: the other consumer won (or
// nothing was ever claimed under that name). The typed sentinel is what lets
// a caller distinguish a lost race from a broken spool.
func TestAck_OfAMessageNotInClaimedIsAlreadyGone(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "never claimed\n")

	err := Ack(m, testHarp, ref.Name)
	require.ErrorIs(t, err, ErrAlreadyGone)
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, DirIn), "an ack of the wrong state must not touch in/")
}

func TestAck_RefusesANameOutsideTheBareFilenameGrammar(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	err := Ack(m, testHarp, "../escape")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrAlreadyGone, "a traversal attempt is a refusal, not a lost race")
}

func TestPending_IsTrueOnlyWhileInHoldsAnUnclaimedFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	require.NoError(t, EnsureDirs(m, testHarp))

	pending, err := Pending(m, testHarp)
	require.NoError(t, err)
	assert.False(t, pending, "an empty in/ has nothing pending")

	seedIn(t, m, "waiting\n")
	pending, err = Pending(m, testHarp)
	require.NoError(t, err)
	assert.True(t, pending, "a file in in/ is pending")

	_, err = Claim(m, testHarp)
	require.NoError(t, err)
	pending, err = Pending(m, testHarp)
	require.NoError(t, err)
	assert.False(t, pending, "a CLAIMED message is spoken for: in/claimed/ is not in/ — the peek asks only whether unclaimed mail waits")

	seedIn(t, m, "more\n")
	pending, err = Pending(m, testHarp)
	require.NoError(t, err)
	assert.True(t, pending)
}

// TestPending_ASpoolThatWasNeverCreatedIsNotPending: nothing has ever been
// written for this harp, which is the common case for a session that never
// delegated. "Not there" is false, not an error — the same answer the
// coordinator's own sweep gives a missing directory.
func TestPending_ASpoolThatWasNeverCreatedIsNotPending(t *testing.T) {
	hostHome(t)
	pending, err := Pending(NewHomeMapper(), testHarp)
	require.NoError(t, err)
	assert.False(t, pending)
}

// TestClaim_ASpoolThatWasNeverCreatedClaimsNothing is the same rule for the
// reader: a turn that starts before anything was ever sent must not fail
// the turn over a directory that does not exist.
func TestClaim_ASpoolThatWasNeverCreatedClaimsNothing(t *testing.T) {
	hostHome(t)
	res, err := Claim(NewHomeMapper(), testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.Empty(t, res.Problems)
}

// TestClaim_ReportsAnUnreadableFileAsAProblemAndLeavesItInPlace pins the
// nothing-silently-dropped property across the claim: a file in in/ that is
// not a message is neither claimed nor skipped — it is returned as a Problem,
// and it stays where it was so an operator (doctor) can still find it.
func TestClaim_ReportsAnUnreadableFileAsAProblemAndLeavesItInPlace(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedIn(t, m, "fine\n")
	inDir, err := DirPath(m, testHarp, DirIn)
	require.NoError(t, err)
	junk := filepath.Join(inDir, "00000000000000000000000.00000001.coord.md")
	require.NoError(t, os.WriteFile(junk, []byte("no frontmatter at all\n"), owneronly.FileMode))

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"fine\n"}, bodiesOf(res.Entries))
	require.Len(t, res.Problems, 1, "the unparseable file must be REPORTED")
	assert.Equal(t, junk, res.Problems[0].Path)
	assert.Equal(t, []string{filepath.Base(junk)}, filesIn(t, m, DirIn), "…and left in in/, never moved somewhere a sweep will not look")
}

// TestFail_MovesAClaimedEntryIntoFailed: the reader's semantic refusal (an
// unknown kind, a payload that will not decode) must have the same terminal
// state for a claimed entry as for a live one, or a refused message would be
// re-delivered and re-refused on every turn for the life of the session.
func TestFail_MovesAClaimedEntryIntoFailed(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	ref, _ := seedIn(t, m, "unknown kind\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)

	require.NoError(t, Fail(m, res.Entries[0].Ref))

	assert.Empty(t, filesIn(t, m, ClaimedDirName))
	assert.Equal(t, []string{ref.Name}, filesIn(t, m, FailedDirName))
}

// TestClaimedDirName_IsLocalNotWire: like the failed/ directories, in/claimed/
// is a reader-local state nothing ever rings a doorbell about, so it must
// stay OUT of the closed wire set — Ref.Validate refuses it — while the
// local path helpers still reach it.
func TestClaimedDirName_IsLocalNotWire(t *testing.T) {
	assert.False(t, ClaimedDirName.Valid(), "in/claimed/ carries no wire obligation and must not grow the doorbell enum")
	assert.Error(t, (Ref{Harp: testHarp, Dir: ClaimedDirName, Name: "0.x.md"}).Validate())
	hostHome(t)
	path, err := DirPath(NewHomeMapper(), testHarp, ClaimedDirName)
	require.NoError(t, err, "a local directory is still addressable by the reader that owns it")
	assert.Equal(t, "claimed", filepath.Base(path))
	assert.Equal(t, "in", filepath.Base(filepath.Dir(path)))
}

// seedOrigin writes one in/ message carrying a producer's origin id — how a
// coordinator re-sending the SAME mailbox message arrives: a new file, a new
// filename, the same identity.
func seedOrigin(t *testing.T, m PathMapper, origin, body string) Ref {
	t.Helper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, OriginID: origin, Body: body})
	require.NoError(t, err)
	return ref
}

// TestClaim_ADuplicateOfWhatWasAlreadyConsumedGoesStraightToConsumed is
// at-least-once meeting a reader with no memory: the hook is a new process
// every turn, so the only record of "already delivered" is in/consumed/.
func TestClaim_ADuplicateOfWhatWasAlreadyConsumedGoesStraightToConsumed(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "mail-1", "first copy\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	require.NoError(t, Ack(m, testHarp, res.Entries[0].Ref.Name))

	dup := seedOrigin(t, m, "mail-1", "second copy\n")
	res, err = Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries, "a message whose identity is already consumed is not delivered again")
	assert.Empty(t, filesIn(t, m, ClaimedDirName))
	assert.Contains(t, filesIn(t, m, DirInConsumed), dup.Name, "the duplicate is kept in the audit trail, not deleted")
}

// Two copies arriving together, or one arriving while its twin is claimed and
// unacknowledged, are one delivery.
func TestClaim_DuplicatesInFlightAreDeliveredOnce(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedOrigin(t, m, "mail-2", "a\n")
	seedOrigin(t, m, "mail-2", "b\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"a\n"}, bodiesOf(res.Entries), "the earlier copy wins")

	seedOrigin(t, m, "mail-2", "c\n")
	res, err = Claim(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{"a\n"}, bodiesOf(res.Entries), "a copy of a claimed-but-unacknowledged message is not a second delivery")
	assert.Len(t, filesIn(t, m, DirInConsumed), 2)
}

// Without an origin id, a message's identity is its filename stem, so two
// distinct runner-written messages never collapse into one.
func TestClaim_MessagesWithoutAnOriginAreDistinct(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	seedIn(t, m, "x\n")
	seedIn(t, m, "x\n")
	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Len(t, res.Entries, 2)
}

func TestEntryIdentity_IsTheOriginElseTheFilenameStem(t *testing.T) {
	n := Name{Nanos: 1, Seq: 2, Writer: "coord"}
	assert.Equal(t, "m-1", Entry{Name: n, Message: &Message{OriginID: "m-1"}}.Identity())
	assert.Equal(t, n.Stem(), Entry{Name: n, Message: &Message{}}.Identity())
	assert.Equal(t, n.Stem(), Entry{Name: n}.Identity())
}
