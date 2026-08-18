package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// --- DOCTOR-CHECK-SPOOL-BACKLOG-t0 -----------------------------------------

// writeRawSpoolMessage drops a spool file directly onto disk with a caller-
// chosen write stamp, bypassing spool.Writer (which always stamps time.Now)
// so a test can construct a message that is already old the moment it
// exists — exactly the "sat unconsumed since before the test even started"
// shape a real stuck entry has. It writes minimal-but-legal frontmatter
// (spool.Parse requires only kind and created) so spool.Sweep reads it as a
// real Entry, not a Problem.
func writeRawSpoolMessage(t *testing.T, mapper spool.PathMapper, harp string, dir spool.Dir, nanos int64, seq uint64, writer string, created time.Time) spool.Ref {
	t.Helper()
	require.NoError(t, spool.EnsureDirs(mapper, harp))
	dirPath, err := spool.DirPath(mapper, harp, dir)
	require.NoError(t, err)
	name := spool.Name{Nanos: nanos, Seq: seq, Writer: writer}
	data := fmt.Sprintf("---\nkind: message\ncreated: %s\n---\nbody\n", created.UTC().Format(time.RFC3339Nano))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, name.String()), []byte(data), 0o600))
	return spool.Ref{Harp: harp, Dir: dir, Name: name.String()}
}

func TestDoctorCheckSpoolBacklog_RightState_NoSessionsDirYet(t *testing.T) {
	testsupport.Isolate(t)
	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "no session directories yet")
}

// TestDoctorCheckSpoolBacklog_RightState_SessionsExistButNoSpool proves
// "a session exists but never turned spool delivery on" is reported as its
// own distinct OK state, not folded into (or confused with) "no sessions at
// all" or "spool checked, nothing stuck".
func TestDoctorCheckSpoolBacklog_RightState_SessionsExistButNoSpool(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "no session has a spool directory")
}

// TestDoctorCheckSpoolBacklog_RightState_HealthySpoolNothingStuck constructs
// a REAL spool with a message written moments ago (via the same Writer the
// runner and coordinator use) and proves it is reported as checked-and-
// healthy — the state actually observed — not just silently absent. This is
// the "looked and found nothing wrong" half that TestDoctorCheckHarpDurability's
// sibling proves for the durability check: it must read differently from
// "didn't look" (the two tests above) even though both are doctorOK.
func TestDoctorCheckSpoolBacklog_RightState_HealthySpoolNothingStuck(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	require.NoError(t, spool.EnsureDirs(mapper, harp))
	w, err := spool.NewWriter(mapper, harp, spool.DirIn, "coord")
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: "message", Body: "hello"})
	require.NoError(t, err)

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "1 session spool(s) checked")
	assert.Contains(t, check.Detail, "0 entries stuck")
	assert.Contains(t, check.Detail, "0 malformed entries",
		"a clean spool must say so explicitly, distinguishably from a spool that was never examined")
}

// TestDoctorCheckSpoolBacklog_WrongState_NamesTheStuckEntry is this check's
// core proof: an entry backdated well past doctorSpoolStuckAge, sitting
// unconsumed in out/, must be named in a warn — while a FRESH entry in in/
// (written moments ago, exactly like a message mid-flight) must NOT be
// reported. Both directions are asserted so the check cannot pass by always
// warning, and cannot pass by never looking at out/.
func TestDoctorCheckSpoolBacklog_WrongState_NamesTheStuckEntry(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	old := time.Now().Add(-10 * time.Minute)
	stuckRef := writeRawSpoolMessage(t, mapper, harp, spool.DirOut, old.UnixNano(), 1, "amber-quiet-heron", old)

	fresh := time.Now()
	_ = writeRawSpoolMessage(t, mapper, harp, spool.DirIn, fresh.UnixNano(), 1, "coord", fresh)

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, stuckRef.String(), "the stuck entry must be named by its ref")
	assert.Contains(t, check.Detail, "1 spool entr(ies)")
	assert.NotContains(t, check.Detail, "coord.md", "the fresh in/ entry must not be reported as stuck")
}

// TestDoctorCheckSpoolBacklog_CapsNamedListWithCount proves a machine with
// many stuck entries gets a bounded, readable line rather than an unbounded
// wall of refs — the same "cap at ~5 with a count" shape
// doctorCheckHarpDurability uses.
func TestDoctorCheckSpoolBacklog_CapsNamedListWithCount(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	old := time.Now().Add(-1 * time.Hour)
	for i := range 8 {
		writeRawSpoolMessage(t, mapper, harp, spool.DirOut, old.UnixNano()+int64(i), uint64(i+1), "amber-quiet-heron", old)
	}

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "8 spool entr(ies)")
	assert.Contains(t, check.Detail, "more")
}

// --- malformed entries: spool.Sweep's Problem, wired into this check -------

// writeRawSpoolFile drops an arbitrary-named file straight into a spool
// directory, bypassing spool.Writer AND spool.ParseName/spool.Parse's
// grammar entirely, so a test can construct exactly the "not a message at
// all" shape spool.Sweep reports as a Problem rather than an Entry.
func writeRawSpoolFile(t *testing.T, mapper spool.PathMapper, harp string, dir spool.Dir, name string, content string) string {
	t.Helper()
	require.NoError(t, spool.EnsureDirs(mapper, harp))
	dirPath, err := spool.DirPath(mapper, harp, dir)
	require.NoError(t, err)
	full := filepath.Join(dirPath, name)
	require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	return full
}

// TestDoctorCheckSpoolBacklog_WrongState_NamesTheMalformedFilename proves a
// filename outside the "<unixnano>.<seq>.<writer>.md" grammar is reported
// BY NAME, and worded distinctly from a stuck-but-valid entry (it must never
// say "sat unconsumed" — no amount of waiting turns a bad filename into a
// deliverable message). A fresh, validly-named entry in the same session's
// in/ is asserted absent from the report, so the check cannot pass by
// flagging everything it sees.
func TestDoctorCheckSpoolBacklog_WrongState_NamesTheMalformedFilename(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	writeRawSpoolFile(t, mapper, harp, spool.DirOut, "not-a-spool-message.txt", "whatever, not a message")

	fresh := time.Now()
	_ = writeRawSpoolMessage(t, mapper, harp, spool.DirIn, fresh.UnixNano(), 1, "coord", fresh)

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "1 spool entr(ies) are malformed")
	assert.Contains(t, check.Detail, "not-a-spool-message.txt", "the malformed entry must be named")
	assert.Contains(t, check.Detail, harp+":out/", "malformed entries are located by harp and direction")
	assert.NotContains(t, check.Detail, "sat unconsumed",
		"a malformed filename is not a stuck-but-valid entry and must not be worded as one")
}

// TestDoctorCheckSpoolBacklog_WrongState_NamesTheMalformedContent proves the
// OTHER Problem path: a filename that DOES parse against the grammar, but
// whose content spool.Parse refuses (no YAML frontmatter at all here) is
// still reported by name — distinct from both a stuck entry and a bad
// filename.
func TestDoctorCheckSpoolBacklog_WrongState_NamesTheMalformedContent(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	name := spool.Name{Nanos: time.Now().UnixNano(), Seq: 1, Writer: "coord"}
	writeRawSpoolFile(t, mapper, harp, spool.DirIn, name.String(), "no frontmatter here, just a body\n")

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "1 spool entr(ies) are malformed")
	assert.Contains(t, check.Detail, name.String(), "the malformed entry must be named")
	assert.NotContains(t, check.Detail, "sat unconsumed",
		"malformed content is not a stuck-but-valid entry and must not be worded as one")
}

// TestDoctorCheckSpoolBacklog_RightState_MalformedFileDoesNotCountAsStuck
// proves the flip side of the two tests above: a spool holding ONLY a
// malformed entry (no stuck ones) must not be reported as having any stuck
// entries — the two lists are independent, not one bucket wearing two
// labels.
func TestDoctorCheckSpoolBacklog_RightState_MalformedFileDoesNotCountAsStuck(t *testing.T) {
	testsupport.Isolate(t)
	mapper := spool.NewHomeMapper()
	harp := "amber-quiet-heron"
	writeRawSpoolFile(t, mapper, harp, spool.DirOut, "garbage.md.bak", "irrelevant")

	check := doctorCheckSpoolBacklog()
	assert.Equal(t, doctorWarn, check.Status)
	assert.NotContains(t, check.Detail, "0 spool entr(ies) sat unconsumed")
	assert.NotContains(t, check.Detail, "sat unconsumed")
}
