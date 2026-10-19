package operations

import (
	"context"
	"os"
	"path/filepath"
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
