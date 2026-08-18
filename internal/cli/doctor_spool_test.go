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
