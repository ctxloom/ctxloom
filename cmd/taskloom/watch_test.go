package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// `taskloom watch` accepted every --format value the root's persistent flag
// admits and then emitted JSONL regardless, so `--format yaml` got a
// confident, wrong answer with no diagnostic — the shape this project's
// silent-no-op family takes on a read. The stream is a JSONL wire contract a
// GUI subscribes to; there is no yaml or toml rendering of it to give, so the
// honest answer is to say so.
func TestWatch_RejectsAFormatItCannotProduce(t *testing.T) {
	for _, format := range []string{"yaml", "toml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			c := &cobra.Command{}
			c.Flags().String("format", format, "")
			// SET, not merely defaulted: checkWatchFormat refuses a format
			// someone ASKED for, and Resolve ignores a flag that was never
			// Changed — deriving json from a test binary's non-terminal stdout,
			// which watch can produce, so nothing would be refused.
			require.NoError(t, c.Flags().Set("format", format))
			err := checkWatchFormat(c)
			require.Error(t, err, "answering a %s request with JSONL is a silent wrong answer", format)
			assert.Contains(t, err.Error(), format, "the rejection must name the format asked for")
			assert.Contains(t, err.Error(), "JSONL", "and say what the stream actually is")
		})
	}
}

// The two values that DO map onto the stream keep working untouched — text is
// the default every existing subscriber invokes with, and its bytes are the
// JSONL contract, so rejecting it would break the very consumer this command
// exists for.
func TestWatch_AcceptsTheFormatsThatMapOntoTheStream(t *testing.T) {
	for _, format := range []string{"", "text", "json"} {
		c := &cobra.Command{}
		c.Flags().String("format", "text", "")
		if format != "" {
			require.NoError(t, c.Flags().Set("format", format))
		}
		assert.NoError(t, checkWatchFormat(c), "--format %q must stay accepted", format)
	}
}

// syncBuffer serializes writes from the watch goroutine against the test's
// reads. Nothing else in this package needed one, so it is declared here.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The watch stream is a WIRE CONTRACT: a GUI parses these lines and re-queries
// on each one. Nothing in this package referenced watchCmd, watchEvent or
// watchDebounce, so the field names, the constant values, the up-front emit a
// subscriber depends on to render without a separate initial query, and the
// project attribution were all free to drift silently. This drives the real
// command end to end against a hermetic project store.
func TestWatch_StreamsTheDocumentedJSONLContract(t *testing.T) {
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)
	projectID, logPath, err := operations.ResolveLogPath(tc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	c := &cobra.Command{}
	c.Flags().String("format", "text", "")
	c.SetContext(ctx)
	c.SetOut(out)

	done := make(chan error, 1)
	go func() { done <- watchCmd.RunE(c, nil) }()

	lines := func() []string {
		s := strings.TrimSpace(out.String())
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}

	// One line up front, before anything has changed.
	require.Eventually(t, func() bool { return len(lines()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"a subscriber must be able to render immediately, with no separate initial-query race")

	var ev watchEvent
	require.NoError(t, json.Unmarshal([]byte(lines()[0]), &ev))
	assert.Equal(t, watchEvent{Event: "changed", Kind: "tasks", Project: projectID}, ev,
		"the wire line's field VALUES are what a GUI keys on")
	// The field NAMES are the other half of the contract and a struct-equal
	// assertion cannot see them: a renamed json tag round-trips fine.
	var keys map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines()[0]), &keys))
	assert.Equal(t, []string{"event", "kind", "project"}, sortedKeys(keys))

	// A change to the project's own log produces another line.
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o755))
	require.NoError(t, os.WriteFile(logPath,
		[]byte(`{"op":"add","task":"alpha","text":"t","status":"To Do","ts":"2026-01-01T00:00:00Z"}`+"\n"), 0o644))
	require.Eventually(t, func() bool { return len(lines()) >= 2 }, 5*time.Second, 10*time.Millisecond,
		"an append to the watched log must produce an event line")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "an interrupted watch is a clean shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not return after its context was cancelled")
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The debounce exists to collapse the write+chmod+lock churn one append
// produces into one logical change. A zero value would silently restore
// per-filesystem-event emission, which is invisible in any assertion about
// the line's shape.
func TestWatch_DebounceIsSetAndSubSecond(t *testing.T) {
	assert.Positive(t, watchDebounce, "a zero debounce emits once per raw filesystem event")
	assert.Less(t, watchDebounce, time.Second, "a debounce longer than a second is a visibly laggy GUI")
}

// The help text advertises an exact example line. A GUI author reads that
// line, not the struct, so the two must not drift apart.
func TestWatch_HelpExampleMatchesTheEmittedShape(t *testing.T) {
	raw, err := json.Marshal(watchEvent{Event: "changed", Kind: "tasks", Project: "swift-amber-falcon"})
	require.NoError(t, err)
	assert.Contains(t, watchCmd.Long, string(raw),
		"the documented example must be the bytes watchEvent actually marshals to")
}
