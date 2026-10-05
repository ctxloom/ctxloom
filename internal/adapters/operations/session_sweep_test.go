package operations

import (
	"context"
	"os"
	"path/filepath"
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
// origin, with or without its essence.
func ssSeed(t *testing.T, harp, origin string, compacted bool) string {
	t.Helper()
	dir := srSeedHarp(t, harp)
	out := t.TempDir()
	sidecar := "project_dir: /tmp/demo\noutput_dir: " + out + "\n"
	if origin != "" {
		sidecar += "origin: " + origin + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, paths.SessionSidecarFileName), []byte(sidecar), 0o644))
	if compacted {
		require.NoError(t, os.WriteFile(filepath.Join(out, paths.EssenceFileName), []byte("# essence\n"), 0o644))
	}
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
// compacted human session and the uncompacted one-shot, and refuses the
// uncompacted human session with the command that lifts the refusal.
func TestSweepSessions_PurgeRows(t *testing.T) {
	testsupport.Isolate(t)
	compacted := ssSeed(t, "done-quiet-heron", "session", true)
	uncompacted := ssSeed(t, "raw-quiet-heron", "session", false)
	oneshot := ssSeed(t, "shot-quiet-heron", "oneshot", false)
	transcript := func(dir string) string {
		return filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName)
	}
	req := SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), PurgeCutoff: srCutoff()}

	rep, err := SweepSessions(context.Background(), git.NewExec(), req)
	require.NoError(t, err)
	assert.Equal(t, SweepPlanned, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	for _, dir := range []string{compacted, uncompacted, oneshot} {
		assert.FileExists(t, transcript(dir), "a report changes nothing")
		srAssertIntact(t, dir, paths.ScratchDirName)
	}

	req.Apply = true
	rep, err = SweepSessions(context.Background(), git.NewExec(), req)
	require.NoError(t, err)

	assert.Equal(t, SweepDone, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	assert.NoFileExists(t, transcript(compacted))
	out, ok := sessions.OutputDirOf(compacted)
	require.True(t, ok)
	assert.FileExists(t, filepath.Join(out, paths.EssenceFileName), "the output dir is the human's: a sweep never deletes it")

	assert.Equal(t, SweepDone, ssRows(rep, "shot-quiet-heron")[SweepPurge].Verdict)
	assert.NoFileExists(t, transcript(oneshot))

	spare := ssRows(rep, "raw-quiet-heron")[SweepSpare]
	assert.Equal(t, "ctxloom session compact raw-quiet-heron", spare.Command)
	assert.FileExists(t, transcript(uncompacted), "a human's uncompacted transcript is its only record")

	for _, dir := range []string{compacted, uncompacted, oneshot} {
		srAssertGone(t, dir, paths.ScratchDirName)
	}
}

// No purge age, no purge: the row is held and an apply leaves the bytes.
func TestSweepSessions_NoPurgeCutoffHoldsThePurge(t *testing.T) {
	testsupport.Isolate(t)
	dir := ssSeed(t, "done-quiet-heron", "session", true)
	rep, err := SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/tmp/demo", ReclaimCutoff: srCutoff(), Apply: true})
	require.NoError(t, err)
	assert.Equal(t, SweepHeld, ssRows(rep, "done-quiet-heron")[SweepPurge].Verdict)
	assert.FileExists(t, filepath.Join(dir, paths.TranscriptsDirName, paths.CanonicalTranscriptFileName))
}

// The scope is the project: another project's session is not in the report.
func TestSweepSessions_ScopeIsTheProject(t *testing.T) {
	testsupport.Isolate(t)
	ssSeed(t, "done-quiet-heron", "session", true)
	rep, err := SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/elsewhere", ReclaimCutoff: time.Now()})
	require.NoError(t, err)
	assert.Empty(t, rep.Rows)

	rep, err = SweepSessions(context.Background(), git.NewExec(),
		SweepRequest{ProjectDir: "/elsewhere", ReclaimCutoff: time.Now(), AllProjects: true})
	require.NoError(t, err)
	assert.NotEmpty(t, ssRows(rep, "done-quiet-heron"))
}
