package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// staleLinkedWorktree builds the shape projectroot.TaskStoreRoot refuses: a
// linked git worktree whose primary checkout does not exist.
//
// It also writes a project config declaring `homing: repo`, which is what
// makes a wrong answer OBSERVABLE rather than merely unusual. That setting
// lives in <WorkDir>/.taskloom/config.yaml, so it is readable only through
// WorkDir; a resolution that proceeds with WorkDir empty silently loses the
// whole project config layer (taskloomconfig.projectConfigPath returns "" for
// an empty dir) and redirects the store from the repo's checked-in log to
// ~/.ctxloom/tasks/<pinned-id>.jsonl. The returned dir is the worktree.
func staleLinkedWorktree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	wt := filepath.Join(base, "wt")
	require.NoError(t, os.MkdirAll(wt, 0o755))
	// <base>/main is deliberately never created — that absence IS the stale
	// pointer. The gitdir must keep the <commondir>/worktrees/<name> shape or
	// DetectWorktree reads it as a submodule and reports Linked=false.
	gitdir := filepath.Join(base, "main", ".git", "worktrees", "wt")
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
	writeConfigForTest(t, wt, "homing: repo\n")
	return wt
}

// jsonlUnder returns every task log beneath dir, so a test can assert that a
// refused operation wrote NOTHING rather than trusting the error it returned.
func jsonlUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			out = append(out, path)
		}
		return nil
	}))
	return out
}

// TestTaskContext_StaleWorktreePointerIsFatalEvenWithAPinnedProjectID pins the
// decision that a pinned project-id does NOT license proceeding without a
// work root. A pin answers WHICH project, but WorkDir also anchors the
// project config layer, so continuing without one resolves a different
// homing mode and a different tag schema than the project declared.
func TestTaskContext_StaleWorktreePointerIsFatalEvenWithAPinnedProjectID(t *testing.T) {
	for _, pinned := range []string{"", "pinned-id"} {
		name := "no pin"
		if pinned != "" {
			name = "pinned project-id"
		}
		t.Run(name, func(t *testing.T) {
			taskstest.Isolate(t)
			wt := staleLinkedWorktree(t)
			taskstest.ChangeDir(t, wt)
			if pinned != "" {
				t.Setenv("CTXLOOM_PROJECT_ID", pinned)
			}

			_, err := taskContext()
			require.Error(t, err, "a stale worktree pointer must refuse regardless of a pin")
			assert.Contains(t, err.Error(), filepath.Join(filepath.Dir(wt), "main"),
				"the refusal must name the missing primary checkout, which is the actionable part")
		})
	}
}

// TestRunAdd_StaleWorktreePointerWithAPinWritesNothingAnywhere is the effect
// half: `taskloom add` under a stale pointer must leave no bytes behind, in
// either candidate store. Before this was made fatal the add SUCCEEDED and
// wrote to ~/.ctxloom/tasks/<pinned-id>.jsonl while the project's declared
// repo-homed log was never created — exit 0, a success line, and a task in a
// store that project never reads.
func TestRunAdd_StaleWorktreePointerWithAPinWritesNothingAnywhere(t *testing.T) {
	home := taskstest.Isolate(t)
	wt := staleLinkedWorktree(t)
	taskstest.ChangeDir(t, wt)
	t.Setenv("CTXLOOM_PROJECT_ID", "pinned-id")

	// assert, not require: the store assertions below are the ones that
	// matter, and a require here would abort before they ever ran — leaving
	// them permanently unexercised and unable to fail.
	err := runAdd(addCmd, []string{"a task that must not land anywhere"})
	assert.Error(t, err)

	assert.Empty(t, jsonlUnder(t, home),
		"no task log may be written under HOME: the pinned id must not redirect the store")
	assert.NoFileExists(t, filepath.Join(wt, ".taskloom", "tasks.jsonl"),
		"nor may one be written to the repo-homed log")
}
