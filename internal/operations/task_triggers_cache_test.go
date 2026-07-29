package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func sampleTaskInputForFingerprint() triggers.TaskInput {
	return triggers.TaskInput{
		HarpID:     "swift-amber-falcon",
		Text:       "wire the signing CLI",
		Trigger:    "when the signing CLI ships",
		DeferredAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		CommitsSince: []triggers.CommitSummary{
			{SHA: "abc123", Date: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), Subject: "feat: ship it"},
		},
		ChangedFiles: []string{"internal/signing/cli.go"},
	}
}

func TestFingerprintTask_StableForIdenticalInput(t *testing.T) {
	ti := sampleTaskInputForFingerprint()
	repo := triggers.RepoState{Dirs: []string{"internal/signing"}}
	a := fingerprintTask(ti, repo)
	b := fingerprintTask(ti, repo)
	assert.Equal(t, a, b)
	assert.NotEmpty(t, a)
}

func TestFingerprintTask_ChangesWithTrigger(t *testing.T) {
	ti := sampleTaskInputForFingerprint()
	repo := triggers.RepoState{}
	a := fingerprintTask(ti, repo)
	ti.Trigger = "something else entirely"
	b := fingerprintTask(ti, repo)
	assert.NotEqual(t, a, b)
}

func TestFingerprintTask_ChangesWithNewCommit(t *testing.T) {
	ti := sampleTaskInputForFingerprint()
	repo := triggers.RepoState{}
	a := fingerprintTask(ti, repo)
	ti.CommitsSince = append(ti.CommitsSince, triggers.CommitSummary{SHA: "def456", Subject: "another commit"})
	b := fingerprintTask(ti, repo)
	assert.NotEqual(t, a, b)
}

// Existence-style triggers depend on repo-global state (the batch header),
// not just this task's own evidence — the fingerprint must cover it too, or
// a cache hit could serve a stale verdict after the repo state that answers
// the trigger has changed.
func TestFingerprintTask_ChangesWithRepoState(t *testing.T) {
	ti := sampleTaskInputForFingerprint()
	a := fingerprintTask(ti, triggers.RepoState{Dirs: []string{"internal/signing"}})
	b := fingerprintTask(ti, triggers.RepoState{Dirs: []string{"internal/signing", "internal/other"}})
	assert.NotEqual(t, a, b)
}

func TestFingerprintTask_ChangesWithDeferredAt(t *testing.T) {
	ti := sampleTaskInputForFingerprint()
	repo := triggers.RepoState{}
	a := fingerprintTask(ti, repo)
	ti.DeferredAt = ti.DeferredAt.Add(24 * time.Hour)
	b := fingerprintTask(ti, repo)
	assert.NotEqual(t, a, b)
}

func TestTriggerCache_SaveLoadRoundTrip(t *testing.T) {
	testsupport.Isolate(t)

	c := triggerVerdictCache{Tasks: map[string]triggerCacheEntry{
		"swift-amber-falcon": {
			Fingerprint: "abc123",
			Verdict:     triggers.Verdict{HarpID: "swift-amber-falcon", Outcome: triggers.Fired, Reasoning: "it shipped"},
		},
	}}
	saveTriggerCache("proj-1", c)

	got := loadTriggerCache("proj-1")
	require.Contains(t, got.Tasks, "swift-amber-falcon")
	assert.Equal(t, "abc123", got.Tasks["swift-amber-falcon"].Fingerprint)
	assert.Equal(t, triggers.Fired, got.Tasks["swift-amber-falcon"].Verdict.Outcome)
}

func TestTriggerCache_DifferentProjectsAreIsolated(t *testing.T) {
	testsupport.Isolate(t)

	saveTriggerCache("proj-a", triggerVerdictCache{Tasks: map[string]triggerCacheEntry{
		"task-a": {Fingerprint: "fp-a", Verdict: triggers.Verdict{HarpID: "task-a", Outcome: triggers.Fired}},
	}})
	saveTriggerCache("proj-b", triggerVerdictCache{Tasks: map[string]triggerCacheEntry{
		"task-b": {Fingerprint: "fp-b", Verdict: triggers.Verdict{HarpID: "task-b", Outcome: triggers.NotFired}},
	}})

	a := loadTriggerCache("proj-a")
	b := loadTriggerCache("proj-b")
	assert.Contains(t, a.Tasks, "task-a")
	assert.NotContains(t, a.Tasks, "task-b")
	assert.Contains(t, b.Tasks, "task-b")
	assert.NotContains(t, b.Tasks, "task-a")
}

func TestLoadTriggerCache_MissingFileIsEmptyNotError(t *testing.T) {
	testsupport.Isolate(t)
	got := loadTriggerCache("never-saved-project")
	assert.NotNil(t, got.Tasks)
	assert.Empty(t, got.Tasks)
}

func TestLoadTriggerCache_EmptyProjectIDIsEmpty(t *testing.T) {
	testsupport.Isolate(t)
	got := loadTriggerCache("")
	assert.Empty(t, got.Tasks)
}

// A corrupt cache file (hand-edited, truncated, or from a future incompatible
// version) must degrade to an empty cache — never error out or panic — since
// the whole cache is supposed to be safe to delete at any time.
func TestLoadTriggerCache_CorruptFileDegradesToEmpty(t *testing.T) {
	testsupport.Isolate(t)

	dir, err := paths.TriggerCacheDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path, err := triggerCacheFilePath("busted-project")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o644))

	assert.NotPanics(t, func() {
		got := loadTriggerCache("busted-project")
		assert.Empty(t, got.Tasks)
	})
}

func TestSaveTriggerCache_EmptyProjectIDIsNoop(t *testing.T) {
	testsupport.Isolate(t)
	assert.NotPanics(t, func() {
		saveTriggerCache("", triggerVerdictCache{Tasks: map[string]triggerCacheEntry{"x": {}}})
	})
	dir, err := paths.TriggerCacheDir()
	require.NoError(t, err)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr), "no project id must never create the cache dir")
}

// Cache files for different projects must not collide even when project ids
// share a prefix or differ only by characters that would matter if used
// directly as a filename.
func TestTriggerCacheFilePath_DistinctPerProjectID(t *testing.T) {
	testsupport.Isolate(t)
	p1, err := triggerCacheFilePath("proj-1")
	require.NoError(t, err)
	p2, err := triggerCacheFilePath("proj-2")
	require.NoError(t, err)
	assert.NotEqual(t, p1, p2)
	assert.True(t, filepath.IsAbs(p1))
}
