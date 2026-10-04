package coord

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withOutputDir points the tree root's output dir at a test dir.
func withOutputDir(t *testing.T, c *Coordinator) string {
	t.Helper()
	out := t.TempDir()
	prev := outputDirOf
	outputDirOf = func(harp string) (string, error) {
		require.Equal(t, c.rootHarp, harp, "outputs go to the tree root's output dir")
		return out, nil
	}
	t.Cleanup(func() { outputDirOf = prev })
	return out
}

// publish stores content and journals it as revision-bumping artifact id.
func publish(t *testing.T, c *Coordinator, harp, id, name, content string) {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	_, _, err := c.artifacts.writeAtomic(strings.NewReader(content), sum[:], uint64(len(content)))
	require.NoError(t, err)
	require.NoError(t, c.recordArtifact(harp, ArtifactProduced{ArtifactID: id, Name: name, SHA256: sum[:], SizeBytes: uint64(len(content))}))
}

// startedChild is a delegated child with a live, idle run.
func startedChild(t *testing.T, c *Coordinator) *RunOutcome {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)
	return out
}

// A FINAL report lands in the human's output dir: each artifact it publishes,
// at its latest revision, and its text as markdown, in a folder named for the
// child.
func TestFinalReport_IsWrittenIntoTheOutputDir(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(t, nil), nil)
	out := withOutputDir(t, c)
	child := startedChild(t, c)

	publish(t, c, child.Harp, "a1", "findings.md", "first draft")
	publish(t, c, child.Harp, "a1", "findings.md", "final findings")
	require.NoError(t, c.recordSummary(child.Harp, child.RunID, 1, Summary{
		Scope: ScopeFinal, Text: "the verdict", ArtifactIDs: []string{"a1"},
	}))

	dir := filepath.Join(out, reportsDirName, child.Harp)
	got, err := os.ReadFile(filepath.Join(dir, "findings.md"))
	require.NoError(t, err)
	assert.Equal(t, "final findings", string(got), "the latest revision only")
	text, err := os.ReadFile(filepath.Join(dir, finalReportFileName))
	require.NoError(t, err)
	assert.Equal(t, "the verdict", string(text))
}

// Only FINAL is the deliverable: a progress report writes nothing.
func TestProgressReport_WritesNothingToTheOutputDir(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(t, nil), nil)
	out := withOutputDir(t, c)
	child := startedChild(t, c)

	require.NoError(t, c.recordSummary(child.Harp, child.RunID, 1, Summary{Scope: ScopeProgress, Text: "still working"}))
	_, err := os.Stat(filepath.Join(out, reportsDirName))
	assert.True(t, os.IsNotExist(err))
}

// Two published artifacts sharing a name, or one sharing the report's own
// name, are all kept: the later ones are prefixed with their artifact id.
// A name that would leave the folder is reduced to its base.
func TestFinalReport_KeepsEveryArtifactWhenNamesCollide(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, startRunSpawner(t, nil), nil)
	out := withOutputDir(t, c)
	child := startedChild(t, c)

	publish(t, c, child.Harp, "a1", "notes.md", "one")
	publish(t, c, child.Harp, "a2", "sub/notes.md", "two")
	publish(t, c, child.Harp, "a3", finalReportFileName, "three")
	publish(t, c, child.Harp, "a4", "../../escape.md", "four")
	require.NoError(t, c.recordSummary(child.Harp, child.RunID, 1, Summary{
		Scope: ScopeFinal, Text: "done", ArtifactIDs: []string{"a1", "a2", "a3", "a4"},
	}))

	dir := filepath.Join(out, reportsDirName, child.Harp)
	for name, want := range map[string]string{
		"notes.md": "one", "a2-notes.md": "two", "a3-" + finalReportFileName: "three", "escape.md": "four", finalReportFileName: "done",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if assert.NoError(t, err, name) {
			assert.Equal(t, want, string(got), name)
		}
	}
}
