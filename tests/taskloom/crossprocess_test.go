// Package tests holds binary-level integration tests: they build the real
// `tasks` binary and drive it as separate OS processes. The in-process unit
// tests only prove the sync.Mutex serializes goroutines within one process;
// these prove the on-disk filelock serializes independent processes.
package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// tasksBinary builds the tasks binary once per test run and returns its path.
func tasksBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tasks-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "tasks")
		cmd := exec.Command("go", "build", "-o", binPath, "github.com/ctxloom/ctxloom/cmd/taskloom")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build tasks binary: %v\n%s", err, out)
		}
	})
	require.NoError(t, buildErr)
	return binPath
}

// env is an isolated home + project directory for one test.
type env struct {
	bin     string
	homeDir string
	projDir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{bin: tasksBinary(t), homeDir: t.TempDir(), projDir: t.TempDir()}
	// Seed the project identity once before any concurrency: identity minting
	// is first-sight (registry + in-tree marker) and is NOT what these tests
	// exercise — a fresh project hit by N parallel first-ever processes can
	// legitimately fork several identities. Real sessions resolve identity at
	// launch (`ctxloom run` exports CTXLOOM_PROJECT_ID) before any tool runs.
	if _, stderr, err := e.run(nil, "list"); err != nil {
		t.Fatalf("seed project identity: %v: %s", err, stderr)
	}
	return e
}

// run executes the tasks binary in the isolated env, capturing stdout and
// stderr separately (warnings ride stderr; JSON rides stdout). Safe to call
// concurrently.
func (e *env) run(extraEnv []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = e.projDir
	cmd.Env = append([]string{
		"HOME=" + e.homeDir,
		"USERPROFILE=" + e.homeDir,
		"PATH=" + os.Getenv("PATH"),
	}, extraEnv...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// jsonTask mirrors the wire shape emitted by `tasks list --json`
// (tasks.Task carries snake_case json tags).
type jsonTask struct {
	HarpID   string `json:"harp_id"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Checked  bool   `json:"checked"`
	TextHash string `json:"text_hash"`
}

// TestCrossProcessConcurrentAdd launches N independent `tasks add` processes
// against ONE per-project task log and asserts no write was lost, no harp ID
// was duplicated, and the on-disk JSONL log is not torn. CTXLOOM_SESSION_HARP
// only stamps each event's origin session; the log location is keyed by
// project, not harp.
func TestCrossProcessConcurrentAdd(t *testing.T) {
	e := newEnv(t)

	const n = 40
	harpEnv := []string{"CTXLOOM_SESSION_HARP=cross-proc-harp"}

	var wg sync.WaitGroup
	errs := make([]error, n)
	outs := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := fmt.Sprintf("concurrent-task-%03d", i)
			_, stderr, err := e.run(harpEnv, "add", text)
			errs[i] = err
			outs[i] = stderr
		}(i)
	}
	wg.Wait()

	for i := range n {
		require.NoErrorf(t, errs[i], "process %d failed: %s", i, outs[i])
	}

	// Read the final store back through the CLI (single process, no race).
	listOut, listErr, err := e.run(harpEnv, "list", "--json")
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
	for i := range n {
		text := fmt.Sprintf("concurrent-task-%03d", i)
		assert.Equalf(t, 1, seenText[text], "task %q should appear exactly once, saw %d", text, seenText[text])
	}

	// The isolated test home holds exactly one per-project log; locate it and
	// assert it is intact.
	logs, err := filepath.Glob(filepath.Join(e.homeDir, ".ctxloom", "tasks", "*.jsonl"))
	require.NoError(t, err)
	require.Lenf(t, logs, 1, "expected exactly one per-project task log, found %v", logs)
	raw, err := os.ReadFile(logs[0])
	require.NoError(t, err, "task log should exist at %s", logs[0])

	// Not torn: every non-empty line is a well-formed JSON event, and exactly N
	// of them are `add`s. A torn append (interleaved or partial write) would
	// leave a line that fails to parse or throw off the add count.
	adds := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Op string `json:"op"`
		}
		require.NoErrorf(t, json.Unmarshal([]byte(line), &ev), "malformed (torn?) event line: %q", line)
		if ev.Op == "add" {
			adds++
		}
	}
	assert.Equalf(t, n, adds, "expected %d add events on disk", n)
}

// TestCrossProcessReadDuringWrites ensures concurrent readers (`tasks list`)
// never observe a torn store while writers (`tasks add`) are mutating it —
// the filelock + O_APPEND write path must present readers an all-or-nothing
// view.
func TestCrossProcessReadDuringWrites(t *testing.T) {
	e := newEnv(t)

	const writers = 20
	const readers = 10
	harpEnv := []string{"CTXLOOM_SESSION_HARP=reader-writer-harp"}

	var wg sync.WaitGroup

	// Writers.
	writerErr := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := e.run(harpEnv, "add", fmt.Sprintf("rw-task-%03d", i))
			writerErr[i] = err
		}(i)
	}

	// Readers interleaved with the writers. Each must either succeed with
	// valid JSON or, at worst, an empty list — never a parse error.
	readerBad := make([]string, readers)
	for i := range readers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, serr, err := e.run(harpEnv, "list", "--json")
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

	for i := range writers {
		require.NoErrorf(t, writerErr[i], "writer %d failed", i)
	}
	for i := range readers {
		require.Emptyf(t, readerBad[i], "%s", readerBad[i])
	}

	// Final state: all writer tasks present.
	listOut, listErr, err := e.run(harpEnv, "list", "--json")
	require.NoErrorf(t, err, "final list failed: %s", listErr)
	var got []jsonTask
	require.NoError(t, json.Unmarshal([]byte(listOut), &got))
	assert.Len(t, got, writers)
}
