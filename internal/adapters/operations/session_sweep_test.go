package operations

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ssSeed plants an aged, provably-ended session of srLayout's shape, as
// origin, whose transcript retention is released ("") or keeps it: it ended
// within the retention period ("recent") or is marked keep ("keep").
func ssSeed(t *testing.T, harp, origin, retain string) string {
	t.Helper()
	dir := srSeedHarp(t, harp)
	ended := srOldEnough
	if retain == "recent" {
		ended = time.Now()
	}
	sidecar := "schema_version: 1\nproject_dir: /tmp/demo\nended_at: " + ended.UTC().Format(time.RFC3339) + "\n"
	if origin != "" {
		sidecar += "origin: " + origin + "\n"
	}
	if retain == "keep" {
		sidecar = "schema_version: 2\n" + strings.TrimPrefix(sidecar, "schema_version: 1\n") + "keep: true\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionSidecarFileName), []byte(sidecar), 0o644))
	srBackdate(t, dir)
	srSeedDeadSession(t, harp)
	return dir
}

func ssRows(rep SweepReport, harp string) map[SweepAction]SweepRow {
	out := map[SweepAction]SweepRow{}
	for _, r := range rep.Rows {
		if r.Harp == harp {
			out[r.Action] = r
		}
	}
	return out
}

// The purge half end to end: a report changes nothing; an apply purges the
// released human session and a recent one-shot, and spares the human
// sessions retention keeps — the recent one, and the kept one with the
// command that releases it.
func TestSweepSessions_PurgeRows(t *testing.T) {
	testsupport.Isolate(t)
	released := ssSeed(t, "done-quiet-heron", "session", "")
	recent := ssSeed(t, "raw-quiet-heron", "session", "recent")
	kept := ssSeed(t, "kept-quiet-heron", "session", "keep")
	oneshot := ssSeed(t, "shot-quiet-heron", "oneshot", "recent")
	transcript := func(dir string) string {
		return filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName)
	}
	req := SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), PurgeCutoff: srCutoff(), Retention: sessions.Retention{Cutoff: srCutoff()}}

	rep, err := SweepSessions(context.Background(), git.NewExec(), req)
	require.NoError(t, err)
	assert.Equal(t, SweepPlanned, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	for _, dir := range []string{released, recent, kept, oneshot} {
		assert.FileExists(t, transcript(dir), "a report changes nothing")
		srAssertIntact(t, dir, paths.ScratchDirName)
	}

	req.Apply = true
	rep, err = SweepSessions(context.Background(), git.NewExec(), req)
	require.NoError(t, err)

	assert.Equal(t, SweepDone, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	assert.NoFileExists(t, transcript(released))

	assert.Equal(t, SweepDone, ssRows(rep, "shot-quiet-heron")[SweepPurge].Verdict)
	assert.NoFileExists(t, transcript(oneshot))

	spare := ssRows(rep, "raw-quiet-heron")[SweepSpare]
	assert.Contains(t, spare.Reason, "retention period")
	assert.Empty(t, spare.Command)
	assert.FileExists(t, transcript(recent), "a transcript within its retention period is the session's record")

	spare = ssRows(rep, "kept-quiet-heron")[SweepSpare]
	assert.Contains(t, spare.Reason, "marked keep")
	assert.Equal(t, "ctxloom session keep kept-quiet-heron --undo", spare.Command)
	assert.FileExists(t, transcript(kept), "a kept transcript is never purged")

	for _, dir := range []string{released, recent, kept, oneshot} {
		srAssertGone(t, dir, paths.ScratchDirName)
	}
}

// No purge age, no purge: the row is held and an apply leaves the bytes.
func TestSweepSessions_NoPurgeCutoffHoldsThePurge(t *testing.T) {
	testsupport.Isolate(t)
	dir := ssSeed(t, "done-quiet-heron", "session", "")
	rep, err := SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), Retention: sessions.Retention{Cutoff: srCutoff()}, Apply: true})
	require.NoError(t, err)
	assert.Equal(t, SweepHeld, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	assert.FileExists(t, filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName))
}

// The scope is the project: another project's session is not in the report.
func TestSweepSessions_ScopeIsTheProject(t *testing.T) {
	testsupport.Isolate(t)
	ssSeed(t, "done-quiet-heron", "session", "")
	rep, err := SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/elsewhere", ReclaimCutoff: time.Now()})
	require.NoError(t, err)
	assert.Empty(t, rep.Rows)

	rep, err = SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/elsewhere", ReclaimCutoff: time.Now(), AllProjects: true})
	require.NoError(t, err)
	assert.NotEmpty(t, ssRows(rep, "done-quiet-heron"))
}

// ssFacts is the one session's facts from ClassifySessions.
func ssFacts(t *testing.T, req SweepRequest, harp string) SessionFacts {
	t.Helper()
	all, err := ClassifySessions(context.Background(), git.NewExec(), req)
	require.NoError(t, err)
	for _, f := range all {
		if f.Harp == harp {
			return f
		}
	}
	t.Fatalf("no facts for %s", harp)
	return SessionFacts{}
}

// ssBytesUnder is the size of the fixture's files under the named top-level
// members.
func ssBytesUnder(members ...string) (n int64) {
	for rel, body := range srLayout {
		top, _, _ := strings.Cut(rel, "/")
		if slices.Contains(members, top) {
			n += int64(len(body))
		}
	}
	return n
}

// A session whose record cannot be read has nothing proving it ended: its
// transcript is retained however old its files are, the reason is reported,
// and a persist-scope reclaim measures only what the reaper would take from
// it — the default scope's members.
func TestClassifySessions_NoRecordRetainsTheTranscript(t *testing.T) {
	testsupport.Isolate(t)
	dir := ssSeed(t, "lost-quiet-heron", "session", "")
	require.NoError(t, os.Remove(filepath.Join(dir, paths.SessionSidecarFileName)))
	srBackdate(t, dir)
	req := SweepRequest{AllProjects: true, ReclaimCutoff: srCutoff(), ReclaimScope: paths.Persist, Retention: sessions.Retention{Cutoff: srCutoff()}}

	f := ssFacts(t, req, "lost-quiet-heron")
	require.ErrorIs(t, f.Retained, sessions.ErrTranscriptRecent)
	assert.Equal(t, f.Retained.Error(), f.RetainedReason)

	released := ssFacts(t, SweepRequest{AllProjects: true, ReclaimCutoff: srCutoff(), Retention: sessions.Retention{Cutoff: srCutoff()}}, "lost-quiet-heron")
	assert.Equal(t, released.ReclaimBytes, f.ReclaimBytes, "a retained session's persist reclaim takes only the default scope's members")
}

// A record-less session retained by its recent conversation anyway is
// reported by when it ended, not overwritten by the missing record.
func TestClassifySessions_NoRecordRecentReportsWhenItEnded(t *testing.T) {
	testsupport.Isolate(t)
	dir := ssSeed(t, "lost-quiet-heron", "session", "")
	require.NoError(t, os.Remove(filepath.Join(dir, paths.SessionSidecarFileName)))
	srBackdate(t, dir)
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName), recent, recent))

	f := ssFacts(t, SweepRequest{AllProjects: true, ReclaimCutoff: time.Now(), Retention: sessions.Retention{Cutoff: srCutoff()}}, "lost-quiet-heron")
	require.ErrorIs(t, f.Retained, sessions.ErrTranscriptRecent)
	assert.Contains(t, f.RetainedReason, recent.UTC().Format(time.RFC3339))
}

// A released session's persist-scope reclaim measures its transcripts too;
// a retained one's does not.
func TestClassifySessions_PersistReclaimMeasuresTheTranscriptOnlyWhenReleased(t *testing.T) {
	testsupport.Isolate(t)
	ssSeed(t, "done-quiet-heron", "session", "")
	ssSeed(t, "raw-quiet-heron", "session", "recent")
	req := SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), Retention: sessions.Retention{Cutoff: srCutoff()}}
	persist := req
	persist.ReclaimScope = paths.Persist

	def := ssFacts(t, req, "done-quiet-heron")
	assert.Equal(t, def.ReclaimBytes+ssBytesUnder(paths.TranscriptsDirName, paths.PackageDirName), ssFacts(t, persist, "done-quiet-heron").ReclaimBytes)
	assert.Equal(t, ssFacts(t, req, "raw-quiet-heron").ReclaimBytes, ssFacts(t, persist, "raw-quiet-heron").ReclaimBytes)
}

// A session is purgeable by its machine-written files alone, and its purge
// bytes are theirs: an authored file beside them counts for neither.
func TestClassifySessions_PurgeFactsAreTheMachineFilesOnly(t *testing.T) {
	testsupport.Isolate(t)
	dir := ssSeed(t, "done-quiet-heron", "session", "")
	req := SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), Retention: sessions.Retention{Cutoff: srCutoff()}}

	f := ssFacts(t, req, "done-quiet-heron")
	assert.True(t, f.Purgeable)
	assert.Equal(t, ssBytesUnder(paths.TranscriptsDirName), f.PurgeBytes, "the purge takes the transcripts, not the authored package")

	for rel := range srLayout {
		if strings.HasPrefix(rel, paths.TranscriptsDirName+"/") {
			require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(rel))))
		}
	}
	f = ssFacts(t, req, "done-quiet-heron")
	assert.False(t, f.Purgeable, "authored files alone are nothing to purge")
	assert.Zero(t, f.PurgeBytes)
}
