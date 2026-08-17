package backends

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// chatOneTurn drives one default (echo) turn through Chat and returns once the
// turn completes or the deadline expires. Chat owns closing out, so the caller
// only has to stop feeding input.
func chatOneTurn(t *testing.T, req agent.ChatRequest, text string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 1)
	out := make(chan agent.ChatEvent, 8)
	in <- agent.ChatMessage{Text: text}

	done := make(chan error, 1)
	go func() { done <- NewMock().Chat(ctx, req, in, out) }()

	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return <-done
			}
			if ev.Complete != nil {
				close(in) // ends the conversation; Chat returns and closes out
			}
		case err := <-done:
			return err
		case <-ctx.Done():
			t.Fatal("Chat did not complete within the deadline")
			return nil
		}
	}
}

// TestMockChat_WritesTheRecordExecuteWouldHave closes the asymmetry that left
// the container transport with no evidence at all.
//
// A container structured/oneshot run is driven through Chat (run.go's
// armOwnedRunContainer); every other arm goes through Execute. While only
// Execute recorded, a containerized run and a run that silently never
// containerized produced the identical observable — exit 0 and an echo — so no
// scenario could assert WHERE the engine ran on the one axis where that
// question is the whole point.
func TestMockChat_WritesTheRecordExecuteWouldHave(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "record.txt")

	err := chatOneTurn(t, agent.ChatRequest{
		WorkDir: "/somewhere/workspace",
		Env:     map[string]string{"CTXLOOM_MOCK_RECORD_FILE": recordFile},
	}, "prove the turn reached the engine")
	require.NoError(t, err)

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err, "Chat must leave the same record Execute does")
	content := string(data)

	// The turn's own text, not a fixed template: this is what proves the
	// record describes THIS turn rather than being written unconditionally.
	assert.Contains(t, content, "prove the turn reached the engine")
	assert.Contains(t, content, "workdir=/somewhere/workspace")
}

// TestMockChat_NoRecordFileWritesNothing keeps the knob a knob: every existing
// Chat-driven test runs without CTXLOOM_MOCK_RECORD_FILE set, and recording
// must stay opt-in rather than scattering files through their temp dirs.
func TestMockChat_NoRecordFileWritesNothing(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, chatOneTurn(t, agent.ChatRequest{WorkDir: dir}, "no record requested"))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no record file was requested, so none may be written")
}

// TestMockChat_RecordFileWriteFailurePropagates mirrors Execute's own record
// write contract: a swallowed write failure lets a later assertion read a
// STALE record from a previous run and call it a pass. recordFile is a
// directory, so the write must fail.
func TestMockChat_RecordFileWriteFailurePropagates(t *testing.T) {
	err := chatOneTurn(t, agent.ChatRequest{
		Env: map[string]string{"CTXLOOM_MOCK_RECORD_FILE": t.TempDir()},
	}, "this turn cannot be recorded")
	require.Error(t, err, "a record-file write failure must not be swallowed as success")
}

// TestWriteMockRecord_RecordsWhereTheEngineRan pins the two signals a container
// scenario reads, and pins them as TWO because neither is sufficient alone.
//
// container_markers is a heuristic that reads true on both sides when the test
// harness itself runs inside a devcontainer — trusting it alone would let a
// scenario pass with no container ever launched. hostname breaks that tie: a
// container gets its own UTS namespace, so it never matches the launching
// process's hostname. cwd and workdir cannot serve at all, because the
// container mounts the project at the SAME absolute path by design (measured:
// a containerized run's record showed cwd and workdir byte-identical to the
// host run's, while hostname and container_markers both differed).
func TestWriteMockRecord_RecordsWhereTheEngineRan(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "record.txt")

	require.NoError(t, writeMockRecord(recordFile, mockRecordFields{WorkDir: "/w"}, nil))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	host, err := os.Hostname()
	require.NoError(t, err)
	require.NotEmpty(t, host, "an empty hostname would make the container comparison vacuous")
	assert.Contains(t, content, "hostname="+host)
	assert.Contains(t, content, "container_markers=")
}
