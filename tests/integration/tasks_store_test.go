//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCtxloom runs the ctxloom binary with the given extra environment, capturing
// stdout and stderr separately (so warnings on stderr don't corrupt JSON parsed
// from stdout) and is safe to call concurrently.
func runCtxloom(env *testenv.TestEnvironment, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	cmd := env.Command(extraEnv, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// jsonTask mirrors the field names emitted by `ctxloom tasks list --json`
// (internal/tasks.Task has no json tags, so the keys are the Go field names).
type jsonTask struct {
	HarpID   string
	Text     string
	Status   string
	Checked  bool
	TextHash string
}

// TestTasksStore_CrossProcessConcurrentAdd pins unmovable-revivable-guidance at
// the cross-PROCESS level. The in-process unit test (TestAdd_ConcurrentNoLostWrites)
// only proves the sync.Mutex serializes goroutines within one process; it cannot
// prove the on-disk filelock serializes separate OS processes. Here we launch N
// independent `ctxloom tasks add` processes against ONE harp-keyed store
// (same CTXLOOM_SESSION_HARP) and assert no write was lost, no harp ID was
// duplicated, and the resulting tasks.md is not torn.
func TestTasksStore_CrossProcessConcurrentAdd(t *testing.T) {
	env := setupTestEnv(t)

	const harp = "cross-proc-harp"
	const n = 40
	harpEnv := []string{"CTXLOOM_SESSION_HARP=" + harp}

	// Fire N separate processes concurrently, each adding one uniquely-named
	// task. Each gets its own *exec.Cmd / output buffer (Command, unlike Run,
	// is concurrency-safe).
	var wg sync.WaitGroup
	errs := make([]error, n)
	outs := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := fmt.Sprintf("concurrent-task-%03d", i)
			_, stderr, err := runCtxloom(env, harpEnv, "tasks", "add", text)
			errs[i] = err
			outs[i] = stderr
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoErrorf(t, errs[i], "process %d failed: %s", i, outs[i])
	}

	// Read the final store back through the CLI (single process, no race).
	// Must carry the same harp env so it reads the harp-keyed store.
	listOut, listErr, err := runCtxloom(env, harpEnv, "tasks", "list", "--json")
	require.NoErrorf(t, err, "tasks list failed: %s", listErr)

	var got []jsonTask
	require.NoError(t, json.Unmarshal([]byte(listOut), &got),
		"tasks list --json did not produce valid JSON (torn store?):\n%s", listOut)

	// No lost writes: exactly N tasks survived.
	assert.Len(t, got, n, "expected %d tasks after %d concurrent adds", n, n)

	// No duplicated harp IDs and every expected text is present exactly once.
	seenHarp := make(map[string]bool, len(got))
	seenText := make(map[string]int, len(got))
	for _, task := range got {
		assert.Falsef(t, seenHarp[task.HarpID], "duplicate harp ID %q", task.HarpID)
		seenHarp[task.HarpID] = true
		seenText[task.Text]++
	}
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("concurrent-task-%03d", i)
		assert.Equalf(t, 1, seenText[text], "task %q should appear exactly once, saw %d", text, seenText[text])
	}

	// The store must live at the harp-keyed path, not the legacy project file.
	harpStore := filepath.Join(env.HomeDir, ".ctxloom", "sessions", harp, "tasks.md")
	raw, err := os.ReadFile(harpStore)
	require.NoError(t, err, "harp-keyed store should exist at %s", harpStore)

	// Not torn: every task-bullet line is well-formed ("- [ ] `harp-id` text"
	// or "- [x] ..."), and there are exactly N of them. A torn read-modify-write
	// would leave a truncated or doubled bullet that fails this shape check or
	// throws off the count.
	bulletRE := regexp.MustCompile(`^- \[[ x]\] ` + "`" + `[a-z-]+` + "`" + ` \S`)
	bullets := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		bullets++
		assert.Truef(t, bulletRE.MatchString(line), "malformed (torn?) task line: %q", line)
	}
	assert.Equalf(t, n, bullets, "expected %d task bullets on disk", n)
}

// TestTasksStore_CrossProcessReadDuringWrites ensures concurrent readers
// (`tasks list`) never observe a torn store while writers (`tasks add`) are
// mutating it — the filelock+atomic-rename write path must present readers an
// all-or-nothing view.
func TestTasksStore_CrossProcessReadDuringWrites(t *testing.T) {
	env := setupTestEnv(t)

	const harp = "reader-writer-harp"
	const writers = 20
	const readers = 10
	harpEnv := []string{"CTXLOOM_SESSION_HARP=" + harp}

	var wg sync.WaitGroup

	// Writers.
	writerErr := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := runCtxloom(env, harpEnv, "tasks", "add", fmt.Sprintf("rw-task-%03d", i))
			writerErr[i] = err
		}(i)
	}

	// Readers interleaved with the writers. Each must either succeed with
	// valid JSON or, at worst, an empty list — never a parse error.
	readerBad := make([]string, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, serr, err := runCtxloom(env, harpEnv, "tasks", "list", "--json")
			if err != nil {
				readerBad[i] = fmt.Sprintf("reader %d errored: %v: %s", i, err, serr)
				return
			}
			var parsed []jsonTask
			if jerr := json.Unmarshal([]byte(out), &parsed); jerr != nil {
				readerBad[i] = fmt.Sprintf("reader %d got non-JSON (torn read): %s", i, out)
			}
		}(i)
	}

	wg.Wait()

	for i := 0; i < writers; i++ {
		require.NoErrorf(t, writerErr[i], "writer %d failed", i)
	}
	for i := 0; i < readers; i++ {
		require.Emptyf(t, readerBad[i], readerBad[i])
	}

	// Final state: all writer tasks present.
	listOut, listErr, err := runCtxloom(env, harpEnv, "tasks", "list", "--json")
	require.NoErrorf(t, err, "final list failed: %s", listErr)
	var got []jsonTask
	require.NoError(t, json.Unmarshal([]byte(listOut), &got))
	assert.Len(t, got, writers)
}
