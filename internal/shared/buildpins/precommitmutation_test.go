package buildpins

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pre-commit mutation step, scripts/hook-mutation-staged. It must judge
// the STAGED change and nothing else, and leave the user's checkout exactly as
// it found it. Each test drives the real script over a throwaway repository,
// with a stub standing in for gomutants: the stub records the tree it was run
// in, so what the mutation would have seen is asserted directly.

const mutationScript = "../../../scripts/hook-mutation-staged"

// mutationStub records the materialised tree into $OUT and exits $STUB_EXIT.
const mutationStub = `#!/usr/bin/env bash
set -eu
pwd > "$OUT/tree-path"
for f in a.go b.go d.go; do
    if [ -e "$f" ]; then cp "$f" "$OUT/$f"; fi
done
if [ -e c.go ]; then touch "$OUT/c-present"; fi
git status --porcelain > "$OUT/status"
exit "${STUB_EXIT:-0}"
`

const (
	aCommitted = "package a\n\nfunc A() int { return 1 }\n"
	aStaged    = "package a\n\nfunc A() int { return 2 }\n"
	aUnstaged  = "package a\n\nfunc A() int { return 3 }\n"
	bCommitted = "package a\n\nfunc B() int { return 1 }\n"
	bUnstaged  = "package a\n\nfunc B() int { return 9 }\n"
	dStaged    = "package a\n\nfunc D() int { return 4 }\n"
)

// mutationRun is one run of the step: the stub's record and the scratch root.
type mutationRun struct {
	code    int
	out     string
	record  string
	scratch string
}

// runMutation runs the step against r with the stub exiting stubExit.
func runMutation(t *testing.T, r *hookRepo, stubExit string) mutationRun {
	t.Helper()
	tools := t.TempDir()
	stub := filepath.Join(tools, "stub.sh")
	require.NoError(t, os.WriteFile(stub, []byte(mutationStub), 0o755))
	record := filepath.Join(tools, "record")
	require.NoError(t, os.Mkdir(record, 0o755))
	scratch := filepath.Join(tools, "mutation-tmp")

	code, out := r.run([]string{
		"CTXLOOM_MUTATION_TMP=" + scratch,
		"OUT=" + record,
		"STUB_EXIT=" + stubExit,
		// What git exports into every pre-commit hook: the real index. The
		// step must read it, and must keep it away from the throwaway tree.
		"GIT_INDEX_FILE=" + filepath.Join(r.dir, ".git", "index"),
	}, mutationScript, "bash", stub)
	return mutationRun{code: code, out: out, record: record, scratch: scratch}
}

func (m mutationRun) recorded(t *testing.T, name string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.record, name))
	if os.IsNotExist(err) {
		return "", false
	}
	require.NoError(t, err)
	return string(raw), true
}

// assertLeftNoTrace: the throwaway worktree, its registration and its scratch
// are all gone.
func (m mutationRun) assertLeftNoTrace(t *testing.T, r *hookRepo) {
	t.Helper()
	if tree, ok := m.recorded(t, "tree-path"); ok {
		_, err := os.Stat(strings.TrimSpace(tree))
		assert.True(t, os.IsNotExist(err), "the throwaway worktree %s is still on disk", tree)
	}
	assert.Equal(t, 1, strings.Count(r.git("worktree", "list", "--porcelain"), "worktree "),
		"a worktree registration outlived the run:\n%s", r.git("worktree", "list"))
	entries, err := os.ReadDir(m.scratch)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		assert.Empty(t, entries, "the run's scratch outlived it")
	}
}

// checkoutState is every byte of the user's checkout the step must not touch:
// the index file itself, and each file in the working tree.
func checkoutState(t *testing.T, r *hookRepo, files ...string) map[string]string {
	t.Helper()
	state := map[string]string{".git/index": string(r.rawIndex())}
	for _, f := range files {
		state[f] = r.read(f)
	}
	return state
}

func TestPreCommitMutation_JudgesOnlyTheStagedChange(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"a.go": aCommitted, "b.go": bCommitted})
	r.stage("a.go", aStaged)
	r.write("a.go", aUnstaged)     // a.go is partially staged
	r.write("b.go", bUnstaged)     // b.go is changed but not staged at all
	r.write("c.go", "package a\n") // c.go is untracked
	r.stage("d.go", dStaged)       // d.go is a staged new file
	before := checkoutState(t, r, "a.go", "b.go", "c.go", "d.go")

	run := runMutation(t, r, "0")

	require.Equal(t, 0, run.code, run.out)
	a, _ := run.recorded(t, "a.go")
	assert.Equal(t, aStaged, a, "the tree under test carries the staged a.go, not the working copy")
	b, _ := run.recorded(t, "b.go")
	assert.Equal(t, bCommitted, b, "an unstaged edit is not in the tree under test")
	_, untracked := run.recorded(t, "c-present")
	assert.False(t, untracked, "an untracked file is not in the tree under test")
	d, _ := run.recorded(t, "d.go")
	assert.Equal(t, dStaged, d, "a staged new file is in the tree under test")
	status, _ := run.recorded(t, "status")
	assert.Contains(t, status, "A  d.go", "the staged new file is tracked there, so the diff scope sees it")
	assert.Equal(t, before, checkoutState(t, r, "a.go", "b.go", "c.go", "d.go"),
		"the user's index and working tree are byte-identical after the run")
	assert.Empty(t, strings.TrimSpace(r.git("stash", "list")), "the stash is untouched")
	run.assertLeftNoTrace(t, r)
}

func TestPreCommitMutation_BelowThresholdRefusesAndCleansUp(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"a.go": aCommitted})
	r.stage("a.go", aStaged)
	before := checkoutState(t, r, "a.go")

	run := runMutation(t, r, "10")

	assert.Equal(t, 10, run.code, run.out)
	assert.Contains(t, run.out, "below the project's mutation threshold")
	assert.Equal(t, before, checkoutState(t, r, "a.go"))
	run.assertLeftNoTrace(t, r)
}

func TestPreCommitMutation_FailedRunNamesTheCoveringTestsAndCleansUp(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"a.go": aCommitted})
	r.stage("a.go", aStaged)
	before := checkoutState(t, r, "a.go")

	run := runMutation(t, r, "1")

	assert.Equal(t, 1, run.code, run.out)
	assert.Contains(t, run.out, "a test that fails on what\nyou staged fails here first")
	assert.Equal(t, before, checkoutState(t, r, "a.go"))
	run.assertLeftNoTrace(t, r)
}

func TestPreCommitMutation_NoStagedProductionGoMakesNoWorktree(t *testing.T) {
	r := newHookRepo(t)
	r.commit(map[string]string{"a.go": aCommitted, "a_test.go": "package a\n", "README.md": "x\n"})
	r.stage("a_test.go", "package a\n\n// more\n")
	r.stage("README.md", "y\n")
	r.write("a.go", aUnstaged) // production Go changed, but not staged

	run := runMutation(t, r, "0")

	require.Equal(t, 0, run.code, run.out)
	assert.Contains(t, run.out, "no staged production Go")
	_, ran := run.recorded(t, "tree-path")
	assert.False(t, ran, "the mutation command ran with nothing to mutate")
	_, err := os.Stat(run.scratch)
	assert.True(t, os.IsNotExist(err), "a scratch dir was made with nothing to mutate")
}

// The step's scratch default and the mutation recipes' must be the same
// directory: both put a run's scratch on disk rather than on a tmpfs /tmp.
func TestPreCommitMutation_ScratchDefaultMatchesTheMutationRecipes(t *testing.T) {
	recipe := regexp.MustCompile(`(?m)^mutation_tmp := env_var_or_default\("CTXLOOM_MUTATION_TMP", "([^"]+)"\)$`).
		FindStringSubmatch(readFile(t, ciJustfilePath))
	require.NotNil(t, recipe, "%s no longer declares mutation_tmp from CTXLOOM_MUTATION_TMP", ciJustfilePath)
	script := regexp.MustCompile(`(?m)^base=\$\{CTXLOOM_MUTATION_TMP:-([^}]+)\}$`).
		FindStringSubmatch(readFile(t, mutationScript))
	require.NotNil(t, script, "%s no longer defaults its scratch from CTXLOOM_MUTATION_TMP", mutationScript)
	assert.Equal(t, recipe[1], script[1])
}
